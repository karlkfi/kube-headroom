package controller

import (
	"slices"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"

	kubeheadroomv1alpha1 "github.com/karlkfi/kube-headroom/api/v1alpha1"
	"github.com/karlkfi/kube-headroom/internal/policy"
)

// resizableContainer is one container Headroom may resize on a pod: an app
// container or a restartable-init (sidecar) container. Regular init containers
// are excluded — they have run to completion by the time a pod is manageable,
// and the resize subresource does not touch them.
type resizableContainer struct {
	Name            string
	RequestMilli    int64
	LimitMilli      int64 // 0 = no CPU limit set
	RestartOnResize bool  // resizePolicy for CPU is RestartContainer (§9.4.2 — never resize)
}

// resizableContainers returns the app + sidecar containers of a pod together
// with their CPU request, current CPU limit, and resize policy. It is the single
// place that defines which containers Headroom's per-container math ranges over.
func resizableContainers(pod *corev1.Pod) []resizableContainer {
	out := make([]resizableContainer, 0, len(pod.Spec.Containers)+len(pod.Spec.InitContainers))
	add := func(c *corev1.Container) {
		out = append(out, resizableContainer{
			Name:            c.Name,
			RequestMilli:    c.Resources.Requests.Cpu().MilliValue(),
			LimitMilli:      c.Resources.Limits.Cpu().MilliValue(),
			RestartOnResize: cpuRestartOnResize(c),
		})
	}
	for i := range pod.Spec.Containers {
		add(&pod.Spec.Containers[i])
	}
	// Restartable-init (native sidecar) containers run for the pod's lifetime and
	// carry a CPU request/limit like app containers, so they participate.
	for i := range pod.Spec.InitContainers {
		c := &pod.Spec.InitContainers[i]
		if c.RestartPolicy != nil && *c.RestartPolicy == corev1.ContainerRestartPolicyAlways {
			add(c)
		}
	}
	return out
}

// cpuRestartOnResize reports whether the container's resizePolicy for CPU is
// RestartContainer. Such a container must never be resized (§9.4.2): a limit
// change would restart the workload. Absent policy defaults to NotRequired.
func cpuRestartOnResize(c *corev1.Container) bool {
	for _, p := range c.ResizePolicy {
		if p.ResourceName == corev1.ResourceCPU {
			return p.RestartPolicy == corev1.RestartContainer
		}
	}
	return false
}

// podTerminal reports whether the pod has reached a terminal phase and therefore
// no longer consumes node CPU for slack accounting.
func podTerminal(pod *corev1.Pod) bool {
	return pod.Status.Phase == corev1.PodSucceeded || pod.Status.Phase == corev1.PodFailed
}

// podCPURequestMilli is the pod's aggregate CPU request over the containers
// Headroom ranges over (§5.4 distribution basis for managed pods; slack basis
// for all pods).
func podCPURequestMilli(rcs []resizableContainer) int64 {
	var sum int64
	for _, c := range rcs {
		sum += c.RequestMilli
	}
	return sum
}

// podCurrentLimitMilli is the pod's aggregate enforced CPU limit, or 0 when the
// pod is not fully bounded. A pod is treated as "unset" (0) unless *every*
// resizable container already carries a CPU limit — a partially-limited pod is
// effectively unbounded and must have a limit set (§6.2, policy CurrentLimit=0).
func podCurrentLimitMilli(rcs []resizableContainer) int64 {
	var sum int64
	for _, c := range rcs {
		if c.LimitMilli <= 0 {
			return 0
		}
		sum += c.LimitMilli
	}
	return sum
}

