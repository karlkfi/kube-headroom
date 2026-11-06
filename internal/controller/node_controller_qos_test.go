package controller

import (
	"encoding/json"
	"fmt"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/events"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	kubeheadroomv1alpha1 "github.com/karlkfi/kube-headroom/api/v1alpha1"
)

// podPinned sorts first on its node, ahead of the plain pod the refusal would strand.
const podPinned = "a-pinned"

// qosContainer builds a container for the QoS specs. CPU is in milli-cores, memory
// in megabytes, and zero leaves a value unset.
func qosContainer(name string, cpuReq, cpuLim, memReq, memLim int64) corev1.Container {
	c := corev1.Container{Name: name, Image: imgBusybox, Resources: corev1.ResourceRequirements{
		Requests: corev1.ResourceList{},
		Limits:   corev1.ResourceList{},
	}}
	if cpuReq > 0 {
		c.Resources.Requests[corev1.ResourceCPU] = *resource.NewMilliQuantity(cpuReq, resource.DecimalSI)
	}
	if cpuLim > 0 {
		c.Resources.Limits[corev1.ResourceCPU] = *resource.NewMilliQuantity(cpuLim, resource.DecimalSI)
	}
	if memReq > 0 {
		c.Resources.Requests[corev1.ResourceMemory] = *resource.NewScaledQuantity(memReq, resource.Mega)
	}
	if memLim > 0 {
		c.Resources.Limits[corev1.ResourceMemory] = *resource.NewScaledQuantity(memLim, resource.Mega)
	}
	return c
}

func sidecarOf(c corev1.Container) corev1.Container {
	always := corev1.ContainerRestartPolicyAlways
	c.RestartPolicy = &always
	return c
}

// makeNodeWithMemory is makeNode with memory allocatable too: resize admission
// refuses a pod requesting memory on a node advertising none.
func makeNodeWithMemory(name string, cores int64) {
	makeNode(name, cores)
	var node corev1.Node
	Expect(k8sClient.Get(ctx, types.NamespacedName{Name: name}, &node)).To(Succeed())
	node.Status.Allocatable[corev1.ResourceMemory] = resource.MustParse("8Gi")
	Expect(k8sClient.Status().Update(ctx, &node)).To(Succeed())
}

// resizeToRequest drives the resize subresource to limit == request on every
// container Headroom resizes.
func resizeToRequest(pod *corev1.Pod) error {
	limits := func(cs []corev1.Container, keep func(corev1.Container) bool) []any {
		out := []any{}
		for _, c := range cs {
			if c.Resources.Requests.Cpu().Sign() > 0 && keep(c) {
				out = append(out, map[string]any{"name": c.Name, "resources": map[string]any{
					"limits": map[string]any{"cpu": c.Resources.Requests.Cpu().String()},
				}})
			}
		}
		return out
	}
	data, err := json.Marshal(map[string]any{"spec": map[string]any{
		"containers":     limits(pod.Spec.Containers, func(corev1.Container) bool { return true }),
		"initContainers": limits(pod.Spec.InitContainers, func(c corev1.Container) bool { return c.RestartPolicy != nil }),
	}})
	if err != nil {
		return err
	}
	return k8sClient.SubResource("resize").Patch(ctx, pod.DeepCopy(), client.RawPatch(types.StrategicMergePatchType, data))
}

