package controller

import (
	"errors"
	"fmt"
	"slices"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/prometheus/client_golang/prometheus/testutil"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/events"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	kubeheadroomv1alpha1 "github.com/karlkfi/kube-headroom/api/v1alpha1"
	"github.com/karlkfi/kube-headroom/internal/eligibility"
)

// zeroRequest spells out a zero CPU request, without which the apiserver defaults
// the request to the container's limit.
func zeroRequest(c corev1.Container) corev1.Container {
	c.Resources.Requests[corev1.ResourceCPU] = resource.MustParse("0")
	return c
}

// These specs need a real apiserver: LimitRanger and ResourceQuota are admission
// plugins a fake client never runs, and their wording is what forbiddenOutcome reads.
// LimitRanges outlive a spec in the apiserver's own cache, so each spec gets a namespace.
var _ = Describe("NodeReconciler and namespace LimitRanges (§8.3)", func() {
	var (
		r   *NodeReconciler
		rec *events.FakeRecorder
		ns  string
	)
	var nsSeq int

	BeforeEach(func() {
		rec = events.NewFakeRecorder(64)
		r = &NodeReconciler{Client: k8sClient, Scheme: k8sClient.Scheme(), Recorder: rec}
		applyConfigWith(false, func(spec *kubeheadroomv1alpha1.HeadroomConfigSpec) {
			spec.Quantum = resource.MustParse("10m")
		})
		nsSeq++
		ns = fmt.Sprintf("limitrange-%d", nsSeq)
		Expect(k8sClient.Create(ctx, &corev1.Namespace{
			Name:   ns,
			Labels: map[string]string{kubeheadroomv1alpha1.LabelMode: kubeheadroomv1alpha1.ModeManaged},
		})).To(Succeed())
	})

	AfterEach(func() {
		_ = k8sClient.Delete(ctx, &kubeheadroomv1alpha1.HeadroomConfig{Name: kubeheadroomv1alpha1.SingletonName})
	})

	// awaitRefusal waits for the apiserver's admission informers to see a policy
	// object: until then a resize the spec expects refused would be accepted.
	awaitRefusal := func(wording string) {
		c := qosContainer(cApp, 1, 100_000, 0, 0)
		Eventually(func() string {
			probe := &corev1.Pod{Name: "probe", Namespace: ns, Spec: corev1.PodSpec{Containers: []corev1.Container{c}}}
			err := k8sClient.Create(ctx, probe, client.DryRunAll)
			if err == nil {
				return ""
			}
			return err.Error()
		}).Should(ContainSubstring(wording))
	}

	createLimitRange := func(name string, items ...corev1.LimitRangeItem) {
		Expect(k8sClient.Create(ctx, &corev1.LimitRange{
			Name: name, Namespace: ns, Spec: corev1.LimitRangeSpec{Limits: items},
		})).To(Succeed())
		awaitRefusal(" per ")
	}

	result := func(label string) float64 { return testutil.ToFloat64(resizesTotal.WithLabelValues(label)) }

	createShapedPod := func(node string, containers, sidecars []corev1.Container) *corev1.Pod {
		for i := range containers {
			containers[i].Name = fmt.Sprintf("c%d", i)
		}
		for i := range sidecars {
			sidecars[i].Name = fmt.Sprintf("s%d", i)
			sidecars[i] = sidecarOf(sidecars[i])
		}
		pod := &corev1.Pod{Name: "shape", Namespace: ns, Spec: corev1.PodSpec{NodeName: node, Containers: containers, InitContainers: sidecars}}
		Expect(k8sClient.Create(ctx, pod)).To(Succeed())
		return pod
	}

	capOf := func(pod *corev1.Pod) int64 {
		var list corev1.LimitRangeList
		Expect(k8sClient.List(ctx, &list, client.InNamespace(ns))).To(Succeed())
		return eligibility.LimitRangeCapMilli(pod, eligibility.ResizableContainers(pod), list.Items)
	}

	limitSum := func(pod *corev1.Pod) int64 {
		var got corev1.Pod
		Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(pod), &got)).To(Succeed())
		var sum int64
		for _, c := range slices.Concat(got.Spec.Containers, got.Spec.InitContainers) {
			sum += c.Resources.Limits.Cpu().MilliValue()
		}
		return sum
	}

	ratioOf := func(typ corev1.LimitType, ratio string) corev1.LimitRangeItem {
		return corev1.LimitRangeItem{Type: typ, MaxLimitRequestRatio: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse(ratio)}}
	}

	It("stops a raise at the container max instead of failing it (the row's measurement)", func() {
		createLimitRange("max", corev1.LimitRangeItem{Type: corev1.LimitTypeContainer, Max: cpuList(500)})
		node := nextNode()
		makeNode(node, 8)
		makeBurstablePod(ns, "capped", node, 100, 200)

		before := result(resultApplied)
		beforeDenied := result(resultLimitRangeDenied)
		_, err := r.Reconcile(ctx, reconcile.Request{Name: node})
		Expect(err).NotTo(HaveOccurred())

		Expect(podLimitMilli(ns, "capped")).To(Equal(int64(500)))
		Expect(result(resultApplied)).To(Equal(before + 1))
		Expect(result(resultLimitRangeDenied)).To(Equal(beforeDenied), "the clamp must prevent the refusal")

		By("a second pass finds nothing to change")
		var got corev1.Pod
		Expect(k8sClient.Get(ctx, types.NamespacedName{Namespace: ns, Name: "capped"}, &got)).To(Succeed())
		generation := got.Generation
		_, err = r.Reconcile(ctx, reconcile.Request{Name: node})
		Expect(err).NotTo(HaveOccurred())
		Expect(k8sClient.Get(ctx, types.NamespacedName{Namespace: ns, Name: "capped"}, &got)).To(Succeed())
		Expect(got.Generation).To(Equal(generation))
	})

	// The cap is the apiserver's rule restated, so each shape drives the real resize
	// at the cap and one milli-core past it and demands the two agree.
	DescribeTable("computes the cap the apiserver actually enforces",
		func(items []corev1.LimitRangeItem, containers, sidecars []corev1.Container, wantCap int64) {
			createLimitRange("rule", items...)
			node := nextNode()
			makeNodeWithMemory(node, 8)
			pod := createShapedPod(node, containers, sidecars)
			Expect(capOf(pod)).To(Equal(wantCap))

			Expect(r.applyResize(ctx, pod, wantCap)).To(Succeed(), "the cap itself must be admitted")
			err := r.applyResize(ctx, pod, wantCap+1)
			Expect(apierrors.IsForbidden(err)).To(BeTrue(), "one milli-core past the cap must be refused, got: %v", err)
			got, _, _ := forbiddenOutcome(err.Error())
			Expect(got).To(Equal(resultLimitRangeDenied), "refusal: %v", err)
		},
		Entry("container max, one container",
			[]corev1.LimitRangeItem{{Type: corev1.LimitTypeContainer, Max: cpuList(500)}},
			[]corev1.Container{qosContainer("", 100, 200, 0, 0)}, nil, int64(500)),
		Entry("container max, equal requests share it",
			[]corev1.LimitRangeItem{{Type: corev1.LimitTypeContainer, Max: cpuList(500)}},
			[]corev1.Container{qosContainer("", 100, 200, 0, 0), qosContainer("", 100, 200, 0, 0)}, nil, int64(1000)),
		Entry("container max reaches a native sidecar",
			[]corev1.LimitRangeItem{{Type: corev1.LimitTypeContainer, Max: cpuList(500)}},
			[]corev1.Container{qosContainer("", 100, 200, 0, 0)},
			[]corev1.Container{qosContainer("", 100, 200, 0, 0)}, int64(1000)),
		Entry("container ratio",
			[]corev1.LimitRangeItem{ratioOf(corev1.LimitTypeContainer, "3")},
			[]corev1.Container{qosContainer("", 100, 200, 0, 0)}, nil, int64(300)),
		Entry("container ratio whose exact limit the apiserver's rounding refuses",
			[]corev1.LimitRangeItem{ratioOf(corev1.LimitTypeContainer, "8.05")},
			[]corev1.Container{qosContainer("", 20, 100, 0, 0)}, nil, int64(160)),
		Entry("pod ratio whose exact limit the apiserver's rounding refuses",
			[]corev1.LimitRangeItem{ratioOf(corev1.LimitTypePod, "8.05")},
			[]corev1.Container{qosContainer("", 20, 100, 0, 0)}, nil, int64(160)),
		Entry("pod max",
			[]corev1.LimitRangeItem{{Type: corev1.LimitTypePod, Max: cpuList(700)}},
			[]corev1.Container{qosContainer("", 100, 200, 0, 0), qosContainer("", 100, 200, 0, 0)}, nil, int64(700)),
		Entry("pod max less a request-less container's own limit, which stays put",
			[]corev1.LimitRangeItem{{Type: corev1.LimitTypePod, Max: cpuList(700)}},
			[]corev1.Container{qosContainer("", 100, 200, 0, 0), zeroRequest(qosContainer("", 0, 150, 0, 0))}, nil, int64(550)),
		Entry("pod ratio",
			[]corev1.LimitRangeItem{ratioOf(corev1.LimitTypePod, "2.5")},
			[]corev1.Container{qosContainer("", 100, 200, 0, 0), qosContainer("", 100, 200, 0, 0)}, nil, int64(500)),
		Entry("the tightest rule wins",
			[]corev1.LimitRangeItem{
				{Type: corev1.LimitTypeContainer, Max: cpuList(900)},
				{Type: corev1.LimitTypePod, Max: cpuList(400)},
			},
			[]corev1.Container{qosContainer("", 100, 200, 0, 0)}, nil, int64(400)),
	)

	// A fractional ratio×request is not a limit any share can hold, and splitLimit
	// rounds one share up, so ratio×total itself is refused. The cap is safe rather
	// than tight here: wantCap+1 can round the other way and be admitted.
	DescribeTable("keeps a raise inside a fractional ratio",
		func(ratio string, containers, sidecars []corev1.Container, refused, wantCap, wantLimit int64) {
			createLimitRange("rule", ratioOf(corev1.LimitTypeContainer, ratio))
			node := nextNode()
			makeNodeWithMemory(node, 8)
			pod := createShapedPod(node, containers, sidecars)
			Expect(capOf(pod)).To(Equal(wantCap))

			err := r.applyResize(ctx, pod, refused)
			Expect(apierrors.IsForbidden(err)).To(BeTrue(), "ratio×total must be refused, got: %v", err)

			denied := result(resultLimitRangeDenied)
			_, err = r.Reconcile(ctx, reconcile.Request{Name: node})
			Expect(err).NotTo(HaveOccurred())
			Expect(result(resultLimitRangeDenied)).To(Equal(denied), "the clamp must prevent the refusal")

			Expect(limitSum(pod)).To(Equal(wantLimit), "the quantum takes the cap down to its grid")
			Expect(r.applyResize(ctx, pod, wantCap)).To(Succeed(), "the cap itself must be admitted")
		},
		Entry("round requests", "1.25",
			[]corev1.Container{qosContainer("", 250, 260, 0, 0), qosContainer("", 150, 160, 0, 0)}, nil,
			int64(500), int64(498), int64(490)),
		Entry("two containers", "1.5",
			[]corev1.Container{qosContainer("", 105, 110, 0, 0), qosContainer("", 95, 100, 0, 0)}, nil,
			int64(300), int64(298), int64(290)),
		Entry("a native sidecar", "1.5",
			[]corev1.Container{qosContainer("", 95, 100, 0, 0)},
			[]corev1.Container{qosContainer("", 105, 110, 0, 0)},
			int64(300), int64(298), int64(290)),
		Entry("a larger ratio", "2.5",
			[]corev1.Container{qosContainer("", 101, 150, 0, 0), qosContainer("", 99, 150, 0, 0)}, nil,
			int64(500), int64(498), int64(490)),
	)

	// The documented cost of that safety (§8.3): a very small request holds the whole
	// pod at its total request, though a higher target would split inside the rule.
	DescribeTable("stops short of a fractional ratio over a very small request",
		func(containers []corev1.Container, wantDenied, wantLimit, admitted int64) {
			node := nextNode()
			makeNodeWithMemory(node, 8)
			pod := createShapedPod(node, containers, nil)
			createLimitRange("rule", ratioOf(corev1.LimitTypeContainer, "1.05"))
			Expect(capOf(pod)).To(Equal(eligibility.PodCPURequestMilli(eligibility.ResizableContainers(pod))))

			denied := result(resultLimitRangeDenied)
			_, err := r.Reconcile(ctx, reconcile.Request{Name: node})
			Expect(err).NotTo(HaveOccurred())
			Expect(result(resultLimitRangeDenied)).To(Equal(denied + float64(wantDenied)))
			Expect(limitSum(pod)).To(Equal(wantLimit))
			Expect(r.applyResize(ctx, pod, admitted)).To(Succeed(), "a higher target was available")
		},
		Entry("a memory-pinned pod's QoS floor is refused",
			[]corev1.Container{qosContainer("", 100, 200, 64, 64), qosContainer("", 10, 20, 64, 64)},
			int64(1), int64(220), int64(115)),
		Entry("a plain pod gets no burst",
			[]corev1.Container{qosContainer("", 1000, 2000, 0, 0), qosContainer("", 10, 20, 0, 0)},
			int64(0), int64(1010), int64(1060)),
	)

	It("brings a running pod's limit down to a max below it", func() {
		node := nextNode()
		makeNode(node, 8)
		makeBurstablePod(ns, "over-max", node, 100, 1000)
		createLimitRange("max", corev1.LimitRangeItem{Type: corev1.LimitTypeContainer, Max: cpuList(300)})

		_, err := r.Reconcile(ctx, reconcile.Request{Name: node})
		Expect(err).NotTo(HaveOccurred())
		Expect(podLimitMilli(ns, "over-max")).To(Equal(int64(300)))
	})

	It("bends the §4.1 QoS floor to a cap inside its quantum", func() {
		node := nextNode()
		makeNodeWithMemory(node, 8)
		Expect(k8sClient.Create(ctx, &corev1.Pod{Name: "pinned", Namespace: ns, Spec: corev1.PodSpec{
			NodeName: node, Containers: []corev1.Container{qosContainer(cApp, 100, 1000, 64, 64)},
		}})).To(Succeed())
		createLimitRange("max", corev1.LimitRangeItem{Type: corev1.LimitTypeContainer, Max: cpuList(105)})

		denied := result(resultLimitRangeDenied)
		_, err := r.Reconcile(ctx, reconcile.Request{Name: node})
		Expect(err).NotTo(HaveOccurred())

		Expect(podLimitMilli(ns, "pinned")).To(Equal(int64(105)), "the only value above request and within the max")
		Expect(result(resultLimitRangeDenied)).To(Equal(denied))
		var got corev1.Pod
		Expect(k8sClient.Get(ctx, types.NamespacedName{Namespace: ns, Name: "pinned"}, &got)).To(Succeed())
		Expect(got.Status.QOSClass).To(Equal(corev1.PodQOSBurstable))
	})

	// Created after the pod, a max below its request leaves no legal limit at all:
	// this is the refusal the clamp cannot prevent.
	DescribeTable("names the LimitRange when no limit satisfies it",
		func(container corev1.Container) {
			node := nextNode()
			makeNodeWithMemory(node, 8)
			pod := &corev1.Pod{Name: "late", Namespace: ns, Spec: corev1.PodSpec{NodeName: node, Containers: []corev1.Container{container}}}
			Expect(k8sClient.Create(ctx, pod)).To(Succeed())
			createLimitRange("late", corev1.LimitRangeItem{Type: corev1.LimitTypeContainer, Max: cpuList(50)})

			quota, denied, errs := result(resultQuotaDenied), result(resultLimitRangeDenied), result(resultError)
			_, err := r.Reconcile(ctx, reconcile.Request{Name: node})
			Expect(err).NotTo(HaveOccurred())

			Expect(result(resultLimitRangeDenied)).To(Equal(denied + 1))
			Expect(result(resultQuotaDenied)).To(Equal(quota), "must not be blamed on a ResourceQuota")
			Expect(result(resultError)).To(Equal(errs), "must not surface as a QoS validation error")
			Expect(podLimitMilli(ns, "late")).To(Equal(int64(1000)))
			Expect(r.inBackoff(pod)).To(BeTrue())
			Eventually(rec.Events).Should(Receive(And(
				ContainSubstring(reasonResizeLimitRange),
				ContainSubstring("refused by a LimitRange"),
				ContainSubstring("maximum cpu usage per Container is 50m"),
			)))
		},
		Entry("plain pod, whose target is its request", qosContainer(cApp, 100, 1000, 0, 0)),
		Entry("memory-pinned pod, whose target is the QoS floor", qosContainer(cApp, 100, 1000, 64, 64)),
	)

	It("still names the ResourceQuota for a quota refusal", func() {
		// envtest runs no quota controller, so status carries what it would have written.
		quota := &corev1.ResourceQuota{Name: "cpu-limits", Namespace: ns, Spec: corev1.ResourceQuotaSpec{
			Hard: corev1.ResourceList{corev1.ResourceLimitsCPU: resource.MustParse("1")},
		}}
		node := nextNode()
		makeNode(node, 8)
		makeBurstablePod(ns, "quota-limited", node, 500, 600)
		Expect(k8sClient.Create(ctx, quota)).To(Succeed())
		quota.Status = corev1.ResourceQuotaStatus{
			Hard: quota.Spec.Hard,
			Used: corev1.ResourceList{corev1.ResourceLimitsCPU: resource.MustParse("600m")},
		}
		Expect(k8sClient.Status().Update(ctx, quota)).To(Succeed())

		awaitRefusal("exceeded quota")

		before, denied := result(resultQuotaDenied), result(resultLimitRangeDenied)
		_, err := r.Reconcile(ctx, reconcile.Request{Name: node})
		Expect(err).NotTo(HaveOccurred())

		Expect(result(resultQuotaDenied)).To(Equal(before + 1))
		Expect(result(resultLimitRangeDenied)).To(Equal(denied))
		Eventually(rec.Events).Should(Receive(And(ContainSubstring(reasonResizeForbidden), ContainSubstring("exceeded quota"))))
	})
})