// eligible applies the pod-local eligibility gates Headroom needs before it may
// manage a pod (§6.3). It is intentionally the safe subset: namespace opt-in is
// resolved separately by the reconciler, and the fuller gate set (exclusion
// lists, LimitRange awareness, dry-run metering) lands with Q5. The resizePolicy
// gate is a safety invariant (§9.4.2) and lives here permanently.
func eligible(pod *corev1.Pod, rcs []resizableContainer) bool {
	if podTerminal(pod) {
		return false
	}
	// Explicit per-pod opt-out overrides namespace enrollment (§6.3).
	if pod.Labels[kubeheadroomv1alpha1.LabelMode] == kubeheadroomv1alpha1.ModeUnmanaged {
		return false
	}
	// Only Burstable pods can gain a floating CPU limit without changing QoS:
	// Guaranteed requires limit==request, BestEffort has no request to burst from
	// and cannot have a limit added via resize (spike Q2b).
	if pod.Status.QOSClass != corev1.PodQOSBurstable {
		return false
	}
	if podCPURequestMilli(rcs) <= 0 {
		return false
	}
	// A container that would restart on resize disqualifies the whole pod.
	for _, c := range rcs {
		if c.RestartOnResize {
			return false
		}
	}
	return true
}

// namespaceManaged resolves namespace-level opt-in (§6.3): excluded namespaces
// are never managed; otherwise the configured NamespaceSelector decides, and
// when unset the default is the label kube-headroom.dev/mode=managed.
func namespaceManaged(ns *corev1.Namespace, spec *kubeheadroomv1alpha1.HeadroomConfigSpec) bool {
	if slices.Contains(spec.ExcludedNamespaces, ns.Name) {
		return false
	}
	if spec.NamespaceSelector != nil {
		sel, err := metav1.LabelSelectorAsSelector(spec.NamespaceSelector)
		if err != nil {
			return false
		}
		return sel.Matches(labels.Set(ns.Labels))
	}
	return ns.Labels[kubeheadroomv1alpha1.LabelMode] == kubeheadroomv1alpha1.ModeManaged
}

// userCapMilli reads the optional per-pod ceiling annotation (§5.3); 0 = none.
func userCapMilli(pod *corev1.Pod) int64 {
	v, ok := pod.Annotations[kubeheadroomv1alpha1.AnnotationMaxCPU]
	if !ok {
		return 0
	}
	q, err := resource.ParseQuantity(v)
	if err != nil || q.MilliValue() <= 0 {
		return 0
	}
	return q.MilliValue()
}

// buildPodInput converts a single pod (with its namespace-managed verdict and
// backoff state already resolved) into the policy's reduced PodInput.
func buildPodInput(pod *corev1.Pod, rcs []resizableContainer, managed bool) policy.PodInput {
	return policy.PodInput{
		Key:               pod.Namespace + "/" + pod.Name,
		RequestMilli:      podCPURequestMilli(rcs),
		CurrentLimitMilli: podCurrentLimitMilli(rcs),
		Managed:           managed,
		UserCapMilli:      userCapMilli(pod),
	}
}

// splitLimit distributes a pod-level target CPU limit across its resizable
// containers pro-rata by CPU request, in whole milli-cores, so the per-container
// limits sum exactly to the pod target and each stays at or above its own
// request. Containers are returned in input order; the largest fractional
// remainders receive the leftover milli-cores.
func splitLimit(targetMilli int64, rcs []resizableContainer) map[string]int64 {
	out := make(map[string]int64, len(rcs))
	totalReq := podCPURequestMilli(rcs)
	if totalReq <= 0 || len(rcs) == 0 {
		return out
	}

	type rem struct {
		name string
		frac float64
		idx  int
	}
	var assigned int64
	rems := make([]rem, 0, len(rcs))
	for i, c := range rcs {
		exact := float64(targetMilli) * float64(c.RequestMilli) / float64(totalReq)
		base := max(int64(exact), c.RequestMilli) // floor, never below own request
		out[c.Name] = base
		assigned += base
		rems = append(rems, rem{name: c.Name, frac: exact - float64(int64(exact)), idx: i})
	}

	// Distribute any leftover (from flooring) to the largest fractional parts.
	leftover := targetMilli - assigned
	if leftover <= 0 {
		return out
	}
	slices.SortStableFunc(rems, func(a, b rem) int {
		if a.frac != b.frac {
			if a.frac > b.frac {
				return -1
			}
			return 1
		}
		return a.idx - b.idx
	})
	for i := range int(leftover) {
		out[rems[i%len(rems)].name]++
	}
	return out
}
