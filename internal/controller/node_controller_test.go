package controller

import (
	"encoding/json"
	"fmt"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/prometheus/client_golang/prometheus/testutil"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	kubeheadroomv1alpha1 "github.com/karlkfi/kube-headroom/api/v1alpha1"
)

// These specs drive the NodeReconciler against a real apiserver (envtest). There
// is no kubelet, so the apiserver accepts a resize patch and reflects it in
// spec.containers[].resources — enough to assert the controller's write path,
// deadband, and dry-run behavior. End-to-end actuation is Q8 (kind ≥1.35).

var nodeSeq int

func nextNode() string {
	nodeSeq++
	return fmt.Sprintf("node-%d", nodeSeq)
}

// makeNode creates a node advertising the given allocatable CPU (cores).
func makeNode(name string, cores int64) {
	node := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: name}}
	Expect(k8sClient.Create(ctx, node)).To(Succeed())
	// Allocatable lives in status, which is dropped on create; set it explicitly.
	node.Status.Allocatable = corev1.ResourceList{corev1.ResourceCPU: *resource.NewQuantity(cores, resource.DecimalSI)}
	Expect(k8sClient.Status().Update(ctx, node)).To(Succeed())
}

// makeWindowsNode creates a Windows node (in-place resize unsupported, §8.4).
func makeWindowsNode(name string, cores int64) {
	node := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: name}}
	Expect(k8sClient.Create(ctx, node)).To(Succeed())
	node.Status.Allocatable = corev1.ResourceList{corev1.ResourceCPU: *resource.NewQuantity(cores, resource.DecimalSI)}
	node.Status.NodeInfo.OperatingSystem = osWindows
	Expect(k8sClient.Status().Update(ctx, node)).To(Succeed())
}

// makeManagedNamespace ensures the nsA namespace exists, opted in via the
// mode=managed label.
func makeManagedNamespace() {
	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{
		Name:   nsA,
		Labels: map[string]string{kubeheadroomv1alpha1.LabelMode: kubeheadroomv1alpha1.ModeManaged},
	}}
	err := k8sClient.Create(ctx, ns)
	if err != nil && !apierrors.IsAlreadyExists(err) {
		Expect(err).NotTo(HaveOccurred())
	}
}

// makeBurstablePod creates a Burstable pod bound to a node with a single app
// container carrying the given CPU request and optional limit (0 = none).
func makeBurstablePod(ns, name, node string, reqMilli, limMilli int64) {
	c := corev1.Container{
		Name:  cApp,
		Image: "busybox:1.36",
		Resources: corev1.ResourceRequirements{
			Requests: corev1.ResourceList{corev1.ResourceCPU: *resource.NewMilliQuantity(reqMilli, resource.DecimalSI)},
		},
	}
	if limMilli > 0 {
		c.Resources.Limits = corev1.ResourceList{corev1.ResourceCPU: *resource.NewMilliQuantity(limMilli, resource.DecimalSI)}
	}
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
		Spec:       corev1.PodSpec{Containers: []corev1.Container{c}, NodeName: node},
	}
	Expect(k8sClient.Create(ctx, pod)).To(Succeed())
}

// makeOwnedBurstablePod is makeBurstablePod plus a single ownerReference, used to
// exercise the ExcludedOwners gate (§6.3).
func makeOwnedBurstablePod(ns, name, node string, reqMilli int64, owner metav1.OwnerReference) {
	c := corev1.Container{
		Name:  cApp,
		Image: "busybox:1.36",
		Resources: corev1.ResourceRequirements{
			Requests: corev1.ResourceList{corev1.ResourceCPU: *resource.NewMilliQuantity(reqMilli, resource.DecimalSI)},
		},
	}
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns, OwnerReferences: []metav1.OwnerReference{owner}},
		Spec:       corev1.PodSpec{Containers: []corev1.Container{c}, NodeName: node},
	}
	Expect(k8sClient.Create(ctx, pod)).To(Succeed())
}

func podLimitMilli(ns, name string) int64 {
	var pod corev1.Pod
	Expect(k8sClient.Get(ctx, types.NamespacedName{Namespace: ns, Name: name}, &pod)).To(Succeed())
	return pod.Spec.Containers[0].Resources.Limits.Cpu().MilliValue()
}

// applyConfig upserts the singleton HeadroomConfig with the given dryRun value.
func applyConfig(dryRun bool) {
	applyConfigWith(dryRun, nil)
}

