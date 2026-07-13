package v1

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	kubeheadroomv1alpha1 "github.com/karlkfi/kube-headroom/api/v1alpha1"
)

const (
	nsA  = "team-a"
	cApp = "app"
)

// --- fixtures ---------------------------------------------------------------

func ptr[T any](v T) *T { return &v }

// newDefaulter builds a PodCustomDefaulter backed by a fake client preloaded with
// the given objects (typically a HeadroomConfig and Namespaces).
func newDefaulter(objs ...client.Object) *PodCustomDefaulter {
	scheme := runtime.NewScheme()
	utilruntime.Must(corev1.AddToScheme(scheme))
	utilruntime.Must(kubeheadroomv1alpha1.AddToScheme(scheme))
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(objs...).Build()
	return &PodCustomDefaulter{Reader: c}
}

// config returns a HeadroomConfig with seeding on (dryRun off, enabled, 2×, 10m
// quantum); mutate tweaks the spec for a specific case.
func config(mutate func(*kubeheadroomv1alpha1.HeadroomConfigSpec)) *kubeheadroomv1alpha1.HeadroomConfig {
	spec := kubeheadroomv1alpha1.HeadroomConfigSpec{
		DryRun:             ptr(false),
		Quantum:            resource.MustParse("10m"),
		ExcludedNamespaces: []string{"kube-system"},
		Webhook: kubeheadroomv1alpha1.Webhook{
			Enabled:           ptr(true),
			InitialMultiplier: resource.MustParse("2"),
		},
	}
	if mutate != nil {
		mutate(&spec)
	}
	return &kubeheadroomv1alpha1.HeadroomConfig{
		ObjectMeta: metav1.ObjectMeta{Name: kubeheadroomv1alpha1.SingletonName},
		Spec:       spec,
	}
}

func managedNS() *corev1.Namespace {
	return &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{
		Name:   nsA,
		Labels: map[string]string{kubeheadroomv1alpha1.LabelMode: kubeheadroomv1alpha1.ModeManaged},
	}}
}

// container builds a container with an optional CPU request/limit (0 = unset).
func container(name string, reqMilli, limMilli int64) corev1.Container {
	c := corev1.Container{Name: name, Resources: corev1.ResourceRequirements{
		Requests: corev1.ResourceList{}, Limits: corev1.ResourceList{},
	}}
	if reqMilli > 0 {
		c.Resources.Requests[corev1.ResourceCPU] = *resource.NewMilliQuantity(reqMilli, resource.DecimalSI)
	}
	if limMilli > 0 {
		c.Resources.Limits[corev1.ResourceCPU] = *resource.NewMilliQuantity(limMilli, resource.DecimalSI)
	}
	return c
}

func pod(cs ...corev1.Container) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "p", Namespace: nsA},
		Spec:       corev1.PodSpec{Containers: cs},
	}
}

// limitMilli reads a container's CPU limit in milli-cores by name (0 = unset).
func limitMilli(p *corev1.Pod, name string) int64 {
	find := func(cs []corev1.Container) (int64, bool) {
		for i := range cs {
			if cs[i].Name == name {
				return cs[i].Resources.Limits.Cpu().MilliValue(), true
			}
		}
		return 0, false
	}
	if v, ok := find(p.Spec.Containers); ok {
		return v
	}
	v, _ := find(p.Spec.InitContainers)
	return v
}

// --- tests ------------------------------------------------------------------

func TestDefaultSeedsBirthLimit(t *testing.T) {
	d := newDefaulter(config(nil), managedNS())
	p := pod(container(cApp, 100, 0))
	if err := d.Default(context.Background(), p); err != nil {
		t.Fatalf("Default returned error: %v", err)
	}
	if got := limitMilli(p, cApp); got != 200 {
		t.Errorf("seeded limit = %dm, want 200m (100m × 2)", got)
	}
}

