package v1

import (
	"context"
	"math"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	kubeheadroomv1alpha1 "github.com/karlkfi/kube-headroom/api/v1alpha1"
	"github.com/karlkfi/kube-headroom/internal/eligibility"
)

// podlog is the logger for the birth-limit webhook.
var podlog = logf.Log.WithName("pod-webhook")

// SetupPodWebhookWithManager registers the birth-limit defaulting webhook for
// Pod (§6.5). The manager's cached client backs the handler's HeadroomConfig and
// Namespace reads.
func SetupPodWebhookWithManager(mgr ctrl.Manager) error {
	return ctrl.NewWebhookManagedBy(mgr, &corev1.Pod{}).
		WithDefaulter(&PodCustomDefaulter{Reader: mgr.GetClient()}).
		Complete()
}

// The webhook is namespace-scoped to the mode=managed label via a namespaceSelector
// on the MutatingWebhookConfiguration (config/webhook/manifests_patch.yaml); the
// failurePolicy is Ignore so a missed mutation degrades to plain v1 eligibility and
// never blocks pod creation (§6.5). The verb is create only — post-birth CPU limits
// belong to the node reconciler. timeoutSeconds is 5 (below the 10s default) so a
// stalled webhook bounds the latency it can add to a pod CREATE before the
// apiserver gives up and, under Ignore, admits the pod unmutated: a mutating
// webhook sits in the synchronous API path, so it must fail open fast. The handler
// itself only does cached reads, so steady-state latency is sub-millisecond.
// +kubebuilder:webhook:path=/mutate--v1-pod,mutating=true,failurePolicy=ignore,sideEffects=None,groups=core,resources=pods,verbs=create,versions=v1,name=mpod-v1.kb.io,admissionReviewVersions=v1,timeoutSeconds=5

// PodCustomDefaulter seeds an absent CPU limit at pod-create time so short-lived
// pods and boot-time-quota runtimes — which the node reconciler may never reach in
// time — are born with a usable ceiling (§6.5). It reuses the shared eligibility
// gates (internal/eligibility) so the webhook and the controller agree on which
// pods are manageable.
type PodCustomDefaulter struct {
	// Reader reads the HeadroomConfig singleton and the pod's Namespace.
	Reader client.Reader
}

// Default implements webhook.CustomDefaulter. It never returns an error: any
// lookup failure or ineligibility leaves the pod unmutated, honoring the
// failurePolicy: Ignore contract that pod creation must never be blocked (§6.5).
func (d *PodCustomDefaulter) Default(ctx context.Context, pod *corev1.Pod) error {
	cfg, ok := d.loadConfig(ctx)
	if !ok || !cfg.enabled || cfg.dryRun {
		// Absent/unreadable config, webhook disabled, or dry-run (observation-only,
		// §9.3): seed nothing.
		return nil
	}
	// Same pod-local gates the controller applies (§6.3), minus the QoS-class check
	// (status.qosClass is unset at admission): opt-out and restart-on-resize.
	if eligibility.OptedOut(pod) {
		return nil
	}
	if !d.namespaceManaged(ctx, pod, cfg.spec) {
		return nil
	}
	if eligibility.OwnerExcluded(pod, cfg.spec.ExcludedOwners) {
		return nil
	}
	rcs := eligibility.ResizableContainers(pod)
	if eligibility.HasRestartOnResize(rcs) {
		return nil // a restart-on-resize container disqualifies the whole pod (§9.4.2)
	}

	if seeded := seedBirthLimits(pod, rcs, cfg.multiplier, cfg.maxMultiplier, cfg.quantumMilli); len(seeded) > 0 {
		podlog.Info("seeded birth CPU limits",
			"namespace", pod.Namespace, "pod", podName(pod),
			"containers", seeded, "multiplier", cfg.multiplier, "maxMultiplier", cfg.maxMultiplier)
	}
	return nil
}

// seedBirthLimits sets limits.cpu = round(request × multiplier), quantized, on
// every resizable container that has a CPU request but no CPU limit, provided the
// result exceeds the request so the pod stays Burstable with real burst room
// (§6.5). A container without a CPU request is left untouched — adding a limit
// there would silently inflate its request (the apiserver defaults request to the
// limit), consuming tenant quota. It mutates pod in place and returns the names of
// the containers it seeded. The set of resizable containers comes from the shared
// eligibility rule; this function only maps names back to the mutable pointers.
func seedBirthLimits(pod *corev1.Pod, rcs []eligibility.ResizableContainer, multiplier, maxMultiplier float64, quantumMilli int64) []string {
	// Clamp the birth multiplier to the policy cap (spec.maxMultiplier; "0"
	// disables) so the webhook never seeds a limit the node reconciler would
	// immediately shrink — and, defensively, so an oversized InitialMultiplier
	// (even one that slipped past CRD validation) can never seed an unbounded CPU
	// limit (§5.3, §6.5).
	if maxMultiplier > 0 && multiplier > maxMultiplier {
		multiplier = maxMultiplier
	}
	if multiplier <= 1 {
		return nil // ≤1× is no burst room; treat as "no seeding"
	}
	byName := containerPtrsByName(pod)
	var seeded []string
	for _, rc := range rcs {
		if rc.RequestMilli <= 0 || rc.LimitMilli > 0 {
			continue // no request (would inflate it) or already limited (controller owns it)
		}
		target := quantizeMilli(int64(math.Round(float64(rc.RequestMilli)*multiplier)), quantumMilli)
		if target <= rc.RequestMilli {
			continue
		}
		c := byName[rc.Name]
		if c == nil {
			continue
		}
		if c.Resources.Limits == nil {
			c.Resources.Limits = corev1.ResourceList{}
		}
		c.Resources.Limits[corev1.ResourceCPU] = *resource.NewMilliQuantity(target, resource.DecimalSI)
		seeded = append(seeded, rc.Name)
	}
	return seeded
}