var _ = Describe("NodeReconciler refusal attribution (§6.4)", func() {
	// A cache that has not yet seen a LimitRange leaves the clamp blind, so the patch
	// goes out and the apiserver refuses it: the fake client's empty LimitRange list
	// and intercepted 403 are that race, with the wording the plugins use.
	DescribeTable("names who refused a 403",
		func(message, wantResult, wantReason, wantRefusedBy string) {
			forbidden := apierrors.NewForbidden(schema.GroupResource{Resource: resourcePods}, "refused", errors.New(message))
			r, rec := newResizeErrReconciler("refused", forbidden)

			labels := []string{resultQuotaDenied, resultLimitRangeDenied, resultForbidden}
			counts := map[string]float64{}
			for _, l := range labels {
				counts[l] = testutil.ToFloat64(resizesTotal.WithLabelValues(l))
			}
			_, err := r.Reconcile(ctx, reconcile.Request{Name: errNodeName})
			Expect(err).NotTo(HaveOccurred())

			for _, l := range labels {
				want := counts[l]
				if l == wantResult {
					want++
				}
				Expect(testutil.ToFloat64(resizesTotal.WithLabelValues(l))).To(Equal(want), l)
			}
			Expect(r.inBackoff(&corev1.Pod{Namespace: nsA, Name: "refused"})).To(BeTrue())
			Eventually(rec.Events).Should(Receive(And(
				ContainSubstring(wantReason), ContainSubstring("refused by "+wantRefusedBy), ContainSubstring(message),
			)))
		},
		Entry("LimitRange max", "maximum cpu usage per Container is 500m, but limit is 1",
			resultLimitRangeDenied, reasonResizeLimitRange, "a LimitRange"),
		Entry("LimitRange per-pod max", "maximum cpu usage per Pod is 500m, but limit is 1",
			resultLimitRangeDenied, reasonResizeLimitRange, "a LimitRange"),
		Entry("LimitRange ratio", "cpu max limit to request ratio per Container is 2, but provided ratio is 10.000000",
			resultLimitRangeDenied, reasonResizeLimitRange, "a LimitRange"),
		Entry("quota exceeded", "exceeded quota: cpu-limits, requested: limits.cpu=8, used: limits.cpu=1, limited: limits.cpu=2",
			resultQuotaDenied, reasonResizeForbidden, "a ResourceQuota"),
		Entry("quota status not yet computed", "status unknown for quota: cpu-limits, resources: limits.cpu",
			resultQuotaDenied, reasonResizeForbidden, "a ResourceQuota"),
		Entry("RBAC, in a namespace named for a quota", `User "system:serviceaccount:quota:headroom" cannot patch resource "pods/resize"`,
			resultForbidden, reasonResizeForbidden, "the apiserver"),
		Entry("an admission webhook's denial", `admission webhook "deny.example.com" denied the request`,
			resultForbidden, reasonResizeForbidden, "the apiserver"),
	)

	It("truncates an event note to what events/v1 accepts", func() {
		rec := events.NewFakeRecorder(1)
		r := &NodeReconciler{Recorder: rec}
		r.recordEvent(&corev1.Pod{}, corev1.EventTypeWarning, reasonResizeForbidden, strings.Repeat("x", eventNoteLimit+1))
		Expect(strings.Count(<-rec.Events, "x")).To(Equal(eventNoteLimit))
	})
})
