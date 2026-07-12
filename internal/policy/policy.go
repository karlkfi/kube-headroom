// Package policy contains the pure, side-effect-free core of Headroom: given a
// node's allocatable CPU and the pods bound to it, decide each managed pod's
// target CPU limit. It has no Kubernetes dependencies so the entire policy
// (design doc §5) is exercisable with table and property tests.
//
// All CPU quantities are integer milli-cores (1000m = 1 core), matching
// resource.Quantity's MilliValue().
package policy

import "math"

// PodInput describes one pod bound to the node, reduced to the fields the
// policy needs. RequestMilli is the pod's aggregate CPU request (sum of its
// containers, including sidecars per their effective-request rules); the
// reconciler splits the resulting pod target across containers pro-rata.
type PodInput struct {
	Key               string
	RequestMilli      int64
	CurrentLimitMilli int64 // enforced limit today; 0 = unset
	Managed           bool  // eligible + opted-in; only managed pods receive burst
	UserCapMilli      int64 // optional per-pod ceiling (headroom.<prefix>/max-cpu); 0 = none
}

// Config holds the tunable policy knobs (design doc §9.2). Zero value is not
// valid; use DefaultConfig and override.
type Config struct {
	MinBurstFloorMilli int64   // absolute burst floor for tiny requests (default 1000m)
	MaxMultiplier      float64 // limit ≤ request × this; ≤0 disables (default 10.0)
	DeadbandGrow       float64 // skip a grow within this fraction of current (default 0.10)
	DeadbandShrink     float64 // skip a shrink within this fraction of current (default 0.05)
	QuantumMilli       int64   // quantize targets to this to avoid arithmetic churn (default 10m)
}

// DefaultConfig returns the documented defaults (§9.2).
func DefaultConfig() Config {
	return Config{
		MinBurstFloorMilli: 1000,
		MaxMultiplier:      10.0,
		DeadbandGrow:       0.10,
		DeadbandShrink:     0.05,
		QuantumMilli:       10,
	}
}

// Decision is the outcome for one managed pod. TargetLimitMilli is the computed
// ceiling; Apply is false when the change is within the deadband (§6.2b) and
// should be skipped to avoid a patch.
type Decision struct {
	Key              string
	TargetLimitMilli int64
	Apply            bool
	Reason           string
}

// NodeStats are the node-level aggregates the reconciler surfaces in events,
// annotations, and metrics (§8.1), returned so it need not recompute them.
type NodeStats struct {
	AllocatableMilli     int64
	TotalRequestsMilli   int64 // all non-terminal pods on the node (physical slack basis)
	ManagedRequestsMilli int64 // M: managed pods only (distribution basis)
	SlackMilli           int64 // S = allocatable − TotalRequests, floored at 0 for distribution
	ManagedPods          int   // N: count of managed pods
	Factor               float64
}

// ComputeNode is the signature documented in §9.2: deterministic and
// side-effect free, returning one Decision per managed pod (input order).
func ComputeNode(allocatableMilli int64, pods []PodInput, cfg Config) []Decision {
	_, decisions := Compute(allocatableMilli, pods, cfg)
	return decisions
}

// Compute is ComputeNode plus the node aggregates. Slack is defined from ALL
// pods' requests (physical truth, §5.4); distribution divides slack across
// managed pods only. When slack ≤ 0 the node is request-overcommitted and every
// managed limit collapses to its request — pure cpu.weight sharing (§5.4).
func Compute(allocatableMilli int64, pods []PodInput, cfg Config) (NodeStats, []Decision) {
	var totalReq, managedReq int64
	var managedCount int
	for _, p := range pods {
		totalReq += p.RequestMilli
		if p.Managed {
			managedReq += p.RequestMilli
			managedCount++
		}
	}

	slack := allocatableMilli - totalReq
	stats := NodeStats{
		AllocatableMilli:     allocatableMilli,
		TotalRequestsMilli:   totalReq,
		ManagedRequestsMilli: managedReq,
		SlackMilli:           max(slack, 0),
		ManagedPods:          managedCount,
		Factor:               1.0,
	}
	if managedReq > 0 && slack > 0 {
		stats.Factor = 1.0 + float64(slack)/float64(managedReq)
	}

	decisions := make([]Decision, 0, managedCount)
	for _, p := range pods {
		if !p.Managed {
			continue
		}
		decisions = append(decisions, decide(p, stats, cfg))
	}
	return stats, decisions
}

// decide computes one managed pod's target limit and deadband verdict.
func decide(p PodInput, s NodeStats, cfg Config) Decision {
	req := p.RequestMilli

	// Slack ≤ 0 (or degenerate M): collapse to request. (§5.4)
	if s.SlackMilli <= 0 || s.ManagedRequestsMilli <= 0 {
		target := clampAndQuantize(req, req, s.AllocatableMilli, p.UserCapMilli, cfg)
		return finalize(p, target, "slack<=0: limit=request", cfg)
	}

	// Proportional burst: S × request / M. (§5.1)
	proportional := float64(s.SlackMilli) * float64(req) / float64(s.ManagedRequestsMilli)

	// Slack-aware floor: min(configured floor, equal share of actual slack).
	// Collapses toward 0 as the node fills, preserving the proportional
	// incentive under contention. (§5.2)
	equalShare := float64(s.SlackMilli) / float64(s.ManagedPods)
	floor := math.Min(float64(cfg.MinBurstFloorMilli), equalShare)

	burst := math.Max(proportional, floor)
	raw := req + int64(math.Round(burst))

	target := clampAndQuantize(raw, req, s.AllocatableMilli, p.UserCapMilli, cfg)
	return finalize(p, target, reasonFor(target, p.CurrentLimitMilli), cfg)
}

// clampAndQuantize applies §5.3 caps in order then quantizes, keeping the
// result within [request, hi] where hi = min(allocatable, request×maxMult, userCap).
func clampAndQuantize(raw, req, allocatable, userCap int64, cfg Config) int64 {
	hi := allocatable
	if cfg.MaxMultiplier > 0 {
		hi = min(hi, int64(math.Round(float64(req)*cfg.MaxMultiplier)))
	}
	if userCap > 0 {
		hi = min(hi, userCap)
	}
	// request is the hard floor even if caps would push below it.
	if hi < req {
		hi = req
	}
	raw = min(max(raw, req), hi)

	q := cfg.QuantumMilli
	if q <= 0 {
		return raw
	}
	target := ((raw + q/2) / q) * q // round to nearest quantum
	if target > hi {
		target -= q // stepped over the cap; step back
	}
	if target < req {
		target = req // never below request (quantization must not violate the floor)
	}
	return target
}

// finalize applies the deadband/hysteresis rule (§6.2b) to set Apply.
func finalize(p PodInput, target int64, reason string, cfg Config) Decision {
	d := Decision{Key: p.Key, TargetLimitMilli: target, Reason: reason}
	cur := p.CurrentLimitMilli
	switch {
	case target == cur:
		d.Apply = false
		d.Reason = "unchanged"
	case cur <= 0:
		d.Apply = true // no enforced limit yet; must set one
	default:
		band := cfg.DeadbandGrow
		if target < cur {
			band = cfg.DeadbandShrink
		}
		frac := math.Abs(float64(target-cur)) / float64(cur)
		d.Apply = frac >= band
		if !d.Apply {
			d.Reason = "within-deadband"
		}
	}
	return d
}

func reasonFor(target, current int64) string {
	switch {
	case current <= 0:
		return "set"
	case target > current:
		return "grow"
	case target < current:
		return "shrink"
	default:
		return "unchanged"
	}
}
