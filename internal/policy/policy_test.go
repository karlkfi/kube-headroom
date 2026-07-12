package policy

import (
	"fmt"
	"testing"
)

// managed/unmanaged constructors keep the test tables terse.
func mp(key string, req, cur int64) PodInput {
	return PodInput{Key: key, RequestMilli: req, CurrentLimitMilli: cur, Managed: true}
}
func up(key string, req int64) PodInput {
	return PodInput{Key: key, RequestMilli: req, Managed: false}
}

func targetsByKey(ds []Decision) map[string]int64 {
	m := make(map[string]int64, len(ds))
	for _, d := range ds {
		m[d.Key] = d.TargetLimitMilli
	}
	return m
}

// §5.1 worked example: 16-core node, three managed pods (0.5, 1.5, 6.0),
// M = S = 8 cores → factor 2.0. With the floor disabled this reproduces the
// pure proportional column of the design doc's table exactly.
func TestProportional_WorkedExample_NoFloor(t *testing.T) {
	cfg := DefaultConfig()
	cfg.MinBurstFloorMilli = 0 // isolate the base proportional policy
	pods := []PodInput{mp("A", 500, 500), mp("B", 1500, 1500), mp("C", 6000, 6000)}

	stats, ds := Compute(16000, pods, cfg)

	if stats.Factor != 2.0 {
		t.Fatalf("factor = %v, want 2.0", stats.Factor)
	}
	if stats.SlackMilli != 8000 || stats.ManagedRequestsMilli != 8000 {
		t.Fatalf("slack=%d managed=%d, want 8000/8000", stats.SlackMilli, stats.ManagedRequestsMilli)
	}
	got := targetsByKey(ds)
	want := map[string]int64{"A": 1000, "B": 3000, "C": 12000}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("target[%s] = %d, want %d", k, got[k], v)
		}
	}
}

// §5.2 floor: the same node with the default 1000m floor lifts the smallest
// pod (A: proportional burst 500 < floor 1000) to request+1000, while B and C
// keep their larger proportional bursts.
func TestProportional_WorkedExample_FloorLiftsSmallPod(t *testing.T) {
	cfg := DefaultConfig()
	pods := []PodInput{mp("A", 500, 500), mp("B", 1500, 1500), mp("C", 6000, 6000)}

	_, ds := Compute(16000, pods, cfg)
	got := targetsByKey(ds)

	// A: floor min(1000, S/N=2667)=1000 > proportional 500 → 500+1000=1500,
	// under the 10× cap (5000). B,C: proportional dominates, unchanged.
	want := map[string]int64{"A": 1500, "B": 3000, "C": 12000}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("target[%s] = %d, want %d", k, got[k], v)
		}
	}
}

// §5.4: a request-overcommitted node (slack ≤ 0) collapses every managed limit
// to its request — pure cpu.weight sharing.
func TestSlackNonPositive_CollapsesToRequest(t *testing.T) {
	cfg := DefaultConfig()
	// allocatable 4000, requests total 5000 (incl. an unmanaged hog) → S<0.
	pods := []PodInput{mp("A", 1000, 3000), mp("B", 1000, 3000), up("hog", 3000)}

	stats, ds := Compute(4000, pods, cfg)
	if stats.SlackMilli != 0 {
		t.Fatalf("slack floored to %d, want 0", stats.SlackMilli)
	}
	for _, d := range ds {
		if d.TargetLimitMilli != 1000 {
			t.Errorf("%s target = %d, want 1000 (=request)", d.Key, d.TargetLimitMilli)
		}
		if !d.Apply { // current 3000 → 1000 is a large shrink, must apply
			t.Errorf("%s should apply the shrink to request", d.Key)
		}
	}
}

// §5.4: unmanaged pods consume slack but receive no decision and no burst.
func TestUnmanaged_ConsumeSlackButNoDecision(t *testing.T) {
	cfg := DefaultConfig()
	cfg.MinBurstFloorMilli = 0
	pods := []PodInput{mp("A", 2000, 2000), up("other", 2000)}

	stats, ds := Compute(8000, pods, cfg)
	if len(ds) != 1 || ds[0].Key != "A" {
		t.Fatalf("got %d decisions, want 1 for A", len(ds))
	}
	// S = 8000-4000 = 4000, M = 2000 → factor 3.0, A target = 2000*3 = 6000.
	if stats.Factor != 3.0 || ds[0].TargetLimitMilli != 6000 {
		t.Fatalf("factor=%v A=%d, want 3.0 / 6000", stats.Factor, ds[0].TargetLimitMilli)
	}
}

