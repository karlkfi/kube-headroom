package controller

import (
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	kubeheadroomv1alpha1 "github.com/karlkfi/kube-headroom/api/v1alpha1"
)

// Shared test constants (kept here so both the pure and envtest specs in this
// package reuse them; satisfies goconst).
const (
	nsA  = "team-a"
	cApp = "app"

	// Exclusion-gate fixtures shared across the pure and envtest specs.
	kindDaemonSet     = "DaemonSet"
	groupApps         = "apps"
	nodeExcludedLabel = "headroom-excluded"
	labelTrue         = "true"
)

// container is a compact fixture builder for a resizable app container.
func container(name string, reqMilli, limMilli int64) corev1.Container {
	c := corev1.Container{Name: name, Resources: corev1.ResourceRequirements{
		Requests: corev1.ResourceList{},
		Limits:   corev1.ResourceList{},
	}}
	if reqMilli > 0 {
		c.Resources.Requests[corev1.ResourceCPU] = *resource.NewMilliQuantity(reqMilli, resource.DecimalSI)
	}
	if limMilli > 0 {
		c.Resources.Limits[corev1.ResourceCPU] = *resource.NewMilliQuantity(limMilli, resource.DecimalSI)
	}
	return c
}

func burstablePod(name string, cs ...corev1.Container) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: nsA},
		Spec:       corev1.PodSpec{Containers: cs},
		Status:     corev1.PodStatus{Phase: corev1.PodRunning, QOSClass: corev1.PodQOSBurstable},
	}
}

