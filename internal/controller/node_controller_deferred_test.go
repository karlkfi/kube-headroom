package controller

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/prometheus/client_golang/prometheus/testutil"
	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

var _ = Describe("NodeReconciler deferred-resize gauge (§6.4)", func() {
	deferredPod := func(name, reason string) *corev1.Pod {
		p := resizeErrPod(name)
		p.Status.Conditions = []corev1.PodCondition{{
			Type: corev1.PodResizePending, Status: corev1.ConditionTrue, Reason: reason,
		}}
		return p
	}

	It("counts managed pods whose PodResizePending reason is Deferred", func() {
		r, _ := newResizeErrReconciler("settled", nil)
		Expect(r.Create(ctx, deferredPod("held-a", corev1.PodReasonDeferred))).To(Succeed())
		Expect(r.Create(ctx, deferredPod("held-b", corev1.PodReasonDeferred))).To(Succeed())
		// Infeasible is a different §6.4 outcome with its own counter; it must not
		// inflate the deferred gauge.
		Expect(r.Create(ctx, deferredPod("refused", corev1.PodReasonInfeasible))).To(Succeed())

		_, err := r.Reconcile(ctx, reconcile.Request{Name: errNodeName})
		Expect(err).NotTo(HaveOccurred())
		Expect(testutil.ToFloat64(nodeResizesDeferred.WithLabelValues(errNodeName))).To(Equal(2.0))

		// Once the kubelet stops holding one, the gauge falls back.
		held := &corev1.Pod{}
		held.Namespace, held.Name = nsA, "held-a"
		Expect(r.Delete(ctx, held)).To(Succeed())

		_, err = r.Reconcile(ctx, reconcile.Request{Name: errNodeName})
		Expect(err).NotTo(HaveOccurred())
		Expect(testutil.ToFloat64(nodeResizesDeferred.WithLabelValues(errNodeName))).To(Equal(1.0))
	})

	It("drops the node's series when the node is forgotten", func() {
		before := testutil.CollectAndCount(nodeResizesDeferred)
		nodeResizesDeferred.WithLabelValues("gone-node").Set(3)
		Expect(testutil.CollectAndCount(nodeResizesDeferred)).To(Equal(before + 1))

		r, _ := newResizeErrReconciler("settled", nil)
		r.forgetNode("gone-node")
		Expect(testutil.CollectAndCount(nodeResizesDeferred)).To(Equal(before))
	})
})