// applyConfigWith upserts the singleton with dryRun plus an optional extra
// mutation of the spec (e.g. ExcludedOwners, ExcludedNodeSelector).
func applyConfigWith(dryRun bool, extra func(*kubeheadroomv1alpha1.HeadroomConfigSpec)) {
	dr := dryRun
	hc := &kubeheadroomv1alpha1.HeadroomConfig{ObjectMeta: metav1.ObjectMeta{Name: kubeheadroomv1alpha1.SingletonName}}
	_, err := controllerutilCreateOrPatch(hc, func() {
		hc.Spec.DryRun = &dr
		if extra != nil {
			extra(&hc.Spec)
		}
	})
	Expect(err).NotTo(HaveOccurred())
}

// controllerutilCreateOrPatch is a tiny inline upsert to avoid pulling in extra
// deps; it creates or updates the HeadroomConfig singleton.
func controllerutilCreateOrPatch(hc *kubeheadroomv1alpha1.HeadroomConfig, mutate func()) (bool, error) {
	var existing kubeheadroomv1alpha1.HeadroomConfig
	err := k8sClient.Get(ctx, types.NamespacedName{Name: hc.Name}, &existing)
	if apierrors.IsNotFound(err) {
		mutate()
		return true, k8sClient.Create(ctx, hc)
	}
	if err != nil {
		return false, err
	}
	hc.ObjectMeta = existing.ObjectMeta
	hc.Spec = existing.Spec
	mutate()
	return false, k8sClient.Update(ctx, hc)
}

