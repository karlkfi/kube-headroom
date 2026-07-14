package controller

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/prometheus/client_golang/prometheus/testutil"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/events"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	kubeheadroomv1alpha1 "github.com/karlkfi/kube-headroom/api/v1alpha1"
)

// These specs cover Q37: backoff state is in-memory only, so a controller
// restart / rollout / leader-failover starts with an empty backoff map. A fresh
// NodeReconciler modeling that startup must reconstruct backoff for a pod that
// already shows an Infeasible resize from its observable status, rather than
// retrying it immediately and re-issuing the failing patch + warning noise.
var _ = Describe("NodeReconciler backoff reconstruction after restart (Q37)", func() {
	// freshReconciler models a process that just started: no BeforeEach-seeded
	// backoff map, exactly as a crash/rollout/failover leaves it.
	var freshReconciler = func(rec events.EventRecorder) *NodeReconciler {
		return &NodeReconciler{Client: k8sClient, Scheme: k8sClient.Scheme(), Recorder: rec}
	}

	AfterEach(func() {
		hc := &kubeheadroomv1alpha1.HeadroomConfig{ObjectMeta: metav1.ObjectMeta{Name: kubeheadroomv1alpha1.SingletonName}}
		_ = k8sClient.Delete(ctx, hc)
	})

	It("re-enters backoff from pod status without a duplicate patch on the first post-startup reconcile", func() {
		rec := events.NewFakeRecorder(16)
		r := freshReconciler(rec)
		applyConfig(false) // live mode: absent the fix, the pod would be re-patched
		makeManagedNamespace()
		node := nextNode()
		makeNode(node, 8)
		makeBurstablePod(nsA, "restart-infeasible", node, 1000, 0)
		// The kubelet already refused a resize before the restart; the condition
		// survives on the pod even though the in-memory backoff map did not.
		setPodResizeInfeasible(nsA, "restart-infeasible")

		beforeApplied := testutil.ToFloat64(resizesTotal.WithLabelValues(resultApplied))
		beforeInfeasible := testutil.ToFloat64(resizesTotal.WithLabelValues(resultInfeasible))

		// First reconcile after "restart": the empty backoff map means the pod is a
		// management candidate, but the Infeasible condition must exclude it this
		// pass and arm backoff — no resize is issued.
		_, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: types.NamespacedName{Name: node}})
		Expect(err).NotTo(HaveOccurred())

		// Backoff is reconstructed (counter fires once) ...
		Expect(testutil.ToFloat64(resizesTotal.WithLabelValues(resultInfeasible))).
			To(BeNumerically("==", beforeInfeasible+1))
		Eventually(rec.Events).Should(Receive(ContainSubstring(reasonResizeInfeasible)))

		// ... but no duplicate failing write: result=applied does not move and the
		// pod exports no target series (it is excluded, contributing slack only).
		Expect(testutil.ToFloat64(resizesTotal.WithLabelValues(resultApplied))).
			To(BeNumerically("==", beforeApplied), "an Infeasible pod must not be re-patched on the first post-restart reconcile")
		_, ok := podLimitSeries("restart-infeasible")
		Expect(ok).To(BeFalse(), "backed-off pod must be excluded from management on its first reconcile")

		// No spurious CPULimitAdjusted event accompanies the (absent) patch.
		Consistently(func() bool {
			select {
			case e := <-rec.Events:
				Expect(e).NotTo(ContainSubstring(reasonCPULimitAdjusted))
				return true
			default:
				return true
			}
		}).Should(BeTrue())

		// The window holds on subsequent reconciles too (backoff, not a one-shot).
		_, err = r.Reconcile(ctx, reconcile.Request{NamespacedName: types.NamespacedName{Name: node}})
		Expect(err).NotTo(HaveOccurred())
		_, ok = podLimitSeries("restart-infeasible")
		Expect(ok).To(BeFalse())
	})
})
