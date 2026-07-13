package controller

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kubeheadroomv1alpha1 "github.com/karlkfi/kube-headroom/api/v1alpha1"
	"github.com/karlkfi/kube-headroom/internal/policy"
)

// policyName is the policy identifier surfaced in the status annotation and,
// later, the explain plugin (design §8.1). Bump it when the arithmetic changes.
const policyName = "proportional-v1"

// Event Reasons are stable PascalCase tokens that mirror the resizesTotal result
// labels for the same fact (kubernetes-conventions.md: one vocabulary across
// event, annotation, and metric). Never rewrite one casually — it is contract.
const (
	reasonCPULimitAdjusted = "CPULimitAdjusted" // a managed pod's CPU limit changed (result=applied/dry-run)
	reasonResizeInfeasible = "ResizeInfeasible" // kubelet refused the resize (result=infeasible)
	reasonResizeForbidden  = "ResizeForbidden"  // limits.cpu quota 403 (result=quota-denied)
)

// podStatus is the JSON payload of the kube-headroom.dev/status annotation
// (design §8.1): the node-level policy inputs plus this pod's computed ceiling,
// so any observed throttle is explainable from `kubectl get pod -o yaml` alone —
// no hidden agent state, no metric the tenant can't see.
type podStatus struct {
	Factor          string `json:"factor"`          // F, two decimals, e.g. "2.00"
	Slack           string `json:"slack"`           // node slack S, e.g. "8000m"
	ManagedRequests string `json:"managedRequests"` // M (distribution basis), e.g. "8000m"
	NodePods        int    `json:"nodePods"`        // non-terminal pods bound to the node
	TargetLimit     string `json:"targetLimit"`     // this pod's computed ceiling, e.g. "8000m"
	Policy          string `json:"policy"`          // policyName
	DryRun          bool   `json:"dryRun,omitempty"`
	ComputedAt      string `json:"computedAt"` // RFC3339; stamped only when content changes
}

// milliString renders a milli-core count in the canonical `<n>m` form so the
// annotation and events read in one consistent unit (design §8.1 examples).
func milliString(m int64) string { return fmt.Sprintf("%dm", m) }

// buildPodStatus assembles the annotation payload sans ComputedAt, which
// writePodStatus stamps only when the meaningful content actually changed.
func buildPodStatus(stats policy.NodeStats, nodePods int, targetMilli int64, dryRun bool) podStatus {
	return podStatus{
		Factor:          fmt.Sprintf("%.2f", stats.Factor),
		Slack:           milliString(stats.SlackMilli),
		ManagedRequests: milliString(stats.ManagedRequestsMilli),
		NodePods:        nodePods,
		TargetLimit:     milliString(targetMilli),
		Policy:          policyName,
		DryRun:          dryRun,
	}
}

// writePodStatus refreshes the kube-headroom.dev/status annotation on a managed
// pod, but only when its meaningful content changed — ComputedAt is ignored in
// the comparison and freshly stamped just on a real change, so a steady node
// never rewrites the annotation (keeps §7 low-churn intact). It patches metadata
// only, never the resize subresource, so it runs in dry-run as well (§9.3:
// "compute + annotate + metric, no patches").
func (r *NodeReconciler) writePodStatus(ctx context.Context, pod *corev1.Pod, desired podStatus) error {
	if prevRaw := pod.Annotations[kubeheadroomv1alpha1.AnnotationStatus]; prevRaw != "" {
		var prev podStatus
		if err := json.Unmarshal([]byte(prevRaw), &prev); err == nil {
			prev.ComputedAt = ""
			cmp := desired
			cmp.ComputedAt = ""
			if prev == cmp {
				return nil // unchanged: no write
			}
		}
	}

	desired.ComputedAt = r.clock().UTC().Format(time.RFC3339)
	data, err := json.Marshal(desired)
	if err != nil {
		return fmt.Errorf("marshal pod status: %w", err)
	}
	patch := map[string]any{
		"metadata": map[string]any{
			"annotations": map[string]any{kubeheadroomv1alpha1.AnnotationStatus: string(data)},
		},
	}
	body, err := json.Marshal(patch)
	if err != nil {
		return fmt.Errorf("marshal status patch: %w", err)
	}
	return r.Patch(ctx, pod, client.RawPatch(types.MergePatchType, body))
}

// recordEvent emits a Kubernetes event on the pod, tolerating a nil Recorder so
// the reconciler stays usable in tests that don't wire one. Events fire only on
// a change (a limit adjusted, a resize refused), never every reconcile
// (kubernetes-conventions.md: events on lifecycle transitions only).
func (r *NodeReconciler) recordEvent(pod *corev1.Pod, eventtype, reason, msg string) {
	if r.Recorder == nil {
		return
	}
	r.Recorder.Event(pod, eventtype, reason, msg)
}

// adjustMessage is the human line shared by the CPULimitAdjusted event in both
// live and dry-run modes: current → target plus the node factor and slack that
// explain it (design §8.1 example).
func adjustMessage(currentMilli, targetMilli int64, stats policy.NodeStats, dryRun bool) string {
	prefix := ""
	if dryRun {
		prefix = "(dry-run) would adjust "
	}
	return fmt.Sprintf("%sCPU limit %s → %s (node factor %.2f, slack %s/%s)",
		prefix, milliString(currentMilli), milliString(targetMilli), stats.Factor,
		milliString(stats.SlackMilli), milliString(stats.AllocatableMilli))
}
