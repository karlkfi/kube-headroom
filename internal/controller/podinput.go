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
	}
}

// splitLimit distributes a pod-level target CPU limit across its resizable
// containers pro-rata by CPU request, in whole milli-cores, so the per-container
// limits sum exactly to the pod target and each stays at or above its own
// request. Containers are returned in input order; the largest fractional
// remainders receive the leftover milli-cores.
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