// TestDefaultSuppressed covers every gate that must leave the pod untouched.
func TestDefaultSuppressed(t *testing.T) {
	tests := []struct {
		name string
		objs []client.Object
		pod  *corev1.Pod
	}{
		{
			name: "dry-run seeds nothing",
			objs: []client.Object{config(func(s *kubeheadroomv1alpha1.HeadroomConfigSpec) { s.DryRun = ptr(true) }), managedNS()},
			pod:  pod(container(cApp, 100, 0)),
		},
		{
			name: "webhook disabled seeds nothing",
			objs: []client.Object{config(func(s *kubeheadroomv1alpha1.HeadroomConfigSpec) { s.Webhook.Enabled = ptr(false) }), managedNS()},
			pod:  pod(container(cApp, 100, 0)),
		},
		{
			name: "multiplier <=1 seeds nothing",
			objs: []client.Object{config(func(s *kubeheadroomv1alpha1.HeadroomConfigSpec) {
				s.Webhook.InitialMultiplier = resource.MustParse("1")
			}), managedNS()},
			pod: pod(container(cApp, 100, 0)),
		},
		{
			name: "no HeadroomConfig => dry-run posture, seeds nothing",
			objs: []client.Object{managedNS()},
			pod:  pod(container(cApp, 100, 0)),
		},
		{
			name: "namespace not opted in",
			objs: []client.Object{config(nil), &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: nsA}}},
			pod:  pod(container(cApp, 100, 0)),
		},
		{
			name: "namespace explicitly excluded even if labeled",
			objs: []client.Object{config(func(s *kubeheadroomv1alpha1.HeadroomConfigSpec) { s.ExcludedNamespaces = []string{nsA} }), managedNS()},
			pod:  pod(container(cApp, 100, 0)),
		},
		{
			name: "pod opted out via label",
			objs: []client.Object{config(nil), managedNS()},
			pod: func() *corev1.Pod {
				p := pod(container(cApp, 100, 0))
				p.Labels = map[string]string{kubeheadroomv1alpha1.LabelMode: kubeheadroomv1alpha1.ModeUnmanaged}
				return p
			}(),
		},
		{
			name: "owner excluded",
			objs: []client.Object{config(func(s *kubeheadroomv1alpha1.HeadroomConfigSpec) {
				s.ExcludedOwners = []kubeheadroomv1alpha1.ExcludedOwner{{Kind: "DaemonSet", APIGroup: "apps"}}
			}), managedNS()},
			pod: func() *corev1.Pod {
				p := pod(container(cApp, 100, 0))
				p.OwnerReferences = []metav1.OwnerReference{{Kind: "DaemonSet", APIVersion: "apps/v1", Name: "fluentd"}}
				return p
			}(),
		},
		{
			name: "restart-on-resize container disqualifies the whole pod",
			objs: []client.Object{config(nil), managedNS()},
			pod: func() *corev1.Pod {
				restart := container("side", 50, 0)
				restart.ResizePolicy = []corev1.ContainerResizePolicy{{ResourceName: corev1.ResourceCPU, RestartPolicy: corev1.RestartContainer}}
				return pod(container(cApp, 100, 0), restart)
			}(),
		},
		{
			name: "container without a cpu request is left untouched",
			objs: []client.Object{config(nil), managedNS()},
			pod:  pod(container(cApp, 0, 0)),
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			d := newDefaulter(tc.objs...)
			// Snapshot every container's limit; the gate must leave them all unchanged.
			before := map[string]int64{}
			for _, cs := range [][]corev1.Container{tc.pod.Spec.Containers, tc.pod.Spec.InitContainers} {
				for i := range cs {
					before[cs[i].Name] = limitMilli(tc.pod, cs[i].Name)
				}
			}
			if err := d.Default(context.Background(), tc.pod); err != nil {
				t.Fatalf("Default returned error: %v", err)
			}
			for name, was := range before {
				if got := limitMilli(tc.pod, name); got != was {
					t.Errorf("container %s limit changed %dm -> %dm; expected no seeding", name, was, got)
				}
			}
		})
	}
}