var _ = Describe("NodeReconciler", func() {
	var r *NodeReconciler

	reconcileNode := func(node string) reconcile.Result {
		res, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: types.NamespacedName{Name: node}})
		Expect(err).NotTo(HaveOccurred())
		return res
	}

	// podStatusAnnotation reads and parses the kube-headroom.dev/status annotation.
	podStatusAnnotation := func(ns, name string) map[string]any {
		var pod corev1.Pod
		Expect(k8sClient.Get(ctx, types.NamespacedName{Namespace: ns, Name: name}, &pod)).To(Succeed())
		raw := pod.Annotations[kubeheadroomv1alpha1.AnnotationStatus]
		if raw == "" {
			return nil
		}
		var st map[string]any
		Expect(json.Unmarshal([]byte(raw), &st)).To(Succeed())
		return st
	}

	BeforeEach(func() {
		// A buffered fake recorder so specs can drain events without blocking the
		// reconcile; specs that assert events swap in their own.
		r = &NodeReconciler{Client: k8sClient, Scheme: k8sClient.Scheme(), Recorder: record.NewFakeRecorder(64)}
	})

	AfterEach(func() {
		// Remove the config so each spec chooses its own dry-run posture.
		hc := &kubeheadroomv1alpha1.HeadroomConfig{ObjectMeta: metav1.ObjectMeta{Name: kubeheadroomv1alpha1.SingletonName}}
		_ = k8sClient.Delete(ctx, hc)
	})

	It("sets a limit on a managed pod alone on a node (not dry-run)", func() {
		applyConfig(false)
		makeManagedNamespace()
		node := nextNode()
		makeNode(node, 8) // 8000m allocatable
		makeBurstablePod(nsA, "solo", node, 1000, 0)

		reconcileNode(node)

		// Alone on an 8-core node: slack ≈ 7000m, factor large, capped by
		// maxMultiplier 10 → 1000m × 10 = 10000m, then clamped to allocatable 8000m.
		Eventually(func() int64 { return podLimitMilli(nsA, "solo") }).Should(Equal(int64(8000)))
	})

	It("does not patch in dry-run mode", func() {
		applyConfig(true)
		makeManagedNamespace()
		node := nextNode()
		makeNode(node, 4) // smaller node; assertion is independent of size
		makeBurstablePod(nsA, "dry", node, 1000, 0)

		reconcileNode(node)

		Consistently(func() int64 { return podLimitMilli(nsA, "dry") }).Should(Equal(int64(0)))
	})

	It("does not manage pods in a non-opted-in namespace", func() {
		applyConfig(false)
		ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "team-plain"}}
		Expect(k8sClient.Create(ctx, ns)).To(Succeed())
		node := nextNode()
		makeNode(node, 8)
		makeBurstablePod("team-plain", "unmanaged", node, 1000, 0)

		reconcileNode(node)

		Consistently(func() int64 { return podLimitMilli("team-plain", "unmanaged") }).Should(Equal(int64(0)))
	})

	It("does not manage pods on a Windows node", func() {
		applyConfig(false)
		makeManagedNamespace()
		node := nextNode()
		makeWindowsNode(node, 8)
		makeBurstablePod(nsA, "winpod", node, 1000, 0)

		reconcileNode(node)

		Consistently(func() int64 { return podLimitMilli(nsA, "winpod") }).Should(Equal(int64(0)))
	})

	It("does not manage pods on a node matching ExcludedNodeSelector", func() {
		applyConfigWith(false, func(s *kubeheadroomv1alpha1.HeadroomConfigSpec) {
			s.ExcludedNodeSelector = &metav1.LabelSelector{MatchLabels: map[string]string{nodeExcludedLabel: labelTrue}}
		})
		makeManagedNamespace()
		node := nextNode()
		makeNode(node, 8)
		// Label the node so the selector excludes it.
		var n corev1.Node
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: node}, &n)).To(Succeed())
		n.Labels = map[string]string{nodeExcludedLabel: labelTrue}
		Expect(k8sClient.Update(ctx, &n)).To(Succeed())
		makeBurstablePod(nsA, "numapod", node, 1000, 0)

		reconcileNode(node)

		Consistently(func() int64 { return podLimitMilli(nsA, "numapod") }).Should(Equal(int64(0)))
	})

	It("does not manage a pod owned by an excluded owner", func() {
		applyConfigWith(false, func(s *kubeheadroomv1alpha1.HeadroomConfigSpec) {
			s.ExcludedOwners = []kubeheadroomv1alpha1.ExcludedOwner{{Kind: kindDaemonSet, APIGroup: groupApps}}
		})
		makeManagedNamespace()
		node := nextNode()
		makeNode(node, 8)
		makeOwnedBurstablePod(nsA, "dspod", node, 1000, metav1.OwnerReference{
			APIVersion: "apps/v1", Kind: kindDaemonSet, Name: "fluentd", UID: "uid-ds",
		})

		reconcileNode(node)

		Consistently(func() int64 { return podLimitMilli(nsA, "dspod") }).Should(Equal(int64(0)))
	})

	It("shrinks a limit when a neighbor books the node's slack", func() {
		applyConfig(false)
		makeManagedNamespace()
		node := nextNode()
		makeNode(node, 8)
		makeBurstablePod(nsA, "incumbent", node, 1000, 0)

		reconcileNode(node)
		Eventually(func() int64 { return podLimitMilli(nsA, "incumbent") }).Should(Equal(int64(8000)))

		// A 6-core neighbor lands: slack drops to ~1000m, incumbent's ceiling falls.
		makeBurstablePod(nsA, "neighbor", node, 6000, 0)
		reconcileNode(node)

		Eventually(func() int64 { return podLimitMilli(nsA, "incumbent") }).Should(And(
			BeNumerically("<", 8000), BeNumerically(">=", 1000)))
	})

	It("issues no patch when a pod is born already at its target (deadband)", func() {
		applyConfig(false)
		makeManagedNamespace()
		node := nextNode()
		makeNode(node, 8)
		// Born at 8000m — exactly the computed target for a solo 1000m-request pod,
		// so the deadband suppresses any resize.
		makeBurstablePod(nsA, "atrest", node, 1000, 8000)

		// First reconcile still writes the status annotation once (§8.1); capture
		// the resource version after it settles.
		reconcileNode(node)

		var before corev1.Pod
		Expect(k8sClient.Get(ctx, types.NamespacedName{Namespace: nsA, Name: "atrest"}, &before)).To(Succeed())
		rv := before.ResourceVersion

		reconcileNode(node)
		reconcileNode(node)

		var after corev1.Pod
		Expect(k8sClient.Get(ctx, types.NamespacedName{Namespace: nsA, Name: "atrest"}, &after)).To(Succeed())
		Expect(after.ResourceVersion).To(Equal(rv), "at-target reconcile should issue no further patch")
		Expect(after.Spec.Containers[0].Resources.Limits.Cpu().MilliValue()).To(Equal(int64(8000)),
			"limit must be untouched by the deadband")
	})

	It("issues zero patches at steady state after applying (deadband holds)", func() {
		applyConfig(false)
		makeManagedNamespace()
		node := nextNode()
		makeNode(node, 8)
		makeBurstablePod(nsA, "steady", node, 1000, 0)

		reconcileNode(node)
		Eventually(func() int64 { return podLimitMilli(nsA, "steady") }).Should(Equal(int64(8000)))

		var before corev1.Pod
		Expect(k8sClient.Get(ctx, types.NamespacedName{Namespace: nsA, Name: "steady"}, &before)).To(Succeed())
		rv := before.ResourceVersion

		// Re-reconciling an unchanged node must not write (target already applied).
		reconcileNode(node)
		reconcileNode(node)

		var after corev1.Pod
		Expect(k8sClient.Get(ctx, types.NamespacedName{Namespace: nsA, Name: "steady"}, &after)).To(Succeed())
		Expect(after.ResourceVersion).To(Equal(rv), "steady-state reconcile should issue no patch")
	})

	It("annotates a managed pod with its computed status (§8.1)", func() {
		applyConfig(false)
		makeManagedNamespace()
		node := nextNode()
		makeNode(node, 8)
		makeBurstablePod(nsA, "annotated", node, 1000, 0)

		reconcileNode(node)
		Eventually(func() int64 { return podLimitMilli(nsA, "annotated") }).Should(Equal(int64(8000)))

		st := podStatusAnnotation(nsA, "annotated")
		Expect(st).NotTo(BeNil())
		Expect(st["policy"]).To(Equal("proportional-v1"))
		Expect(st["targetLimit"]).To(Equal("8000m"))
		Expect(st).To(HaveKeyWithValue("nodePods", BeNumerically("==", 1)))
		Expect(st).To(HaveKey("factor"))
		Expect(st).To(HaveKey("slack"))
		Expect(st).To(HaveKey("managedRequests"))
		Expect(st).To(HaveKey("computedAt"))
		// dryRun omitempty: absent (not false) in live mode.
		Expect(st).NotTo(HaveKey("dryRun"))
	})

	It("annotates in dry-run without resizing, marking dryRun (§9.3)", func() {
		applyConfig(true)
		makeManagedNamespace()
		node := nextNode()
		makeNode(node, 8)
		makeBurstablePod(nsA, "dryannot", node, 1000, 0)

		reconcileNode(node)

		Eventually(func() map[string]any { return podStatusAnnotation(nsA, "dryannot") }).ShouldNot(BeNil())
		st := podStatusAnnotation(nsA, "dryannot")
		Expect(st).To(HaveKeyWithValue("dryRun", true))
		Expect(st["targetLimit"]).To(Equal("8000m"))
		// Annotated, but the limit itself was never patched.
		Expect(podLimitMilli(nsA, "dryannot")).To(Equal(int64(0)))
	})

	It("emits a CPULimitAdjusted event on resize (§8.1)", func() {
		rec := record.NewFakeRecorder(16)
		r.Recorder = rec
		applyConfig(false)
		makeManagedNamespace()
		node := nextNode()
		makeNode(node, 8)
		makeBurstablePod(nsA, "evented", node, 1000, 0)

		reconcileNode(node)
		Eventually(func() int64 { return podLimitMilli(nsA, "evented") }).Should(Equal(int64(8000)))

		Eventually(rec.Events).Should(Receive(And(
			ContainSubstring(reasonCPULimitAdjusted),
			ContainSubstring("→ 8000m"),
		)))
	})

	It("records node gauges and the applied resize counter (§8.1)", func() {
		applyConfig(false)
		makeManagedNamespace()
		node := nextNode()
		makeNode(node, 8)
		makeBurstablePod(nsA, "metered", node, 1000, 0)

		before := testutil.ToFloat64(resizesTotal.WithLabelValues(resultApplied))
		reconcileNode(node)
		Eventually(func() int64 { return podLimitMilli(nsA, "metered") }).Should(Equal(int64(8000)))

		// Slack = 8 cores allocatable − 1 core requested = 7 cores; factor > 1.
		Expect(testutil.ToFloat64(nodeSlackCores.WithLabelValues(node))).To(BeNumerically("~", 7.0, 0.001))
		Expect(testutil.ToFloat64(nodeFactor.WithLabelValues(node))).To(BeNumerically(">", 1.0))
		Expect(testutil.ToFloat64(nodeManagedPods.WithLabelValues(node))).To(BeNumerically("==", 1))
		Expect(testutil.ToFloat64(resizesTotal.WithLabelValues(resultApplied))).To(BeNumerically(">", before))
	})

	It("meters dry-run decisions under result=dry-run, not applied (§9.3)", func() {
		applyConfig(true)
		makeManagedNamespace()
		node := nextNode()
		makeNode(node, 8)
		makeBurstablePod(nsA, "drymeter", node, 1000, 0)

		beforeDry := testutil.ToFloat64(resizesTotal.WithLabelValues(resultDryRun))
		beforeApplied := testutil.ToFloat64(resizesTotal.WithLabelValues(resultApplied))
		reconcileNode(node)

		Expect(testutil.ToFloat64(resizesTotal.WithLabelValues(resultDryRun))).To(BeNumerically(">", beforeDry))
		Expect(testutil.ToFloat64(resizesTotal.WithLabelValues(resultApplied))).To(Equal(beforeApplied))
	})
})