// containerPtrsByName indexes a pod's app and restartable-init containers by name
// so the webhook can mutate the exact containers eligibility.ResizableContainers
// selected. Container names are unique across app + init containers in a valid
// pod; admission rejects duplicates independently.
func containerPtrsByName(pod *corev1.Pod) map[string]*corev1.Container {
	m := make(map[string]*corev1.Container, len(pod.Spec.Containers)+len(pod.Spec.InitContainers))
	for i := range pod.Spec.Containers {
		m[pod.Spec.Containers[i].Name] = &pod.Spec.Containers[i]
	}
	for i := range pod.Spec.InitContainers {
		m[pod.Spec.InitContainers[i].Name] = &pod.Spec.InitContainers[i]
	}
	return m
}

// quantizeMilli rounds v to the nearest multiple of q (q ≤ 0 disables), matching
// the policy core's rounding so a birth limit and the reconciled limit line up on
// the same grid (§5.3).
func quantizeMilli(v, q int64) int64 {
	if q <= 0 {
		return v
	}
	return ((v + q/2) / q) * q
}

// webhookConfig is HeadroomConfig reduced to what the webhook acts on.
type webhookConfig struct {
	enabled       bool
	dryRun        bool
	multiplier    float64
	maxMultiplier float64
	quantumMilli  int64
	spec          *kubeheadroomv1alpha1.HeadroomConfigSpec
}

// loadConfig reads the HeadroomConfig singleton. When it is absent the webhook
// adopts the controller's absent-config posture — dry-run — so it stays inert
// until an operator opts in (§9.3). The bool is false only on a read error other
// than NotFound, which fails open (no mutation).
func (d *PodCustomDefaulter) loadConfig(ctx context.Context) (webhookConfig, bool) {
	var hc kubeheadroomv1alpha1.HeadroomConfig
	err := d.Reader.Get(ctx, types.NamespacedName{Name: kubeheadroomv1alpha1.SingletonName}, &hc)
	if apierrors.IsNotFound(err) {
		return webhookConfig{dryRun: true}, true
	}
	if err != nil {
		podlog.V(1).Info("could not read HeadroomConfig; seeding nothing", "err", err.Error())
		return webhookConfig{}, false
	}
	s := hc.Spec
	enabled := true
	if s.Webhook.Enabled != nil {
		enabled = *s.Webhook.Enabled
	}
	dryRun := true
	if s.DryRun != nil {
		dryRun = *s.DryRun
	}
	return webhookConfig{
		enabled:       enabled,
		dryRun:        dryRun,
		multiplier:    s.Webhook.InitialMultiplier.AsApproximateFloat64(),
		maxMultiplier: s.MaxMultiplier.AsApproximateFloat64(),
		quantumMilli:  s.Quantum.MilliValue(),
		spec:          &s,
	}, true
}

// namespaceManaged confirms the pod's namespace is opted in under the live config
// (§6.3), honoring ExcludedNamespaces and a custom NamespaceSelector even though
// the MutatingWebhookConfiguration already coarse-filters on the mode label. A
// namespace it cannot read fails open (no mutation).
func (d *PodCustomDefaulter) namespaceManaged(ctx context.Context, pod *corev1.Pod, spec *kubeheadroomv1alpha1.HeadroomConfigSpec) bool {
	name := pod.Namespace
	if name == "" {
		if req, err := admission.RequestFromContext(ctx); err == nil {
			name = req.Namespace
		}
	}
	if name == "" {
		return false
	}
	var ns corev1.Namespace
	if err := d.Reader.Get(ctx, types.NamespacedName{Name: name}, &ns); err != nil {
		return false
	}
	return eligibility.NamespaceManaged(&ns, spec)
}

// podName returns a human-readable identifier for logging; a generateName pod has
// no name yet at admission.
func podName(pod *corev1.Pod) string {
	if pod.Name != "" {
		return pod.Name
	}
	return pod.GenerateName + "<pending>"
}