func TestDefaultAlreadyLimitedUntouched(t *testing.T) {
	d := newDefaulter(config(nil), managedNS())
	// app already has a limit (controller owns it post-bind); web has none.
	p := pod(container(cApp, 100, 300), container("web", 100, 0))
	if err := d.Default(context.Background(), p); err != nil {
		t.Fatalf("Default returned error: %v", err)
	}
	if got := limitMilli(p, cApp); got != 300 {
		t.Errorf("already-limited container = %dm, want 300m (untouched)", got)
	}
	if got := limitMilli(p, "web"); got != 200 {
		t.Errorf("unlimited container = %dm, want 200m (seeded)", got)
	}
}

func TestDefaultSeedsSidecarNotPlainInit(t *testing.T) {
	d := newDefaulter(config(nil), managedNS())
	always := corev1.ContainerRestartPolicyAlways
	p := pod(container(cApp, 100, 0))
	sidecar := container("proxy", 50, 0)
	sidecar.RestartPolicy = &always
	plainInit := container("boot", 500, 0)
	p.Spec.InitContainers = []corev1.Container{plainInit, sidecar}

	if err := d.Default(context.Background(), p); err != nil {
		t.Fatalf("Default returned error: %v", err)
	}
	if got := limitMilli(p, cApp); got != 200 {
		t.Errorf("app limit = %dm, want 200m", got)
	}
	if got := limitMilli(p, "proxy"); got != 100 {
		t.Errorf("sidecar limit = %dm, want 100m (50m × 2)", got)
	}
	if got := limitMilli(p, "boot"); got != 0 {
		t.Errorf("plain init limit = %dm, want 0m (untouched)", got)
	}
}

// TestDefaultClampsToMaxMultiplier proves the birth multiplier is clamped to
// spec.maxMultiplier, so an oversized InitialMultiplier — even one that bypassed
// CRD validation — cannot seed an unbounded CPU limit (Q18).
func TestDefaultClampsToMaxMultiplier(t *testing.T) {
	d := newDefaulter(config(func(s *kubeheadroomv1alpha1.HeadroomConfigSpec) {
		s.Webhook.InitialMultiplier = resource.MustParse("1000") // absurd (validation-bypassed)
		s.MaxMultiplier = resource.MustParse("10")               // policy cap
	}), managedNS())
	p := pod(container(cApp, 100, 0))
	if err := d.Default(context.Background(), p); err != nil {
		t.Fatalf("Default returned error: %v", err)
	}
	if got := limitMilli(p, cApp); got != 1000 {
		t.Errorf("seeded limit = %dm, want 1000m (clamped to 100m × 10, not × 1000)", got)
	}
}

// TestDefaultMaxMultiplierDisabledNoClamp proves maxMultiplier "0" (the disable
// sentinel) leaves the birth multiplier unclamped.
func TestDefaultMaxMultiplierDisabledNoClamp(t *testing.T) {
	d := newDefaulter(config(func(s *kubeheadroomv1alpha1.HeadroomConfigSpec) {
		s.Webhook.InitialMultiplier = resource.MustParse("5")
		s.MaxMultiplier = resource.MustParse("0") // cap disabled
	}), managedNS())
	p := pod(container(cApp, 100, 0))
	if err := d.Default(context.Background(), p); err != nil {
		t.Fatalf("Default returned error: %v", err)
	}
	if got := limitMilli(p, cApp); got != 500 {
		t.Errorf("seeded limit = %dm, want 500m (100m × 5, cap disabled)", got)
	}
}

func TestDefaultQuantizes(t *testing.T) {
	// request 105m × 2 = 210m; with a 100m quantum it rounds to 200m.
	d := newDefaulter(config(func(s *kubeheadroomv1alpha1.HeadroomConfigSpec) { s.Quantum = resource.MustParse("100m") }), managedNS())
	p := pod(container(cApp, 105, 0))
	if err := d.Default(context.Background(), p); err != nil {
		t.Fatalf("Default returned error: %v", err)
	}
	if got := limitMilli(p, cApp); got != 200 {
		t.Errorf("seeded limit = %dm, want 200m (round(210m) to 100m grid)", got)
	}
}