// These specs need a real apiserver: the "Pod QOS Class may not change" refusal
// is resize validation, which a fake client never runs.
var _ = Describe("NodeReconciler and the QoS-immutable resize rule (§4.1)", func() {
	var r *NodeReconciler

	BeforeEach(func() {
		r = &NodeReconciler{Client: k8sClient, Scheme: k8sClient.Scheme(), Recorder: events.NewFakeRecorder(64)}
	})

	AfterEach(func() {
		_ = k8sClient.Delete(ctx, &kubeheadroomv1alpha1.HeadroomConfig{Name: kubeheadroomv1alpha1.SingletonName})
	})

	var podSeq int

	// guaranteedAtRequest stands in for the apiserver's QoS computation, so each
	// shape drives the real resize to limit == request and demands the two agree.
	DescribeTable("agrees with the apiserver about which pods refuse limit == request",
		func(spec corev1.PodSpec, wantRefused bool) {
			makeManagedNamespace()
			node := nextNode()
			makeNodeWithMemory(node, 8)
			podSeq++
			spec.NodeName = node
			pod := &corev1.Pod{Name: fmt.Sprintf("qos-%d", podSeq), Namespace: nsA, Spec: spec}
			Expect(k8sClient.Create(ctx, pod)).To(Succeed())
			Expect(pod.Status.QOSClass).To(Equal(corev1.PodQOSBurstable), "the shape must start Burstable to be managed")

			Expect(guaranteedAtRequest(pod)).To(Equal(wantRefused))

			err := resizeToRequest(pod)
			if wantRefused {
				Expect(apierrors.IsInvalid(err)).To(BeTrue(), "want a validation refusal, got: %v", err)
				Expect(err.Error()).To(ContainSubstring("Pod QOS Class may not change"))
			} else {
				Expect(err).NotTo(HaveOccurred())
			}
		},
		Entry("memory pinned, CPU limit above request",
			corev1.PodSpec{Containers: []corev1.Container{qosContainer(cApp, 100, 1000, 64, 64)}}, true),
		Entry("memory pinned, no CPU limit yet",
			corev1.PodSpec{Containers: []corev1.Container{qosContainer(cApp, 100, 0, 64, 64)}}, true),
		Entry("memory limit above request",
			corev1.PodSpec{Containers: []corev1.Container{qosContainer(cApp, 100, 1000, 64, 128)}}, false),
		Entry("memory request with no limit",
			corev1.PodSpec{Containers: []corev1.Container{qosContainer(cApp, 100, 1000, 64, 0)}}, false),
		Entry("no memory at all",
			corev1.PodSpec{Containers: []corev1.Container{qosContainer(cApp, 100, 1000, 0, 0)}}, false),
		Entry("two containers, both memory pinned",
			corev1.PodSpec{Containers: []corev1.Container{qosContainer(cApp, 100, 1000, 64, 64), qosContainer("two", 200, 2000, 32, 32)}}, true),
		Entry("two containers, one memory unpinned",
			corev1.PodSpec{Containers: []corev1.Container{qosContainer(cApp, 100, 1000, 64, 64), qosContainer("two", 200, 2000, 32, 64)}}, false),
		Entry("second container with memory pinned and no CPU, so it never gets a limit",
			corev1.PodSpec{Containers: []corev1.Container{qosContainer(cApp, 100, 1000, 64, 64), qosContainer("two", 0, 0, 32, 32)}}, false),
		Entry("sidecar memory pinned",
			corev1.PodSpec{
				Containers:     []corev1.Container{qosContainer(cApp, 100, 1000, 64, 64)},
				InitContainers: []corev1.Container{sidecarOf(qosContainer("side", 50, 500, 32, 32))},
			}, true),
		Entry("sidecar memory unpinned",
			corev1.PodSpec{
				Containers:     []corev1.Container{qosContainer(cApp, 100, 1000, 64, 64)},
				InitContainers: []corev1.Container{sidecarOf(qosContainer("side", 50, 500, 32, 64))},
			}, false),
		Entry("init container already at limit == request",
			corev1.PodSpec{
				Containers:     []corev1.Container{qosContainer(cApp, 100, 1000, 64, 64)},
				InitContainers: []corev1.Container{qosContainer("init", 50, 50, 32, 32)},
			}, true),
		Entry("init container CPU limit above request, which Headroom leaves alone",
			corev1.PodSpec{
				Containers:     []corev1.Container{qosContainer(cApp, 100, 1000, 64, 64)},
				InitContainers: []corev1.Container{qosContainer("init", 50, 500, 32, 32)},
			}, false),
		Entry("init container with no limits",
			corev1.PodSpec{
				Containers:     []corev1.Container{qosContainer(cApp, 100, 1000, 64, 64)},
				InitContainers: []corev1.Container{qosContainer("init", 50, 0, 32, 0)},
			}, false),
		Entry("pod-level resources set the class",
			corev1.PodSpec{
				Containers: []corev1.Container{qosContainer(cApp, 100, 1000, 64, 64)},
				Resources: &corev1.ResourceRequirements{
					Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("100m"), corev1.ResourceMemory: resource.MustParse("64M")},
					Limits:   corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("1"), corev1.ResourceMemory: resource.MustParse("128M")},
				},
			}, false),
		Entry("empty pod-level resources leave the class to the containers",
			corev1.PodSpec{
				Containers: []corev1.Container{qosContainer(cApp, 100, 1000, 64, 64)},
				Resources:  &corev1.ResourceRequirements{},
			}, true),
	)

	It("shrinks memory-pinned and plain pods on a node with no slack, without churn", func() {
		applyConfigWith(false, func(spec *kubeheadroomv1alpha1.HeadroomConfigSpec) {
			spec.Quantum = resource.MustParse("10m")
		})
		makeManagedNamespace()
		node := nextNode()
		makeNodeWithMemory(node, 2)

		pinned := &corev1.Pod{Name: podPinned, Namespace: nsA, Spec: corev1.PodSpec{
			NodeName: node, Containers: []corev1.Container{qosContainer(cApp, 100, 1000, 64, 64)},
		}}
		Expect(k8sClient.Create(ctx, pinned)).To(Succeed())
		makeBurstablePod(nsA, "z-plain", node, 100, 1000)
		// A Guaranteed neighbour books the rest of the node and is never managed.
		filler := &corev1.Pod{Name: "m-filler", Namespace: nsA, Spec: corev1.PodSpec{
			NodeName: node, Containers: []corev1.Container{qosContainer(cApp, 1800, 1800, 64, 64)},
		}}
		Expect(k8sClient.Create(ctx, filler)).To(Succeed())

		req := reconcile.Request{Name: node}
		_, err := r.Reconcile(ctx, req)
		Expect(err).NotTo(HaveOccurred())

		Expect(podLimitMilli(nsA, podPinned)).To(Equal(int64(110)), "one quantum above request")
		Expect(podLimitMilli(nsA, "z-plain")).To(Equal(int64(100)), "limit == request keeps a pod with no memory limit Burstable")

		var got corev1.Pod
		Expect(k8sClient.Get(ctx, types.NamespacedName{Namespace: nsA, Name: podPinned}, &got)).To(Succeed())
		Expect(got.Status.QOSClass).To(Equal(corev1.PodQOSBurstable))

		By("a second pass finds nothing to change")
		generation := got.Generation
		_, err = r.Reconcile(ctx, req)
		Expect(err).NotTo(HaveOccurred())
		Expect(k8sClient.Get(ctx, types.NamespacedName{Namespace: nsA, Name: podPinned}, &got)).To(Succeed())
		Expect(got.Generation).To(Equal(generation))
	})
})