// §5.3 cap 2: maxMultiplier bounds a tiny request hard. NOTE: this is the
// documented tension with §5.2 — a 10m pod is capped at 100m (10×), so the
// "+1 core" floor never reaches genuinely tiny requests. Locking the literal
// "caps applied after floor" behavior (§5.3).
func TestMaxMultiplier_CapsTinyRequest(t *testing.T) {
	cfg := DefaultConfig() // MaxMultiplier 10
	pods := []PodInput{mp("tiny", 10, 10)}

	_, ds := Compute(16000, pods, cfg)
	if ds[0].TargetLimitMilli != 100 {
		t.Errorf("tiny target = %d, want 100 (10× cap dominates floor)", ds[0].TargetLimitMilli)
	}
}

// §5.3 cap 3: allocatable and per-pod userCap clamp the target.
func TestClamps_AllocatableAndUserCap(t *testing.T) {
	cfg := DefaultConfig()
	cfg.MaxMultiplier = 0 // disable the multiplier cap to isolate the others

	// Allocatable clamp: single managed pod, huge slack.
	_, ds := Compute(4000, []PodInput{mp("A", 1000, 1000)}, cfg)
	if ds[0].TargetLimitMilli != 4000 {
		t.Errorf("allocatable clamp: got %d, want 4000", ds[0].TargetLimitMilli)
	}

	// userCap clamp below allocatable.
	p := mp("B", 1000, 1000)
	p.UserCapMilli = 2500
	_, ds = Compute(16000, []PodInput{p}, cfg)
	if ds[0].TargetLimitMilli != 2500 {
		t.Errorf("userCap clamp: got %d, want 2500", ds[0].TargetLimitMilli)
	}
}

// §5.3 cap 4: targets are quantized to the configured quantum, never above the
// cap and never below the request.
func TestQuantization(t *testing.T) {
	cfg := DefaultConfig()
	cfg.MinBurstFloorMilli = 0
	cfg.MaxMultiplier = 0
	cfg.QuantumMilli = 100
	// Craft slack so the raw target is 3333 → quantizes to 3300.
	// A req 1000, want factor such that 1000*F ≈ 3333 → F=3.333 → S/M=2.333.
	// M=1000, S=2333 → allocatable = 1000+2333 = 3333.
	_, ds := Compute(3333, []PodInput{mp("A", 1000, 1000)}, cfg)
	got := ds[0].TargetLimitMilli
	if got%100 != 0 {
		t.Errorf("target %d not quantized to 100", got)
	}
	if got > 3333 {
		t.Errorf("quantized target %d exceeds allocatable cap 3333", got)
	}
}

// §6.2b deadband: grows use DeadbandGrow (10%), shrinks use DeadbandShrink (5%).
// Target for B in the worked example is 3000; vary CurrentLimit to probe.
func TestDeadband(t *testing.T) {
	cfg := DefaultConfig()
	cfg.MinBurstFloorMilli = 0

	cases := []struct {
		name    string
		current int64
		apply   bool
	}{
		{"grow beyond band", 2000, true},    // +50% ≥ 10%
		{"grow within band", 2950, false},   // +1.7% < 10%
		{"grow at band edge", 2700, true},   // +11.1% ≥ 10%
		{"shrink beyond band", 3200, true},  // -6.25% ≥ 5%
		{"shrink within band", 3050, false}, // -1.6% < 5%
		{"exact", 3000, false},              // no change
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			pods := []PodInput{mp("A", 500, 500), mp("B", 1500, tc.current), mp("C", 6000, 6000)}
			_, ds := Compute(16000, pods, cfg)
			var b Decision
			for _, d := range ds {
				if d.Key == "B" {
					b = d
				}
			}
			if b.TargetLimitMilli != 3000 {
				t.Fatalf("precondition: B target = %d, want 3000", b.TargetLimitMilli)
			}
			if b.Apply != tc.apply {
				t.Errorf("current %d: Apply = %v (%s), want %v", tc.current, b.Apply, b.Reason, tc.apply)
			}
		})
	}
}

