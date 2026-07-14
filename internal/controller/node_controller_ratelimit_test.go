package controller

import (
	"context"
	"sync/atomic"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/prometheus/client_golang/prometheus/testutil"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/events"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	kubeheadroomv1alpha1 "github.com/karlkfi/kube-headroom/api/v1alpha1"
)

// These specs cover the §7 / §6.2c per-node write-pressure bound: the per-node
// token bucket in Reconcile that caps resize patches-per-second per node. When
// several managed pods on one node all want a resize, the bucket lets a bounded
// number through the first pass, then breaks the loop and asks controller-runtime
// to requeue (a non-zero RequeueAfter) so the rest are applied on a follow-up.
// Like the resize-error specs, this drives a fake client (whose resize PATCH is
// intercepted) rather than envtest, so the bucket state is deterministic — a real
// apiserver would accept every patch and never exercise the break.

const rateLimitNodeName = "ratelimit-node"

// rateLimitConfig builds a non-dry-run singleton whose per-node patch rate is
// pinned to pps. The policy knobs mirror the CRD defaults (the fake client applies
// no apiserver defaulting) so each solo Burstable pod computes a real, applied
// resize; only the rate bound decides how many land per pass.
func rateLimitConfig(pps int32) *kubeheadroomv1alpha1.HeadroomConfig {
	dr := false
	return &kubeheadroomv1alpha1.HeadroomConfig{
		ObjectMeta: metav1.ObjectMeta{Name: kubeheadroomv1alpha1.SingletonName},
		Spec: kubeheadroomv1alpha1.HeadroomConfigSpec{
			DryRun:        &dr,
			MinBurstFloor: resource.MustParse("1"),
			MaxMultiplier: resource.MustParse("10"),
			Quantum:       resource.MustParse("10m"),
			Deadband:      kubeheadroomv1alpha1.Deadband{GrowPercent: 10, ShrinkPercent: 5},
			RateLimits:    kubeheadroomv1alpha1.RateLimits{PerNodePatchesPerSecond: pps},
		},
	}
}

// rateLimitNode is an 8-core Linux node (in-place resize allowed).
func rateLimitNode() *corev1.Node {
	return &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{Name: rateLimitNodeName},
		Status: corev1.NodeStatus{
			Allocatable: corev1.ResourceList{corev1.ResourceCPU: *resource.NewQuantity(8, resource.DecimalSI)},
		},
	}
}

// rateLimitPod is a solo Burstable pod (1000m request, no limit) bound to the
// node. QOSClass is set explicitly because the fake client runs no admission to
// populate it, and Eligible gates on pod.Status.QOSClass == Burstable.
func rateLimitPod(name string) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: nsA},
		Spec: corev1.PodSpec{
			NodeName: rateLimitNodeName,
			Containers: []corev1.Container{{
				Name:  cApp,
				Image: imgBusybox,
				Resources: corev1.ResourceRequirements{
					Requests: corev1.ResourceList{corev1.ResourceCPU: *resource.NewMilliQuantity(1000, resource.DecimalSI)},
				},
			}},
		},
		Status: corev1.PodStatus{Phase: corev1.PodRunning, QOSClass: corev1.PodQOSBurstable},
	}
}

// newRateLimitReconciler wires a NodeReconciler onto a fake client seeded with the
// rate-limited config, the node, the managed namespace (reused from the error
// specs), and one managed pod per name. Every resize-subresource PATCH is counted
// and allowed, so the returned counter is exactly the number of resizes the
// per-node bucket admitted this reconcile.
func newRateLimitReconciler(pps int32, podNames ...string) (*NodeReconciler, *atomic.Int64) {
	patched := &atomic.Int64{}

	objs := make([]client.Object, 0, 3+len(podNames))
	objs = append(objs, rateLimitConfig(pps), rateLimitNode(), resizeErrNamespace())
	for _, n := range podNames {
		objs = append(objs, rateLimitPod(n))
	}

	c := fake.NewClientBuilder().
		WithScheme(scheme.Scheme).
		WithObjects(objs...).
		WithIndex(&corev1.Pod{}, podNodeNameIndex, func(o client.Object) []string {
			n := o.(*corev1.Pod).Spec.NodeName
			if n == "" {
				return nil
			}
			return []string{n}
		}).
		WithInterceptorFuncs(interceptor.Funcs{
			SubResourcePatch: func(_ context.Context, _ client.Client, subResourceName string, _ client.Object, _ client.Patch, _ ...client.SubResourcePatchOption) error {
				// Only the resize subresource is patched here; count each accepted
				// resize so the test can assert the per-node bound held.
				if subResourceName == "resize" {
					patched.Add(1)
				}
				return nil
			},
		}).
		Build()

	rec := events.NewFakeRecorder(64)
	return &NodeReconciler{Client: c, Scheme: scheme.Scheme, Recorder: rec}, patched
}

var _ = Describe("NodeReconciler per-node rate-limiter break (§7, §6.2c)", func() {
	runReconcile := func(r *NodeReconciler) (reconcile.Result, error) {
		return r.Reconcile(ctx, reconcile.Request{NamespacedName: types.NamespacedName{Name: rateLimitNodeName}})
	}

	It("bounds patches to the bucket and requeues the rest when the bucket is exhausted", func() {
		// pps=1 → a burst of 1 token: the first managed pod's resize drains the
		// bucket, the loop breaks, and the remaining two are deferred to the requeue.
		r, patched := newRateLimitReconciler(1, "rl-a", "rl-b", "rl-c")

		before := testutil.ToFloat64(resizesTotal.WithLabelValues(resultApplied))
		res, err := runReconcile(r)

		Expect(err).NotTo(HaveOccurred())
		// Bucket exhausted mid-pass: a non-zero RequeueAfter drives the follow-up
		// reconcile that applies the deferred pods (§6.2 step 4c).
		Expect(res.RequeueAfter).To(Equal(time.Second))

		// Exactly one resize landed — the bucket admitted a single patch, the break
		// stopped the rest.
		Expect(patched.Load()).To(Equal(int64(1)), "only one pod may be patched under a 1 patch/s bucket")
		Expect(testutil.ToFloat64(resizesTotal.WithLabelValues(resultApplied))).
			To(Equal(before+1), "applied counter must move exactly once")
	})

	It("applies every pod and does not requeue when the bucket has budget for all", func() {
		// pps=10 → burst 10, ample for three pods: all resize, no rate-limit break,
		// so the reconcile settles with a zero result. This isolates the break above
		// to the rate bound rather than any other skip.
		r, patched := newRateLimitReconciler(10, "rl-x", "rl-y", "rl-z")

		before := testutil.ToFloat64(resizesTotal.WithLabelValues(resultApplied))
		res, err := runReconcile(r)

		Expect(err).NotTo(HaveOccurred())
		Expect(res.IsZero()).To(BeTrue(), "an unexhausted bucket must not requeue")

		Expect(patched.Load()).To(Equal(int64(3)), "all three pods fit within a 10 patch/s bucket")
		Expect(testutil.ToFloat64(resizesTotal.WithLabelValues(resultApplied))).
			To(Equal(before + 3))
	})
})
