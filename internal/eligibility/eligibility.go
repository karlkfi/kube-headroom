// Package eligibility holds the pure, side-effect-free rules that decide which
// pods Headroom may manage and how their CPU request/limit is enumerated (design
// doc §6.3, §9.1). It depends only on core Kubernetes API types and the project's
// label/annotation constants, so both the node reconciler (internal/controller)
// and the birth-limit mutating webhook (internal/webhook) share one definition of
// "manageable" instead of reimplementing the gates.
package eligibility

import (
	"slices"
	"strings"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"

	kubeheadroomv1alpha1 "github.com/karlkfi/kube-headroom/api/v1alpha1"
)

// osWindows is the node OperatingSystem value for Windows nodes, which cannot
// perform in-place CPU resize (§8.4) and are therefore excluded structurally.
const osWindows = "windows"

// ResizableContainer is one container Headroom may resize on a pod: an app
// container or a restartable-init (sidecar) container. Regular init containers
// are excluded — they have run to completion by the time a pod is manageable,
// and the resize subresource does not touch them.
type ResizableContainer struct {
	Name            string
	RequestMilli    int64
	LimitMilli      int64 // 0 = no CPU limit set
	RestartOnResize bool  // resizePolicy for CPU is RestartContainer (§9.4.2 — never resize)
}

// ResizableContainers returns the app + sidecar containers of a pod together
// with their CPU request, current CPU limit, and resize policy. It is the single
// place that defines which containers Headroom's per-container math ranges over.
func ResizableContainers(pod *corev1.Pod) []ResizableContainer {
	out := make([]ResizableContainer, 0, len(pod.Spec.Containers)+len(pod.Spec.InitContainers))
	add := func(c *corev1.Container) {
		out = append(out, ResizableContainer{
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

// PodTerminal reports whether the pod has reached a terminal phase and therefore
// no longer consumes node CPU for slack accounting.
func PodTerminal(pod *corev1.Pod) bool {
	return pod.Status.Phase == corev1.PodSucceeded || pod.Status.Phase == corev1.PodFailed
}

// PodCPURequestMilli is the pod's aggregate CPU request over the containers
// Headroom ranges over (§5.4 distribution basis for managed pods; slack basis
// for all pods).
func PodCPURequestMilli(rcs []ResizableContainer) int64 {
	var sum int64
	for _, c := range rcs {
		sum += c.RequestMilli
	}
	return sum
}

// PodCurrentLimitMilli is the pod's aggregate enforced CPU limit, or 0 when the
// pod is not fully bounded. A pod is treated as "unset" (0) unless *every*
// resizable container already carries a CPU limit — a partially-limited pod is
// effectively unbounded and must have a limit set (§6.2, policy CurrentLimit=0).
func PodCurrentLimitMilli(rcs []ResizableContainer) int64 {
	var sum int64
	for _, c := range rcs {
		if c.LimitMilli <= 0 {
			return 0
		}
		sum += c.LimitMilli
	}
	return sum
}

// OptedOut reports whether the pod explicitly opts out of management with the
// kube-headroom.dev/mode: unmanaged label, overriding its namespace enrollment
// (§6.3). Shared by the reconciler's Eligible gate and the webhook.
func OptedOut(pod *corev1.Pod) bool {
	return pod.Labels[kubeheadroomv1alpha1.LabelMode] == kubeheadroomv1alpha1.ModeUnmanaged
}

// Eligible applies the pod-local eligibility gates Headroom needs before it may
// manage a pod (§6.3): non-terminal, not opted out, Burstable QoS, a positive
// CPU request, and no container that would restart on resize (§9.4.2). The
// remaining §6.3 gates need cluster context and live in the reconciler:
// namespace opt-in (NamespaceManaged), owner exclusion (OwnerExcluded), and node
// exclusion (NodeManageable). LimitRange awareness is deferred to Phase 2.
//
// This gate keys off pod.Status.QOSClass, which the apiserver populates after
// admission — so it is the controller's post-bind gate. The webhook, which runs
// before QoS is set, applies its own create-time gates (internal/webhook).
func Eligible(pod *corev1.Pod, rcs []ResizableContainer) bool {
	if PodTerminal(pod) {
		return false
	}
	// Explicit per-pod opt-out overrides namespace enrollment (§6.3).
	if OptedOut(pod) {
		return false
	}
	// Only Burstable pods can gain a floating CPU limit without changing QoS:
	// Guaranteed requires limit==request, BestEffort has no request to burst from
	// and cannot have a limit added via resize (spike Q2b).
	if pod.Status.QOSClass != corev1.PodQOSBurstable {
		return false
	}
	if PodCPURequestMilli(rcs) <= 0 {
		return false
	}
	// A container that would restart on resize disqualifies the whole pod.
	return !HasRestartOnResize(rcs)
}

// HasRestartOnResize reports whether any resizable container would restart on a
// CPU resize (§9.4.2), which disqualifies the whole pod from management.
func HasRestartOnResize(rcs []ResizableContainer) bool {
	for _, c := range rcs {
		if c.RestartOnResize {
			return true
		}
	}
	return false
}

// NamespaceManaged resolves namespace-level opt-in (§6.3): excluded namespaces
// are never managed; otherwise the configured NamespaceSelector decides, and
// when unset the default is the label kube-headroom.dev/mode=managed.
func NamespaceManaged(ns *corev1.Namespace, spec *kubeheadroomv1alpha1.HeadroomConfigSpec) bool {
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

// OwnerExcluded reports whether the pod is owned by anything in the operator's
// exclusion list (§6.3). A pod matches when one of its ownerReferences shares an
// entry's Kind and — when the entry constrains them — its APIGroup and Name. The
// owner's APIVersion (e.g. "apps/v1") is reduced to its group ("apps") for the
// APIGroup comparison; core-group owners carry an empty group.
func OwnerExcluded(pod *corev1.Pod, excluded []kubeheadroomv1alpha1.ExcludedOwner) bool {
	for _, ref := range pod.OwnerReferences {
		group, _, _ := strings.Cut(ref.APIVersion, "/") // "apps/v1" -> "apps"; "v1" -> ""
		for _, e := range excluded {
			if e.Kind != ref.Kind {
				continue
			}
			if e.APIGroup != "" && e.APIGroup != group {
				continue
			}
			if e.Name != "" && e.Name != ref.Name {
				continue
			}
			return true
		}
	}
	return false
}

// NodeManageable reports whether Headroom may manage pods bound to the node
// (§6.3). Windows nodes cannot do in-place CPU resize and are excluded
// structurally; static CPU/Memory Manager and NUMA-pinned nodes, where resize is
// prohibited, are opt-out via the operator's ExcludedNodeSelector (with the §6.4
// Infeasible back-off as the defensive fallback when a node is not pre-labeled).
func NodeManageable(node *corev1.Node, spec *kubeheadroomv1alpha1.HeadroomConfigSpec) bool {
	if node.Status.NodeInfo.OperatingSystem == osWindows || node.Labels[corev1.LabelOSStable] == osWindows {
		return false
	}
	if spec.ExcludedNodeSelector != nil {
		sel, err := metav1.LabelSelectorAsSelector(spec.ExcludedNodeSelector)
		if err != nil {
			return false // a malformed selector is fail-closed: manage nothing on any node
		}
		if sel.Matches(labels.Set(node.Labels)) {
			return false
		}
	}
	return true
}

// UserCapMilli reads the optional per-pod ceiling annotation (§5.3); 0 = none.
func UserCapMilli(pod *corev1.Pod) int64 {
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
