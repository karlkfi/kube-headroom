package controller

import (
	"context"
	"errors"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/prometheus/client_golang/prometheus/testutil"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/events"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	kubeheadroomv1alpha1 "github.com/karlkfi/kube-headroom/api/v1alpha1"
)

// These specs cover the §6.4 resize-error outcomes in classifyResizeError. Unlike
// the envtest specs (which drive a real apiserver that accepts every resize
// patch), here the resize subresource PATCH is intercepted on a controller-runtime
// fake client and made to fail, so the refusal table — 403 → quota-denied,
// Conflict → requeue, generic → bubble — is exercised directly. Quota 403 is a
// Phase-0-validated failure path (spike Q2c) and a §8.6 alerting signal, so it
// must not stay untested.

const errNodeName = "err-node"

// resizeErrConfig builds a non-dry-run singleton with the knobs the policy needs
// spelled out explicitly. The fake client applies no CRD defaults (there is no
// apiserver defaulting), so a zero spec would leave maxMultiplier/quantum at 0 and
// compute no target; these mirror the CRD defaults so the solo pod's target is a
// real, applied resize.
func resizeErrConfig() *kubeheadroomv1alpha1.HeadroomConfig {
	dr := false
	return &kubeheadroomv1alpha1.HeadroomConfig{
		ObjectMeta: metav1.ObjectMeta{Name: kubeheadroomv1alpha1.SingletonName},
		Spec: kubeheadroomv1alpha1.HeadroomConfigSpec{
			DryRun:        &dr,
			MinBurstFloor: resource.MustParse("1"),
			MaxMultiplier: resource.MustParse("10"),
			Quantum:       resource.MustParse("10m"),
			Deadband:      kubeheadroomv1alpha1.Deadband{GrowPercent: 10, ShrinkPercent: 5},
		},
	}
}

// resizeErrNode is an 8-core Linux node (in-place resize allowed).
func resizeErrNode() *corev1.Node {
	return &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{Name: errNodeName},
		Status: corev1.NodeStatus{
			Allocatable: corev1.ResourceList{corev1.ResourceCPU: *resource.NewQuantity(8, resource.DecimalSI)},
		},
	}
}

// resizeErrNamespace is the managed (mode=managed) namespace the pod lives in.
func resizeErrNamespace() *corev1.Namespace {
	return &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{
		Name:   nsA,
		Labels: map[string]string{kubeheadroomv1alpha1.LabelMode: kubeheadroomv1alpha1.ModeManaged},
	}}
}

// resizeErrPod is a solo Burstable pod (1000m request, no limit) bound to the
// node. QOSClass is set explicitly because the fake client runs no admission to
// populate it, and Eligible gates on pod.Status.QOSClass == Burstable.
func resizeErrPod(name string) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: nsA},
		Spec: corev1.PodSpec{
			NodeName: errNodeName,
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

// newResizeErrReconciler wires a NodeReconciler onto a fake client seeded with the
// config, node, namespace, and pod, whose resize-subresource PATCH is intercepted
// to fail with resizeErr. It returns the reconciler and its fake recorder.
func newResizeErrReconciler(podName string, resizeErr error) (*NodeReconciler, *events.FakeRecorder) {
	c := fake.NewClientBuilder().
		WithScheme(scheme.Scheme).
		WithObjects(resizeErrConfig(), resizeErrNode(), resizeErrNamespace(), resizeErrPod(podName)).
		WithIndex(&corev1.Pod{}, podNodeNameIndex, func(o client.Object) []string {
			n := o.(*corev1.Pod).Spec.NodeName
			if n == "" {
				return nil
			}
			return []string{n}
		}).
		WithInterceptorFuncs(interceptor.Funcs{
			SubResourcePatch: func(_ context.Context, _ client.Client, subResourceName string, _ client.Object, _ client.Patch, _ ...client.SubResourcePatchOption) error {
				// Only the resize subresource is patched in this path; fail it so the
				// §6.4 classifier runs on a real error.
				if subResourceName == "resize" {
					return resizeErr
				}
				return nil
			},
		}).
		Build()

	rec := events.NewFakeRecorder(16)
	return &NodeReconciler{Client: c, Scheme: scheme.Scheme, Recorder: rec}, rec
}

var _ = Describe("NodeReconciler resize-error outcomes (§6.4)", func() {
	runReconcile := func(r *NodeReconciler) (reconcile.Result, error) {
		return r.Reconcile(ctx, reconcile.Request{NamespacedName: types.NamespacedName{Name: errNodeName}})
	}

	// podStub reconstructs the key inBackoff/setBackoffKey use (ns/name) without
	// re-fetching the (interceptor-owned) pod.
	podStub := func(name string) *corev1.Pod {
		return &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: nsA, Name: name}}
	}

	It("meters quota-denied, warns, and backs off on a 403 Forbidden (spike Q2c)", func() {
		forbidden := apierrors.NewForbidden(schema.GroupResource{Resource: "pods"}, "quotapod",
			errors.New("exceeded quota: cpu-limits, requested: limits.cpu=8"))
		r, rec := newResizeErrReconciler("quotapod", forbidden)

		before := testutil.ToFloat64(resizesTotal.WithLabelValues(resultQuotaDenied))
		res, err := runReconcile(r)

		// A refused resize is handled, not fatal: no error, no requeue (the backoff
		// window, not a requeue, drives the retry).
		Expect(err).NotTo(HaveOccurred())
		Expect(res.IsZero()).To(BeTrue())

		Expect(testutil.ToFloat64(resizesTotal.WithLabelValues(resultQuotaDenied))).
			To(Equal(before + 1))
		Expect(r.inBackoff(podStub("quotapod"))).To(BeTrue(), "403 must place the pod in backoff")
		Eventually(rec.Events).Should(Receive(ContainSubstring(reasonResizeForbidden)))
	})

	It("meters conflict and requeues the node on a Conflict (stale generation)", func() {
		conflict := apierrors.NewConflict(schema.GroupResource{Resource: "pods"}, "conflictpod",
			errors.New("the object has been modified"))
		r, rec := newResizeErrReconciler("conflictpod", conflict)

		before := testutil.ToFloat64(resizesTotal.WithLabelValues(resultConflict))
		res, err := runReconcile(r)

		// Conflict is not fatal — the node requeues and recomputes from fresh state.
		Expect(err).NotTo(HaveOccurred())
		Expect(res.RequeueAfter).To(Equal(time.Second))

		Expect(testutil.ToFloat64(resizesTotal.WithLabelValues(resultConflict))).
			To(Equal(before + 1))
		// A conflict is transient, not operator-actionable: no warning event, no backoff.
		Expect(r.inBackoff(podStub("conflictpod"))).To(BeFalse())
		Consistently(rec.Events).ShouldNot(Receive())
	})

	It("meters error and bubbles up an unexpected patch error", func() {
		boom := errors.New("connection reset by peer")
		r, _ := newResizeErrReconciler("errorpod", boom)

		before := testutil.ToFloat64(resizesTotal.WithLabelValues(resultError))
		_, err := runReconcile(r)

		// An unclassified error must surface so controller-runtime requeues with backoff.
		Expect(err).To(HaveOccurred())
		Expect(errors.Is(err, boom)).To(BeTrue(), "the original error must be wrapped, not swallowed")

		Expect(testutil.ToFloat64(resizesTotal.WithLabelValues(resultError))).
			To(Equal(before + 1))
		Expect(r.inBackoff(podStub("errorpod"))).To(BeFalse())
	})
})
