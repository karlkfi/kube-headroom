package controller

import (
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	kubeheadroomv1alpha1 "github.com/karlkfi/kube-headroom/api/v1alpha1"
	"github.com/karlkfi/kube-headroom/internal/eligibility"
)

// Shared test constants (kept here so the envtest specs in node_controller_test.go
// and the pure specs in this file reuse them; satisfies goconst).
const (
	nsA  = "team-a"
	cApp = "app"

	// osWindows mirrors the eligibility package's node OS sentinel for the
	// envtest node fixtures (the production constant is unexported there).
	osWindows = "windows"

	// Exclusion-gate fixtures shared across the pure and envtest specs.
	kindDaemonSet     = "DaemonSet"
	groupApps         = "apps"
	nodeExcludedLabel = "headroom-excluded"
	labelTrue         = "true"
)

func TestSplitLimit(t *testing.T) {
	tests := []struct {
		name   string
		target int64
		rcs    []eligibility.ResizableContainer
		want   map[string]int64
	}{
		{
			name:   "single container gets the whole target",
			target: 1500,
			rcs:    []eligibility.ResizableContainer{{Name: cApp, RequestMilli: 200}},
			want:   map[string]int64{cApp: 1500},
		},
		{
			name:   "even split of equal requests",
			target: 1000,
			rcs:    []eligibility.ResizableContainer{{Name: "a", RequestMilli: 100}, {Name: "b", RequestMilli: 100}},
			want:   map[string]int64{"a": 500, "b": 500},
		},
		{
			name:   "pro-rata by request",
			target: 900,
			rcs:    []eligibility.ResizableContainer{{Name: "big", RequestMilli: 200}, {Name: "small", RequestMilli: 100}},
			want:   map[string]int64{"big": 600, "small": 300},
		},
		{
			name:   "remainder goes to the larger fractional part",
			target: 1000,
			rcs:    []eligibility.ResizableContainer{{Name: "a", RequestMilli: 100}, {Name: "b", RequestMilli: 200}},
			// exact: a=333.33 b=666.66 -> floors 333/666 leftover 1 -> b (bigger frac)
			want: map[string]int64{"a": 333, "b": 667},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := splitLimit(tc.target, tc.rcs)
			if len(got) != len(tc.want) {
				t.Fatalf("got %v want %v", got, tc.want)
			}
			var sum int64
			for k, v := range got {
				if v != tc.want[k] {
					t.Errorf("container %s: got %d want %d", k, v, tc.want[k])
				}
				sum += v
			}
			if sum != tc.target {
				t.Errorf("split does not sum to target: got %d want %d", sum, tc.target)
			}
		})
	}
}

// TestSplitLimitInvariants asserts the two safety properties over a range of
// splits: the per-container limits sum to the target and none dips below its
// own request.
func TestSplitLimitInvariants(t *testing.T) {
	rcs := []eligibility.ResizableContainer{
		{Name: "a", RequestMilli: 150},
		{Name: "b", RequestMilli: 350},
		{Name: "c", RequestMilli: 500},
	}
	total := eligibility.PodCPURequestMilli(rcs) // 1000
	for target := total; target <= 10*total; target += 137 {
		got := splitLimit(target, rcs)
		var sum int64
		for _, c := range rcs {
			if got[c.Name] < c.RequestMilli {
				t.Fatalf("target %d: container %s limit %d below request %d", target, c.Name, got[c.Name], c.RequestMilli)
			}
			sum += got[c.Name]
		}
		if sum != target {
			t.Fatalf("target %d: sum %d != target", target, sum)
		}
	}
}

func TestResolveConfig(t *testing.T) {
	tru := true
	hc := &kubeheadroomv1alpha1.HeadroomConfig{Spec: kubeheadroomv1alpha1.HeadroomConfigSpec{
		MinBurstFloor:  resource.MustParse("2"),
		MaxMultiplier:  resource.MustParse("8"),
		Quantum:        resource.MustParse("25m"),
		Deadband:       kubeheadroomv1alpha1.Deadband{GrowPercent: 15, ShrinkPercent: 5},
		DryRun:         &tru,
		DebouncePeriod: metav1.Duration{Duration: 3 * time.Second},
		RateLimits:     kubeheadroomv1alpha1.RateLimits{PerNodePatchesPerSecond: 20},
	}}
	got := resolveConfig(hc, defaultDebouncePeriod)
	if got.policy.MinBurstFloorMilli != 2000 {
		t.Errorf("MinBurstFloorMilli = %d, want 2000", got.policy.MinBurstFloorMilli)
	}
	if got.policy.MaxMultiplier != 8 {
		t.Errorf("MaxMultiplier = %v, want 8", got.policy.MaxMultiplier)
	}
	if got.policy.QuantumMilli != 25 {
		t.Errorf("QuantumMilli = %d, want 25", got.policy.QuantumMilli)
	}
	if got.policy.DeadbandGrow != 0.15 || got.policy.DeadbandShrink != 0.05 {
		t.Errorf("deadband = %v/%v, want 0.15/0.05", got.policy.DeadbandGrow, got.policy.DeadbandShrink)
	}
	if !got.dryRun {
		t.Error("dryRun should be true")
	}
	if got.perNodePPS != 20 {
		t.Errorf("perNodePPS = %v, want 20", got.perNodePPS)
	}
	if got.debouncePeriod != 3*time.Second {
		t.Errorf("debounce = %v, want 3s", got.debouncePeriod)
	}
}
