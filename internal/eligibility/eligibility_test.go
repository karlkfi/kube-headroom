package eligibility

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	kubeheadroomv1alpha1 "github.com/karlkfi/kube-headroom/api/v1alpha1"
)

const (
	nsA  = "team-a"
	cApp = "app"

	// Exclusion-gate fixtures shared across the specs in this package.
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
			if got := Eligible(tc.pod, ResizableContainers(tc.pod)); got != tc.want {
				t.Errorf("Eligible = %v, want %v", got, tc.want)
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
	rcs := ResizableContainers(pod)
	if len(rcs) != 2 {
		t.Fatalf("expected app + sidecar, got %d containers: %+v", len(rcs), rcs)
	}
	if got := PodCPURequestMilli(rcs); got != 300 {
		t.Errorf("pod request = %d, want 300 (app 200 + sidecar 100, init excluded)", got)
	}
}

func TestPodCurrentLimitMilli(t *testing.T) {
	// Fully limited pod aggregates.
	full := ResizableContainers(burstablePod("full", container("a", 100, 300), container("b", 100, 200)))
	if got := PodCurrentLimitMilli(full); got != 500 {
		t.Errorf("fully-limited pod = %d, want 500", got)
	}
	// Partially limited pod is treated as unset (0) so a limit is set.
	partial := ResizableContainers(burstablePod("partial", container("a", 100, 300), container("b", 100, 0)))
	if got := PodCurrentLimitMilli(partial); got != 0 {
		t.Errorf("partially-limited pod = %d, want 0", got)
	}
	// A request-less sidecar carries no managed limit (§5.4), so its perpetually
	// unset limit must not drag the aggregate to 0 — the app container's limit
	// alone bounds the pod (Q24 steady-state guarantee).
	withSidecar := ResizableContainers(burstablePod("sidecar", container("app", 100, 800), container("agent", 0, 0)))
	if got := PodCurrentLimitMilli(withSidecar); got != 800 {
		t.Errorf("app+request-less-sidecar pod = %d, want 800", got)
	}
}

func TestNamespaceManaged(t *testing.T) {
	managedNS := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{
		Name:   nsA,
		Labels: map[string]string{kubeheadroomv1alpha1.LabelMode: kubeheadroomv1alpha1.ModeManaged},
	}}
	plainNS := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "team-b"}}

	defaultSpec := kubeheadroomv1alpha1.HeadroomConfigSpec{ExcludedNamespaces: []string{"kube-system"}}
	if !NamespaceManaged(managedNS, &defaultSpec) {
		t.Error("labeled namespace should be managed under default selector")
	}
	if NamespaceManaged(plainNS, &defaultSpec) {
		t.Error("unlabeled namespace should not be managed")
	}

	// Excluded namespace is never managed even when labeled.
	excludedSpec := kubeheadroomv1alpha1.HeadroomConfigSpec{ExcludedNamespaces: []string{nsA}}
	if NamespaceManaged(managedNS, &excludedSpec) {
		t.Error("excluded namespace must not be managed")
	}

	// Explicit selector overrides the default label convention.
	selSpec := kubeheadroomv1alpha1.HeadroomConfigSpec{
		NamespaceSelector: &metav1.LabelSelector{MatchLabels: map[string]string{"tier": "prod"}},
	}
	prodNS := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "p", Labels: map[string]string{"tier": "prod"}}}
	if !NamespaceManaged(prodNS, &selSpec) {
		t.Error("namespace matching explicit selector should be managed")
	}
	if NamespaceManaged(managedNS, &selSpec) {
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
			if got := OwnerExcluded(tc.pod, tc.excluded); got != tc.want {
				t.Errorf("OwnerExcluded = %v, want %v", got, tc.want)
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

	if !NodeManageable(linux, empty) {
		t.Error("a plain linux node should be manageable")
	}
	if NodeManageable(windowsByInfo, empty) {
		t.Error("a windows node (NodeInfo) must be excluded")
	}
	if NodeManageable(windowsByLabel, empty) {
		t.Error("a windows node (os label) must be excluded")
	}
	if !NodeManageable(staticCPU, empty) {
		t.Error("without a selector the labeled node is manageable")
	}
	if NodeManageable(staticCPU, withSelector) {
		t.Error("a node matching ExcludedNodeSelector must be excluded")
	}
	if !NodeManageable(linux, withSelector) {
		t.Error("a node not matching the selector stays manageable")
	}
}

func TestUserCapMilli(t *testing.T) {
	pod := burstablePod("capped", container(cApp, 100, 0))
	pod.Annotations = map[string]string{kubeheadroomv1alpha1.AnnotationMaxCPU: "2500m"}
	if got := UserCapMilli(pod); got != 2500 {
		t.Errorf("UserCapMilli = %d, want 2500", got)
	}
	// Unparseable annotation is ignored (0 = no cap).
	pod.Annotations[kubeheadroomv1alpha1.AnnotationMaxCPU] = "not-a-quantity"
	if got := UserCapMilli(pod); got != 0 {
		t.Errorf("UserCapMilli = %d, want 0 for bad value", got)
	}
}
