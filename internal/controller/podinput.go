package controller

import (
	"slices"

	corev1 "k8s.io/api/core/v1"

	"github.com/karlkfi/kube-headroom/internal/eligibility"
	"github.com/karlkfi/kube-headroom/internal/policy"
)

// buildPodInput converts a single pod (with its namespace-managed verdict and
// backoff state already resolved) into the policy's reduced PodInput. The
// pod-local eligibility gates live in internal/eligibility.
func buildPodInput(pod *corev1.Pod, rcs []eligibility.ResizableContainer, managed bool) policy.PodInput {
	return policy.PodInput{
		Key:               pod.Namespace + "/" + pod.Name,
		RequestMilli:      eligibility.PodCPURequestMilli(rcs),
		CurrentLimitMilli: eligibility.PodCurrentLimitMilli(rcs),
		Managed:           managed,
		UserCapMilli:      eligibility.UserCapMilli(pod),
		AboveRequest:      guaranteedAtRequest(pod),
	}
}

// guaranteedAtRequest reports whether a limit equal to the request on every app
// and sidecar container would make the pod Guaranteed: every container then has
// CPU and memory limits equal to its requests. Requests never exceed limits, so
// checking each container matches the apiserver's per-resource sums. Non-empty
// pod-level resources set the QoS class themselves, and a container resize cannot
// move them.
func guaranteedAtRequest(pod *corev1.Pod) bool {
	if r := pod.Spec.Resources; r != nil && (len(r.Requests) > 0 || len(r.Limits) > 0) {
		return false
	}
	pinned := func(c *corev1.Container) bool {
		memReq := c.Resources.Requests.Memory()
		return c.Resources.Requests.Cpu().Sign() > 0 && memReq.Sign() > 0 && memReq.Cmp(*c.Resources.Limits.Memory()) == 0
	}
	for i := range pod.Spec.Containers {
		if !pinned(&pod.Spec.Containers[i]) {
			return false
		}
	}
	for i := range pod.Spec.InitContainers {
		c := &pod.Spec.InitContainers[i]
		if !pinned(c) {
			return false
		}
		// Headroom never resizes a non-restartable init container, so its own limit counts.
		sidecar := c.RestartPolicy != nil && *c.RestartPolicy == corev1.ContainerRestartPolicyAlways
		if !sidecar && c.Resources.Requests.Cpu().Cmp(*c.Resources.Limits.Cpu()) != 0 {
			return false
		}
	}
	return true
}

// splitLimit distributes a pod-level target CPU limit across its resizable
// containers pro-rata by CPU request, in whole milli-cores, so the per-container
// limits sum exactly to the pod target and each stays at or above its own
// request. Containers are returned in input order; the largest fractional
// remainders receive the leftover milli-cores.
//
// A container with zero CPU request is omitted from the result entirely: it
// receives no computed burst and its limit is left untouched (§5.4), matching
// the birth-limit webhook. Assigning it a limit of 0 would write limits.cpu:"0",
// which reads back as "unset" and makes the pod look unbounded forever — a
// re-patch every debounce cycle that breaks the §7 zero-churn guarantee (Q24).
func splitLimit(targetMilli int64, rcs []eligibility.ResizableContainer) map[string]int64 {
	out := make(map[string]int64, len(rcs))
	totalReq := eligibility.PodCPURequestMilli(rcs)
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
		if c.RequestMilli <= 0 {
			continue // §5.4: request-less container gets no burst; leave its limit untouched
		}
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
