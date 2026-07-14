package controller

import (
	"testing"
	"time"
)

// enqueueDelay must splay the debounce by ±50% around the base so a restart wave
// (informer LIST → a Create per pod, every node enqueued at once) desynchronizes
// (§8.6/§9.4.4), while never straying outside [base/2, 1.5*base).
func TestEnqueueDelayJitterBounds(t *testing.T) {
	const base = 2 * time.Second
	r := &NodeReconciler{DebouncePeriod: base}

	// Sweep the whole [0,1) jitter fraction: the extremes pin the bounds and the
	// midpoint recovers the un-jittered base.
	cases := []struct {
		frac float64
		want time.Duration
	}{
		{0.0, base / 2},               // lower bound: base - base/2
		{0.5, base},                   // center: exactly the base debounce
		{0.999999, base + base/2 - 1}, // just under the upper bound
	}
	for _, c := range cases {
		r.randFloat = func() float64 { return c.frac }
		got := r.enqueueDelay()
		if d := got - c.want; d < -time.Millisecond || d > time.Millisecond {
			t.Errorf("enqueueDelay(frac=%v) = %v, want ≈ %v", c.frac, got, c.want)
		}
		if got < base/2 || got >= base+base/2 {
			t.Errorf("enqueueDelay(frac=%v) = %v outside [%v, %v)", c.frac, got, base/2, base+base/2)
		}
	}
}

// The default (unseeded) source must stay in bounds and actually vary — a fixed
// debounce (the Q35 gap) would return the same value every call.
func TestEnqueueDelayJitterDefaultSourceSpreads(t *testing.T) {
	const base = 2 * time.Second
	r := &NodeReconciler{DebouncePeriod: base}

	seen := map[time.Duration]struct{}{}
	for range 100 {
		got := r.enqueueDelay()
		if got < base/2 || got >= base+base/2 {
			t.Fatalf("enqueueDelay() = %v outside [%v, %v)", got, base/2, base+base/2)
		}
		seen[got] = struct{}{}
	}
	if len(seen) < 2 {
		t.Errorf("enqueueDelay produced %d distinct values over 100 calls; expected a spread, not a fixed debounce", len(seen))
	}
}

// The jitter tracks the resolved base: when spec.debouncePeriod is published via
// setDebounce (no struct override), the splay scales to that value, not the
// hardcoded default.
func TestEnqueueDelayJitterTracksDynamicDebounce(t *testing.T) {
	const dynamic = 10 * time.Second
	r := &NodeReconciler{}
	r.setDebounce(dynamic)

	r.randFloat = func() float64 { return 0.0 } // lower bound
	if got, want := r.enqueueDelay(), dynamic/2; got != want {
		t.Errorf("lower-bound enqueueDelay = %v, want %v", got, want)
	}
	r.randFloat = func() float64 { return 0.5 } // center recovers the base
	if got := r.enqueueDelay(); got != dynamic {
		t.Errorf("center enqueueDelay = %v, want %v", got, dynamic)
	}
}