// Property (§9.2): request ≤ target ≤ min(allocatable, request×maxMult, userCap)
// across a deterministic grid of nodes.
func TestInvariant_TargetWithinBounds(t *testing.T) {
	cfg := DefaultConfig()
	for _, alloc := range []int64{1000, 4000, 16000, 64000} {
		for _, n := range []int{1, 3, 8} {
			pods := make([]PodInput, n)
			for i := range pods {
				req := int64(100 * (i + 1))
				pods[i] = mp(fmt.Sprintf("p%d", i), req, req)
			}
			_, ds := Compute(alloc, pods, cfg)
			byKey := map[string]PodInput{}
			for _, p := range pods {
				byKey[p.Key] = p
			}
			for _, d := range ds {
				p := byKey[d.Key]
				hi := max(min(alloc, int64(float64(p.RequestMilli)*cfg.MaxMultiplier)), p.RequestMilli)
				if d.TargetLimitMilli < p.RequestMilli {
					t.Errorf("alloc=%d n=%d: %s target %d < request %d", alloc, n, d.Key, d.TargetLimitMilli, p.RequestMilli)
				}
				if d.TargetLimitMilli > hi {
					t.Errorf("alloc=%d n=%d: %s target %d > cap %d", alloc, n, d.Key, d.TargetLimitMilli, hi)
				}
			}
		}
	}
}

// Property (§9.2): adding a pod never raises an incumbent's target (monotonicity).
func TestInvariant_Monotonicity(t *testing.T) {
	cfg := DefaultConfig()
	base := []PodInput{mp("A", 1000, 0), mp("B", 2000, 0), mp("C", 500, 0)}
	_, before := Compute(16000, base, cfg)
	beforeT := targetsByKey(before)

	// Add both a managed and an unmanaged newcomer.
	for _, extra := range []PodInput{mp("D", 1500, 0), up("X", 1500)} {
		_, after := Compute(16000, append(append([]PodInput{}, base...), extra), cfg)
		for k, v := range targetsByKey(after) {
			if bt, ok := beforeT[k]; ok && v > bt {
				t.Errorf("adding %s raised %s target %d → %d", extra.Key, k, bt, v)
			}
		}
	}
}

// Property (§9.2): splitting a managed pod into k pods of equal request leaves
// the aggregate limit unchanged, in the proportional regime (floor/caps not
// binding). Scale-invariance ⇒ no pod-fragmentation incentive (§5.1).
func TestInvariant_ScaleInvariance(t *testing.T) {
	cfg := DefaultConfig()
	cfg.MinBurstFloorMilli = 0 // keep the floor out of the proportional regime
	cfg.MaxMultiplier = 0      // and the multiplier cap

	// One managed 4000m pod plus a fixed 4000m of other managed load so M and S
	// stay well inside caps. allocatable chosen so S ≤ 9M (no allocatable clamp).
	const alloc = 40000
	whole := []PodInput{mp("W", 4000, 0), mp("ballast", 4000, 0)}
	_, dsWhole := Compute(alloc, whole, cfg)
	wholeT := targetsByKey(dsWhole)

	// Split W into four 1000m pods; ballast unchanged so M is identical.
	split := []PodInput{
		mp("w1", 1000, 0), mp("w2", 1000, 0), mp("w3", 1000, 0), mp("w4", 1000, 0),
		mp("ballast", 4000, 0),
	}
	_, dsSplit := Compute(alloc, split, cfg)
	splitT := targetsByKey(dsSplit)

	var splitSum int64
	for _, k := range []string{"w1", "w2", "w3", "w4"} {
		splitSum += splitT[k]
	}
	if splitSum != wholeT["W"] {
		t.Errorf("scale variance: whole W = %d, split sum = %d", wholeT["W"], splitSum)
	}
	if splitT["ballast"] != wholeT["ballast"] {
		t.Errorf("ballast moved under split: %d → %d", wholeT["ballast"], splitT["ballast"])
	}
}
