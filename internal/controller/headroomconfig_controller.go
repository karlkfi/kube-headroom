package controller

import (
	"context"
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	apiequality "k8s.io/apimachinery/pkg/api/equality"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	kubeheadroomv1alpha1 "github.com/karlkfi/kube-headroom/api/v1alpha1"
)

const (
	// conditionReady is the summary condition on HeadroomConfig: True once the
	// controller has observed the config and is reconciling the cluster against it.
	conditionReady = "Ready"

	// reasonReconciled is the Ready condition reason for a normal reconcile.
	reasonReconciled = "Reconciled"

	// statusResyncPeriod is the backstop requeue for the singleton. The managed
	// counts derive from the node reconciler's in-memory series, which no watch
	// event surfaces, so a periodic reconcile is what converges status after a
	// count change the pod/node watches raced ahead of (see Reconcile).
	statusResyncPeriod = 30 * time.Second
)

// managedCountSource reports how many pods Headroom manages and across how many
// nodes. NodeReconciler implements it from the same per-node series that back the
// headroom_pods_managed / headroom_node_managed_pods gauges, so the status this
// reconciler writes and those metrics agree by construction (Q26).
type managedCountSource interface {
	ManagedCounts() (pods, nodes int)
}

// HeadroomConfigReconciler reconciles a HeadroomConfig object
type HeadroomConfigReconciler struct {
	client.Client
	Scheme *runtime.Scheme

	// ManagedState reports the live managed-pod / managed-node counts written into
	// status. In production it is the NodeReconciler. A nil source yields zero
	// counts (used by tests that exercise the status wiring without a running node
	// reconciler).
	ManagedState managedCountSource
}

// The controller only reads the cluster-scoped singleton config and writes its
// status — no create/update/delete on the object, no finalizer. Least privilege.
// +kubebuilder:rbac:groups=kube-headroom.dev,resources=headroomconfigs,verbs=get;list;watch
// +kubebuilder:rbac:groups=kube-headroom.dev,resources=headroomconfigs/status,verbs=get;update;patch

// Cluster-wide permissions the node reconciler (Q4/Q7) needs. Pod writes are
// limited to two paths: patch on the resize subresource for the CPU limit, and
// patch on the pod's metadata for the kube-headroom.dev/status annotation (§8.1
// two-command explainability) — never update/delete, never any other field.
// +kubebuilder:rbac:groups="",resources=pods,verbs=get;list;watch;patch
// +kubebuilder:rbac:groups="",resources=pods/resize,verbs=patch
// +kubebuilder:rbac:groups="",resources=nodes,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=namespaces,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=limitranges,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=events,verbs=create;patch
// +kubebuilder:rbac:groups=autoscaling.k8s.io,resources=verticalpodautoscalers,verbs=get;list;watch

// Reconcile refreshes the singleton HeadroomConfig's status: ObservedGeneration
// (the generation this controller has acted on), the ManagedPods / ManagedNodes
// counts (§8.1), and the Ready condition — so `kubectl get hcfg` and the
// printcolumn report real state instead of the empty status the scaffold left.
// The counts come from the node reconciler's live accounting rather than a fresh
// list, keeping status and the headroom_pods_managed metric in agreement.
func (r *HeadroomConfigReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	var hc kubeheadroomv1alpha1.HeadroomConfig
	if err := r.Get(ctx, req.NamespacedName, &hc); err != nil {
		// Absent (never created, or deleted): there is no object to write status to.
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	pods, nodes := 0, 0
	if r.ManagedState != nil {
		pods, nodes = r.ManagedState.ManagedCounts()
	}

	orig := hc.DeepCopy()
	hc.Status.ObservedGeneration = hc.Generation
	hc.Status.ManagedPods = int32(pods)
	hc.Status.ManagedNodes = int32(nodes)
	meta.SetStatusCondition(&hc.Status.Conditions, metav1.Condition{
		Type:               conditionReady,
		Status:             metav1.ConditionTrue,
		ObservedGeneration: hc.Generation,
		Reason:             reasonReconciled,
		Message:            readyMessage(&hc, pods, nodes),
	})

	// Only write when something actually changed. A steady cluster reconciles on
	// the resync backstop but issues no write, keeping the object (and the
	// generation-filtered self-watch) quiet (§7 low-churn).
	if !apiequality.Semantic.DeepEqual(orig.Status, hc.Status) {
		if err := r.Status().Update(ctx, &hc); err != nil {
			return ctrl.Result{}, fmt.Errorf("update HeadroomConfig status: %w", err)
		}
		log.V(1).Info("updated HeadroomConfig status",
			"managedPods", pods, "managedNodes", nodes, "observedGeneration", hc.Generation)
	}

	return ctrl.Result{RequeueAfter: statusResyncPeriod}, nil
}

// readyMessage summarizes what the controller is doing for `kubectl describe`.
func readyMessage(hc *kubeheadroomv1alpha1.HeadroomConfig, pods, nodes int) string {
	mode := "enforcing"
	if hc.Spec.DryRun == nil || *hc.Spec.DryRun {
		mode = "dry-run"
	}
	return fmt.Sprintf("Reconciling in %s mode; managing %d pods across %d nodes", mode, pods, nodes)
}

// headroomConfigRequests maps any watched pod/node event onto the one singleton
// config key, so a change in what Headroom manages nudges a status refresh.
func headroomConfigRequests(context.Context, client.Object) []reconcile.Request {
	return []reconcile.Request{{NamespacedName: types.NamespacedName{Name: kubeheadroomv1alpha1.SingletonName}}}
}

// SetupWithManager sets up the controller with the Manager. It watches the
// config itself (generation-filtered so its own status writes don't re-trigger
// it) plus pods and nodes — the events that move the managed counts — mapped onto
// the singleton. The Reconcile resync backstop covers the residual case where the
// node reconciler updates its in-memory counts after such an event has fired.
func (r *HeadroomConfigReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&kubeheadroomv1alpha1.HeadroomConfig{}, builder.WithPredicates(predicate.GenerationChangedPredicate{})).
		Watches(&corev1.Pod{}, handler.EnqueueRequestsFromMapFunc(headroomConfigRequests),
			builder.WithPredicates(predicate.Funcs{UpdateFunc: podUpdateRelevant})).
		Watches(&corev1.Node{}, handler.EnqueueRequestsFromMapFunc(headroomConfigRequests),
			builder.WithPredicates(predicate.Funcs{UpdateFunc: nodeAllocatableChanged})).
		Named("headroomconfig").
		Complete(r)
}