func TestSplitLimit(t *testing.T) {
	tests := []struct {
		name   string
		target int64
		rcs    []resizableContainer
		want   map[string]int64
	}{
		{
			name:   "single container gets the whole target",
			target: 1500,
			rcs:    []resizableContainer{{Name: cApp, RequestMilli: 200}},
			want:   map[string]int64{cApp: 1500},
		},
		{
			name:   "even split of equal requests",
			target: 1000,
			rcs:    []resizableContainer{{Name: "a", RequestMilli: 100}, {Name: "b", RequestMilli: 100}},
			want:   map[string]int64{"a": 500, "b": 500},
		},
		{
			name:   "pro-rata by request",
			target: 900,
			rcs:    []resizableContainer{{Name: "big", RequestMilli: 200}, {Name: "small", RequestMilli: 100}},
			want:   map[string]int64{"big": 600, "small": 300},
		},
		{
			name:   "remainder goes to the larger fractional part",
			target: 1000,
			rcs:    []resizableContainer{{Name: "a", RequestMilli: 100}, {Name: "b", RequestMilli: 200}},
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
	rcs := []resizableContainer{
		{Name: "a", RequestMilli: 150},
		{Name: "b", RequestMilli: 350},
		{Name: "c", RequestMilli: 500},
	}
	total := podCPURequestMilli(rcs) // 1000
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

func TestEligible(t *testing.T) {
	restartCPU := corev1.Container{
		Name: cApp,
		Resources: corev1.ResourceRequirements{
			Requests: corev1.ResourceList{corev1.ResourceCPU: *resource.NewMilliQuantity(100, resource.DecimalSI)},
		},
		ResizePolicy: []corev1.ContainerResizePolicy{{ResourceName: corev1.ResourceCPU, RestartPolicy: corev1.RestartContainer}},
	}

	tests := []struct {
		name string
		pod  *corev1.Pod
		want bool
	}{
		{
			name: "burstable with cpu request is eligible",
			pod:  burstablePod("ok", container(cApp, 100, 0)),
			want: true,
		},
		{
			name: "opted out via pod label",
			pod: func() *corev1.Pod {
				p := burstablePod("opt-out", container(cApp, 100, 0))
				p.Labels = map[string]string{kubeheadroomv1alpha1.LabelMode: kubeheadroomv1alpha1.ModeUnmanaged}
				return p
			}(),
			want: false,
		},
		{
			name: "guaranteed is excluded",
			pod: func() *corev1.Pod {
				p := burstablePod("guar", container(cApp, 100, 100))
				p.Status.QOSClass = corev1.PodQOSGuaranteed
				return p
			}(),
			want: false,
		},
		{
			name: "no cpu request is excluded",
			pod: func() *corev1.Pod {
				p := burstablePod("besteffort", corev1.Container{Name: cApp})
				return p
			}(),
			want: false,
		},
		{
			name: "terminal pod is excluded",
			pod: func() *corev1.Pod {
				p := burstablePod("done", container(cApp, 100, 0))
				p.Status.Phase = corev1.PodSucceeded
				return p
			}(),
			want: false,
		},
		{
			name: "cpu resizePolicy RestartContainer is excluded",
			pod:  burstablePod("restart", restartCPU),
			want: false,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := eligible(tc.pod, resizableContainers(tc.pod)); got != tc.want {
				t.Errorf("eligible = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestSidecarCountsAsResizable(t *testing.T) {
	always := corev1.ContainerRestartPolicyAlways
	pod := burstablePod("with-sidecar", container(cApp, 200, 0))
	pod.Spec.InitContainers = []corev1.Container{
		{Name: "boot", Resources: corev1.ResourceRequirements{ // plain init: excluded
			Requests: corev1.ResourceList{corev1.ResourceCPU: *resource.NewMilliQuantity(500, resource.DecimalSI)},
		}},
		func() corev1.Container { // native sidecar: included
			c := container("proxy", 100, 0)
			c.RestartPolicy = &always
			return c
		}(),
	}
	rcs := resizableContainers(pod)
	if len(rcs) != 2 {
		t.Fatalf("expected app + sidecar, got %d containers: %+v", len(rcs), rcs)
	}
	if got := podCPURequestMilli(rcs); got != 300 {
		t.Errorf("pod request = %d, want 300 (app 200 + sidecar 100, init excluded)", got)
	}
}

func TestPodCurrentLimitMilli(t *testing.T) {
	// Fully limited pod aggregates.
	full := resizableContainers(burstablePod("full", container("a", 100, 300), container("b", 100, 200)))
	if got := podCurrentLimitMilli(full); got != 500 {
		t.Errorf("fully-limited pod = %d, want 500", got)
	}
	// Partially limited pod is treated as unset (0) so a limit is set.
	partial := resizableContainers(burstablePod("partial", container("a", 100, 300), container("b", 100, 0)))
	if got := podCurrentLimitMilli(partial); got != 0 {
		t.Errorf("partially-limited pod = %d, want 0", got)
	}
}

func TestNamespaceManaged(t *testing.T) {
	managedNS := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{
		Name:   nsA,
		Labels: map[string]string{kubeheadroomv1alpha1.LabelMode: kubeheadroomv1alpha1.ModeManaged},
	}}
	plainNS := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "team-b"}}

	defaultSpec := kubeheadroomv1alpha1.HeadroomConfigSpec{ExcludedNamespaces: []string{"kube-system"}}
	if !namespaceManaged(managedNS, &defaultSpec) {
		t.Error("labeled namespace should be managed under default selector")
	}
	if namespaceManaged(plainNS, &defaultSpec) {
		t.Error("unlabeled namespace should not be managed")
	}

	// Excluded namespace is never managed even when labeled.
	excludedSpec := kubeheadroomv1alpha1.HeadroomConfigSpec{ExcludedNamespaces: []string{nsA}}
	if namespaceManaged(managedNS, &excludedSpec) {
		t.Error("excluded namespace must not be managed")
	}

	// Explicit selector overrides the default label convention.
	selSpec := kubeheadroomv1alpha1.HeadroomConfigSpec{
		NamespaceSelector: &metav1.LabelSelector{MatchLabels: map[string]string{"tier": "prod"}},
	}
	prodNS := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "p", Labels: map[string]string{"tier": "prod"}}}
	if !namespaceManaged(prodNS, &selSpec) {
		t.Error("namespace matching explicit selector should be managed")
	}
	if namespaceManaged(managedNS, &selSpec) {
		t.Error("mode label should not satisfy an explicit selector")
	}
}

func TestOwnerExcluded(t *testing.T) {
	ownedBy := func(kind, apiVersion, name string) *corev1.Pod {
		p := burstablePod("owned", container(cApp, 100, 0))
		p.OwnerReferences = []metav1.OwnerReference{{Kind: kind, APIVersion: apiVersion, Name: name}}
		return p
	}

	tests := []struct {
		name     string
		pod      *corev1.Pod
		excluded []kubeheadroomv1alpha1.ExcludedOwner
		want     bool
	}{
		{
			name: "no owners, no exclusions",
			pod:  burstablePod("plain", container(cApp, 100, 0)),
			want: false,
		},
		{
			name:     "kind-only match",
			pod:      ownedBy(kindDaemonSet, "apps/v1", "fluentd"),
			excluded: []kubeheadroomv1alpha1.ExcludedOwner{{Kind: kindDaemonSet}},
			want:     true,
		},
		{
			name:     "kind matches but apiGroup does not",
			pod:      ownedBy(kindDaemonSet, "apps/v1", "fluentd"),
			excluded: []kubeheadroomv1alpha1.ExcludedOwner{{Kind: kindDaemonSet, APIGroup: "batch"}},
			want:     false,
		},
		{
			name:     "kind + apiGroup match",
			pod:      ownedBy("StatefulSet", "apps/v1", "db"),
			excluded: []kubeheadroomv1alpha1.ExcludedOwner{{Kind: "StatefulSet", APIGroup: groupApps}},
			want:     true,
		},
		{
			name:     "name narrows the match",
			pod:      ownedBy("StatefulSet", "apps/v1", "db"),
			excluded: []kubeheadroomv1alpha1.ExcludedOwner{{Kind: "StatefulSet", Name: "other"}},
			want:     false,
		},
		{
			name:     "core-group owner has empty group",
			pod:      ownedBy("Node", "v1", "node-1"),
			excluded: []kubeheadroomv1alpha1.ExcludedOwner{{Kind: "Node", APIGroup: ""}},
			want:     true,
		},
		{
			name:     "core-group owner does not match a grouped exclusion",
			pod:      ownedBy("Node", "v1", "node-1"),
			excluded: []kubeheadroomv1alpha1.ExcludedOwner{{Kind: "Node", APIGroup: "apps"}},
			want:     false,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := ownerExcluded(tc.pod, tc.excluded); got != tc.want {
				t.Errorf("ownerExcluded = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestNodeManageable(t *testing.T) {
	linux := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "linux-1"}}
	linux.Status.NodeInfo.OperatingSystem = "linux"

	windowsByInfo := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "win-1"}}
	windowsByInfo.Status.NodeInfo.OperatingSystem = osWindows

	windowsByLabel := &corev1.Node{ObjectMeta: metav1.ObjectMeta{
		Name:   "win-2",
		Labels: map[string]string{corev1.LabelOSStable: osWindows},
	}}

	staticCPU := &corev1.Node{ObjectMeta: metav1.ObjectMeta{
		Name:   "numa-1",
		Labels: map[string]string{nodeExcludedLabel: labelTrue},
	}}

	empty := &kubeheadroomv1alpha1.HeadroomConfigSpec{}
	withSelector := &kubeheadroomv1alpha1.HeadroomConfigSpec{
		ExcludedNodeSelector: &metav1.LabelSelector{MatchLabels: map[string]string{nodeExcludedLabel: labelTrue}},
	}

	if !nodeManageable(linux, empty) {
		t.Error("a plain linux node should be manageable")
	}
	if nodeManageable(windowsByInfo, empty) {
		t.Error("a windows node (NodeInfo) must be excluded")
	}
	if nodeManageable(windowsByLabel, empty) {
		t.Error("a windows node (os label) must be excluded")
	}
	if !nodeManageable(staticCPU, empty) {
		t.Error("without a selector the labeled node is manageable")
	}
	if nodeManageable(staticCPU, withSelector) {
		t.Error("a node matching ExcludedNodeSelector must be excluded")
	}
	if !nodeManageable(linux, withSelector) {
		t.Error("a node not matching the selector stays manageable")
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

func TestUserCapMilli(t *testing.T) {
	pod := burstablePod("capped", container(cApp, 100, 0))
	pod.Annotations = map[string]string{kubeheadroomv1alpha1.AnnotationMaxCPU: "2500m"}
	if got := userCapMilli(pod); got != 2500 {
		t.Errorf("userCapMilli = %d, want 2500", got)
	}
	// Unparseable annotation is ignored (0 = no cap).
	pod.Annotations[kubeheadroomv1alpha1.AnnotationMaxCPU] = "not-a-quantity"
	if got := userCapMilli(pod); got != 0 {
		t.Errorf("userCapMilli = %d, want 0 for bad value", got)
	}
}
