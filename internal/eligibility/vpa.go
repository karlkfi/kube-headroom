package eligibility

import (
	"slices"
	"strings"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const (
	vpaOff          = "Off" // spec.updatePolicy.updateMode and containerPolicies[].mode
	vpaAllNames     = "*"
	vpaRequestsOnly = "RequestsOnly"

	kindReplicaSet = "ReplicaSet"
)

// VPA is the part of a VerticalPodAutoscaler that decides whether it writes CPU
// limits. The type is defined here because the CRD is optional: Headroom takes no
// dependency on the autoscaler module.
type VPA struct {
	Name       string
	TargetKind string
	TargetName string
	UpdateMode string
	Policies   []VPAContainerPolicy
}

// VPAContainerPolicy is one entry of spec.resourcePolicy.containerPolicies.
type VPAContainerPolicy struct {
	ContainerName       string   `json:"containerName,omitempty"`
	Mode                string   `json:"mode,omitempty"`
	ControlledResources []string `json:"controlledResources,omitempty"`
	ControlledValues    string   `json:"controlledValues,omitempty"`
}

// LimitManagingVPA names a VPA that can write the pod's CPU limit, or returns ""
// when none does (§6.3, §8.3). VPA picks the oldest of several VPAs on one target;
// any of them disqualifies the pod here, because the mistake is then a visible skip
// rather than two writers on one field.
func LimitManagingVPA(pod *corev1.Pod, rcs []ResizableContainer, vpas []VPA) string {
	for i := range vpas {
		v := &vpas[i]
		if !podUnderTarget(pod, v.TargetKind, v.TargetName) {
			continue
		}
		if slices.ContainsFunc(rcs, func(c ResizableContainer) bool { return v.writesCPULimit(c.Name) }) {
			return v.Name
		}
	}
	return ""
}

// writesCPULimit applies VPA's own defaulting: an unset update mode is Auto, unset
// controlledValues is RequestsAndLimits, unset controlledResources includes CPU, and
// a policy naming the container beats the "*" entry. Every mode but Off writes the
// limit, on a running pod or when VPA admits its replacement. A container with no
// limit yet is no exception: VPA scales whatever limit Headroom would give it.
func (v *VPA) writesCPULimit(container string) bool {
	if v.UpdateMode == vpaOff {
		return false
	}
	p := v.policyFor(container)
	if p == nil {
		return true
	}
	if p.Mode == vpaOff || p.ControlledValues == vpaRequestsOnly {
		return false
	}
	return p.ControlledResources == nil || slices.Contains(p.ControlledResources, string(corev1.ResourceCPU))
}

func (v *VPA) policyFor(container string) *VPAContainerPolicy {
	var fallback *VPAContainerPolicy
	for i := range v.Policies {
		switch v.Policies[i].ContainerName {
		case container:
			return &v.Policies[i]
		case vpaAllNames:
			fallback = &v.Policies[i]
		}
	}
	return fallback
}

// podUnderTarget reports whether a VPA's targetRef resolves to the pod's controller.
// VPA walks ownerReferences up to the topmost controller and compares kind and name.
// That takes a read per level, so this derives the two common two-level chains from
// the names their controllers generate instead: a Deployment's ReplicaSet is
// <name>-<hash>, and a CronJob's Job is <name>-<time>. A controller further up a
// longer chain is not seen.
func podUnderTarget(pod *corev1.Pod, kind, name string) bool {
	owner := metav1.GetControllerOf(pod)
	if owner == nil {
		return false
	}
	if owner.Kind == kind && owner.Name == name {
		return true
	}
	parent := owner.Name[:max(strings.LastIndex(owner.Name, "-"), 0)]
	switch owner.Kind {
	case kindReplicaSet:
		return kind != kindReplicaSet && parent == name
	case "Job":
		return kind == "CronJob" && parent == name
	}
	return false
}
