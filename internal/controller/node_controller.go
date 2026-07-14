package controller

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/time/rate"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/events"
	"k8s.io/client-go/util/workqueue"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	kubeheadroomv1alpha1 "github.com/karlkfi/kube-headroom/api/v1alpha1"
	"github.com/karlkfi/kube-headroom/internal/eligibility"
	"github.com/karlkfi/kube-headroom/internal/policy"
)

const (
	// podNodeNameIndex is the field-index key the reconciler lists pods by so it
	// can find every pod bound to a node without a full-cache scan.
	podNodeNameIndex = "spec.nodeName"

	// defaultFieldManager is the stable SSA field owner for the CPU-limit leaf
	// (§6.2, §9.4.1). Force=true wrests limits.cpu from the pod's creator once and
	// then holds it without churn (spike Q2d).
	defaultFieldManager = "headroom"

	// defaultDebouncePeriod collapses a burst of scheduling events on one node
	// into a single recompute (§6.2). Used when HeadroomConfig is absent.
	defaultDebouncePeriod = 2 * time.Second

	// defaultBackoffPeriod is how long a pod is skipped after a resize is refused
	// (quota 403 or kubelet Infeasible) before Headroom retries it (§6.4).
	defaultBackoffPeriod = 60 * time.Second
)

// NodeReconciler recomputes and applies managed CPU limits for every pod bound
// to a node, the node being the unit of reconciliation (§6.2). It is the wiring
// around the pure policy core in internal/policy.
type NodeReconciler struct {
	client.Client
	Scheme *runtime.Scheme

	// Recorder emits CPULimitAdjusted / ResizeInfeasible / ResizeForbidden events
	// on managed pods (§8.1) via the events/v1 API. A nil Recorder is tolerated
	// (events are skipped).
	Recorder events.EventRecorder

	// FieldManager is the SSA owner for limits.cpu (defaults to "headroom").
	FieldManager string
	// DebouncePeriod, when set, forces the enqueue debounce and overrides the
	// spec.debouncePeriod resolved from the live config. Left zero in production
	// (the config drives the debounce); tests set it for deterministic timing.
	DebouncePeriod time.Duration
	// BackoffPeriod is the ineligibility window after a refused resize.
	BackoffPeriod time.Duration

	// dynamicDebounce caches spec.debouncePeriod (nanoseconds) as of the last
	// reconcile so the event handler — which runs outside Reconcile and has no
	// config in hand — enqueues node keys with the configured delay (§6.2).
	dynamicDebounce atomic.Int64

	mu       sync.Mutex
	limiters map[string]*rate.Limiter // node name -> per-node patch token bucket
	backoff  sync.Map                 // pod key (ns/name) -> time.Time expiry

	// podSeries tracks, per node, the managed pods currently exporting a
	// podLimitCores series, so a pod that leaves a node or becomes ineligible has
	// its series deleted (the pod-labelled analogue of forgetNode's node-series
	// cleanup). Its per-node sizes also feed the cluster-wide podsManaged gauge.
	// Guarded by mu. node name -> pod key (ns/name) -> label values.
	podSeries map[string]map[string]podSeriesRef

	// now is overridable in tests; defaults to time.Now.
	now func() time.Time
}

// resolvedConfig is HeadroomConfig reduced to what a reconcile needs: the pure
// policy knobs plus the operational toggles the reconciler itself acts on.
type resolvedConfig struct {
	policy         policy.Config
	spec           kubeheadroomv1alpha1.HeadroomConfigSpec
	dryRun         bool
	perNodePPS     float64
	debouncePeriod time.Duration
}

// podSeriesRef holds the label values of a podLimitCores series so it can be
// deleted without re-fetching the pod (which may be gone by cleanup time).
type podSeriesRef struct {
	namespace string
	name      string
}

// The RBAC this reconciler needs (pods, pods/resize, nodes, namespaces) is
// declared once on HeadroomConfigReconciler; see headroomconfig_controller.go.

// Reconcile recomputes every managed pod's CPU limit for one node and applies
// the decisions the policy says to apply (§6.2 step 4). The request name is a
// node name; the request namespace is always empty.
func (r *NodeReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	// Measure wall-clock reconcile latency (§8.1). Real time, not the injectable
	// clock (which tests freeze for backoff/timestamp determinism).
	start := time.Now()
	defer func() { reconcileDuration.Observe(time.Since(start).Seconds()) }()

	cfg, err := r.loadConfig(ctx)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("load config: %w", err)
	}
	// Publish the resolved debounce so subsequent watch events enqueue with the
	// configured delay rather than the hardcoded default (§6.2).
	r.setDebounce(cfg.debouncePeriod)

	var node corev1.Node
	if err := r.Get(ctx, types.NamespacedName{Name: req.Name}, &node); err != nil {
		if apierrors.IsNotFound(err) {
			r.forgetNode(req.Name) // node gone: drop its limiter state (§6.2 step 2)
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, fmt.Errorf("get node %s: %w", req.Name, err)
	}
	allocatableMilli := node.Status.Allocatable.Cpu().MilliValue()

	// Windows / static-CPU-manager / NUMA-pinned nodes forbid in-place resize
	// (§6.3): recompute slack for observability but manage none of their pods.
	nodeManaged := eligibility.NodeManageable(&node, &cfg.spec)
	if !nodeManaged {
		log.V(1).Info("node excluded from management; pods contribute slack only", "node", req.Name)
	}

	var podList corev1.PodList
	if err := r.List(ctx, &podList, client.MatchingFields{podNodeNameIndex: req.Name}); err != nil {
		return ctrl.Result{}, fmt.Errorf("list pods on node %s: %w", req.Name, err)
	}

	inputs, byKey, err := r.buildInputs(ctx, cfg, podList.Items, nodeManaged)
	if err != nil {
		return ctrl.Result{}, err
	}

	stats, decisions := policy.Compute(allocatableMilli, inputs, cfg.policy)
	log.V(1).Info("computed node targets", "node", req.Name,
		"allocatableMilli", stats.AllocatableMilli, "slackMilli", stats.SlackMilli,
		"factor", stats.Factor, "managedPods", stats.ManagedPods, "decisions", len(decisions))

	// Node-level policy inputs as gauges — computed even for an unmanaged node,
	// so slack/factor are observable everywhere (§8.1).
	nodeFactor.WithLabelValues(req.Name).Set(stats.Factor)
	nodeSlackCores.WithLabelValues(req.Name).Set(float64(stats.SlackMilli) / 1000.0)
	nodeManagedPods.WithLabelValues(req.Name).Set(float64(stats.ManagedPods))

	// Per-pod target-limit gauges — the §8.1 money-graph series. Set for every
	// managed pod's computed target (the ceiling Headroom would/does enforce),
	// independent of whether this pass applies it: the value is meaningful in
	// dry-run and even when a later rate-limit break skips the actual patch.
	// syncPodMetrics then deletes series for pods no longer managed on this node.
	current := make(map[string]podSeriesRef, len(decisions))
	for _, d := range decisions {
		pod := byKey[d.Key]
		if pod == nil {
			continue
		}
		podLimitCores.WithLabelValues(pod.Namespace, pod.Name).Set(float64(d.TargetLimitMilli) / 1000.0)
		current[d.Key] = podSeriesRef{namespace: pod.Namespace, name: pod.Name}
	}
	r.syncPodMetrics(req.Name, current)

	nodePods := len(inputs)
	limiter := r.limiterFor(req.Name, cfg.perNodePPS)
	rateLimited := false
	for _, d := range decisions {
		pod := byKey[d.Key]
		if pod == nil {
			continue
		}

		// Every managed pod carries a status annotation explaining its current
		// ceiling, refreshed on change in dry-run and live alike (§8.1, §9.3).
		// Metadata only — not a resize — so it is exempt from the dry-run patch ban.
		status := buildPodStatus(stats, nodePods, d.TargetLimitMilli, cfg.dryRun)
		if err := r.writePodStatus(ctx, pod, status); err != nil {
			log.V(1).Info("status annotation write failed", "pod", d.Key, "err", err.Error())
		}

		if !d.Apply {
			continue
		}

		currentMilli := eligibility.PodCurrentLimitMilli(eligibility.ResizableContainers(pod))
		if cfg.dryRun {
			// Dry-run meters and annotates what would change but issues no patch
			// (§9.3): the default, safe adoption path.
			resizesTotal.WithLabelValues(resultDryRun).Inc()
			log.Info("dry-run: would resize", "pod", d.Key,
				"currentMilli", currentMilli, "targetMilli", d.TargetLimitMilli, "reason", d.Reason)
			r.recordEvent(pod, corev1.EventTypeNormal, reasonCPULimitAdjusted,
				adjustMessage(currentMilli, d.TargetLimitMilli, stats, true))
			continue
		}
		// Per-node token bucket bounds write pressure (§6.2 step 4c, §7). When the
		// bucket is empty, apply the rest on a follow-up reconcile rather than block.
		if !limiter.Allow() {
			rateLimited = true
			break
		}
		if err := r.applyResize(ctx, pod, d.TargetLimitMilli); err != nil {
			if res, handled := r.classifyResizeError(ctx, pod, d.Key, err); handled {
				rateLimited = rateLimited || res
				continue
			}
			resizesTotal.WithLabelValues(resultError).Inc()
			return ctrl.Result{}, fmt.Errorf("resize %s: %w", d.Key, err)
		}
		resizesTotal.WithLabelValues(resultApplied).Inc()
		r.recordEvent(pod, corev1.EventTypeNormal, reasonCPULimitAdjusted,
			adjustMessage(currentMilli, d.TargetLimitMilli, stats, false))
	}

	if rateLimited {
		return ctrl.Result{RequeueAfter: time.Second}, nil
	}
	return ctrl.Result{}, nil
}

// buildInputs turns the pods on a node into policy inputs. Every non-terminal
// pod contributes to slack (§5.4); only eligible, namespace-enrolled, non-backed
// -off pods are marked Managed and thus receive a limit. It returns the inputs
// and a key→pod map so decisions can be applied without re-fetching.
func (r *NodeReconciler) buildInputs(ctx context.Context, cfg *resolvedConfig, pods []corev1.Pod, nodeManaged bool) ([]policy.PodInput, map[string]*corev1.Pod, error) {
	nsManaged := map[string]bool{}
	inputs := make([]policy.PodInput, 0, len(pods))
	byKey := make(map[string]*corev1.Pod, len(pods))

	for i := range pods {
		pod := &pods[i]
		if eligibility.PodTerminal(pod) {
			continue // terminal pods hold no CPU; they neither book slack nor get managed
		}
		rcs := eligibility.ResizableContainers(pod)

		managed := false
		if nodeManaged && eligibility.Eligible(pod, rcs) && !eligibility.OwnerExcluded(pod, cfg.spec.ExcludedOwners) && !r.inBackoff(pod) {
			ok, cached := nsManaged[pod.Namespace]
			if !cached {
				var ns corev1.Namespace
				if err := r.Get(ctx, types.NamespacedName{Name: pod.Namespace}, &ns); err != nil {
					if !apierrors.IsNotFound(err) {
						return nil, nil, fmt.Errorf("get namespace %s: %w", pod.Namespace, err)
					}
					ok = false
				} else {
					ok = eligibility.NamespaceManaged(&ns, &cfg.spec)
				}
				nsManaged[pod.Namespace] = ok
			}
			managed = ok
		}

		inputs = append(inputs, buildPodInput(pod, rcs, managed))
		byKey[pod.Namespace+"/"+pod.Name] = pod

		if managed {
			// A managed pod whose kubelet refused the last resize should back off
			// (§6.4); detect the Infeasible condition here so a steady reconcile
			// applies the window without needing the patch to fail synchronously.
			// This branch is reached only when the pod is not already backed off,
			// so the event and counter fire once per backoff window, not per
			// reconcile — a sustained count is the alerting signal (§6.4).
			if podResizeInfeasible(pod) {
				resizesTotal.WithLabelValues(resultInfeasible).Inc()
				r.recordEvent(pod, corev1.EventTypeWarning, reasonResizeInfeasible,
					fmt.Sprintf("kubelet reports the CPU-limit resize is infeasible; backing off %s", r.backoffPeriod()))
				r.setBackoff(pod)
			}
		}
	}
	return inputs, byKey, nil
}

// applyResize patches only limits.cpu of the pod's resizable containers via the
// resize subresource with server-side apply (§9.4.1). The pod target is split
// pro-rata across containers so their limits sum to the target.
func (r *NodeReconciler) applyResize(ctx context.Context, pod *corev1.Pod, targetMilli int64) error {
	rcs := eligibility.ResizableContainers(pod)
	perContainer := splitLimit(targetMilli, rcs)
	if len(perContainer) == 0 {
		return nil
	}

	containers := make([]any, 0, len(perContainer))
	for _, c := range rcs {
		m, ok := perContainer[c.Name]
		if !ok {
			continue
		}
		containers = append(containers, map[string]any{
			"name": c.Name,
			"resources": map[string]any{
				"limits": map[string]any{
					"cpu": resource.NewMilliQuantity(m, resource.DecimalSI).String(),
				},
			},
		})
	}

	obj := map[string]any{
		"apiVersion": "v1",
		"kind":       "Pod",
		"metadata": map[string]any{
			"name":      pod.Name,
			"namespace": pod.Namespace,
		},
		"spec": map[string]any{"containers": containers},
	}
	data, err := json.Marshal(obj)
	if err != nil {
		return fmt.Errorf("marshal apply body: %w", err)
	}

	u := &unstructured.Unstructured{Object: obj}
	return r.SubResource("resize").Patch(ctx, u, client.RawPatch(types.ApplyPatchType, data),
		client.FieldOwner(r.fieldManager()), client.ForceOwnership)
}

// classifyResizeError maps a resize patch error to the §6.4 outcome table,
// metering each outcome and emitting a warning event for the refusals an
// operator should see. It returns (rateLimited, handled): handled=false means
// the error should bubble up and requeue the node with backoff.
func (r *NodeReconciler) classifyResizeError(ctx context.Context, pod *corev1.Pod, key string, err error) (bool, bool) {
	log := logf.FromContext(ctx)
	switch {
	case apierrors.IsNotFound(err):
		return false, true // pod vanished mid-reconcile; nothing to do
	case apierrors.IsConflict(err):
		// Stale generation: the next reconcile recomputes from current state.
		resizesTotal.WithLabelValues(resultConflict).Inc()
		log.V(1).Info("resize conflict, will requeue", "pod", key)
		return true, true
	case apierrors.IsForbidden(err):
		// Quota limits.cpu 403 (spike Q2c): treat like Infeasible — back off.
		resizesTotal.WithLabelValues(resultQuotaDenied).Inc()
		log.Info("resize forbidden (likely limits.cpu quota); backing off", "pod", key, "err", err.Error())
		r.recordEvent(pod, corev1.EventTypeWarning, reasonResizeForbidden,
			fmt.Sprintf("CPU-limit resize forbidden (likely a limits.cpu ResourceQuota); backing off %s", r.backoffPeriod()))
		r.setBackoffKey(key)
		return false, true
	default:
		return false, false
	}
}

// loadConfig reads the HeadroomConfig singleton and reduces it to a
// resolvedConfig. When the config is absent, Headroom runs with documented
// defaults in dry-run (safe, observable) mode (§9.3).
func (r *NodeReconciler) loadConfig(ctx context.Context) (*resolvedConfig, error) {
	var hc kubeheadroomv1alpha1.HeadroomConfig
	err := r.Get(ctx, types.NamespacedName{Name: kubeheadroomv1alpha1.SingletonName}, &hc)
	if apierrors.IsNotFound(err) {
		return &resolvedConfig{
			policy:         policy.DefaultConfig(),
			dryRun:         true,
			perNodePPS:     10,
			debouncePeriod: r.debouncePeriod(),
		}, nil
	}
	if err != nil {
		return nil, err
	}
	return resolveConfig(&hc, r.debouncePeriod()), nil
}

// resolveConfig converts a HeadroomConfig into a resolvedConfig. CRD defaults are
// applied by the apiserver, so zero values here mean "explicitly set to zero".
func resolveConfig(hc *kubeheadroomv1alpha1.HeadroomConfig, fallbackDebounce time.Duration) *resolvedConfig {
	s := hc.Spec
	cfg := policy.Config{
		MinBurstFloorMilli: s.MinBurstFloor.MilliValue(),
		MaxMultiplier:      s.MaxMultiplier.AsApproximateFloat64(),
		DeadbandGrow:       float64(s.Deadband.GrowPercent) / 100.0,
		DeadbandShrink:     float64(s.Deadband.ShrinkPercent) / 100.0,
		QuantumMilli:       s.Quantum.MilliValue(),
	}
	dryRun := true
	if s.DryRun != nil {
		dryRun = *s.DryRun
	}
	perNodePPS := float64(s.RateLimits.PerNodePatchesPerSecond)
	if perNodePPS <= 0 {
		perNodePPS = 10
	}
	debounce := s.DebouncePeriod.Duration
	if debounce <= 0 {
		debounce = fallbackDebounce
	}
	return &resolvedConfig{policy: cfg, spec: s, dryRun: dryRun, perNodePPS: perNodePPS, debouncePeriod: debounce}
}

// --- per-node rate limiting -------------------------------------------------

func (r *NodeReconciler) limiterFor(node string, pps float64) *rate.Limiter {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.limiters == nil {
		r.limiters = map[string]*rate.Limiter{}
	}
	lim, ok := r.limiters[node]
	if !ok {
		// Burst == steady rate: absorb a rollout wave up to one second of budget.
		lim = rate.NewLimiter(rate.Limit(pps), int(pps))
		r.limiters[node] = lim
	} else if lim.Limit() != rate.Limit(pps) {
		lim.SetLimit(rate.Limit(pps))
		lim.SetBurst(int(pps))
	}
	return lim
}

func (r *NodeReconciler) forgetNode(node string) {
	r.mu.Lock()
	delete(r.limiters, node)
	// Drop every per-pod series this node was exporting, then refresh the
	// cluster-wide count from what remains — otherwise a deleted node leaks a
	// series per pod it hosted.
	for _, s := range r.podSeries[node] {
		podLimitCores.DeleteLabelValues(s.namespace, s.name)
	}
	delete(r.podSeries, node)
	r.refreshPodsManagedLocked()
	r.mu.Unlock()
	// Drop the node's gauge series so a deleted node doesn't linger in /metrics.
	nodeFactor.DeleteLabelValues(node)
	nodeSlackCores.DeleteLabelValues(node)
	nodeManagedPods.DeleteLabelValues(node)
}

// syncPodMetrics records the pods currently exporting a podLimitCores series for
// one node and deletes the series for any pod managed on this node last
// reconcile but no longer in `current` (rebound, deleted, or newly ineligible) —
// the pod-labelled analogue of forgetNode. It then refreshes the cluster-wide
// podsManaged gauge from the live series counts.
func (r *NodeReconciler) syncPodMetrics(node string, current map[string]podSeriesRef) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.podSeries == nil {
		r.podSeries = map[string]map[string]podSeriesRef{}
	}
	for key, s := range r.podSeries[node] {
		if _, ok := current[key]; !ok {
			podLimitCores.DeleteLabelValues(s.namespace, s.name)
		}
	}
	if len(current) == 0 {
		delete(r.podSeries, node)
	} else {
		r.podSeries[node] = current
	}
	r.refreshPodsManagedLocked()
}

// refreshPodsManagedLocked sets podsManaged to the total number of managed pods
// across all nodes (the sum of node_managed_pods). Caller must hold mu.
func (r *NodeReconciler) refreshPodsManagedLocked() {
	total := 0
	for _, s := range r.podSeries {
		total += len(s)
	}
	podsManaged.Set(float64(total))
}

// --- backoff state ----------------------------------------------------------

func (r *NodeReconciler) inBackoff(pod *corev1.Pod) bool {
	v, ok := r.backoff.Load(pod.Namespace + "/" + pod.Name)
	if !ok {
		return false
	}
	until := v.(time.Time)
	if r.clock().After(until) {
		r.backoff.Delete(pod.Namespace + "/" + pod.Name)
		return false
	}
	return true
}

func (r *NodeReconciler) setBackoff(pod *corev1.Pod) { r.setBackoffKey(pod.Namespace + "/" + pod.Name) }

func (r *NodeReconciler) setBackoffKey(key string) {
	r.backoff.Store(key, r.clock().Add(r.backoffPeriod()))
}

// --- small accessors with defaults ------------------------------------------

func (r *NodeReconciler) fieldManager() string {
	if r.FieldManager != "" {
		return r.FieldManager
	}
	return defaultFieldManager
}

// debouncePeriod is the static fallback used when resolving a config that does
// not set spec.debouncePeriod: an explicit struct override, else the default.
func (r *NodeReconciler) debouncePeriod() time.Duration {
	if r.DebouncePeriod > 0 {
		return r.DebouncePeriod
	}
	return defaultDebouncePeriod
}

// setDebounce records the debounce resolved from the live config so the event
// handler picks it up on the next enqueue. A zero/negative value is ignored so
// a transient bad read never collapses the debounce.
func (r *NodeReconciler) setDebounce(d time.Duration) {
	if d > 0 {
		r.dynamicDebounce.Store(int64(d))
	}
}

// enqueueDelay is the debounce the event handler applies to a node key. The
// explicit struct override wins (test determinism); otherwise it reflects
// spec.debouncePeriod as of the last reconcile, falling back to the default
// until the first reconcile has published a value.
func (r *NodeReconciler) enqueueDelay() time.Duration {
	if r.DebouncePeriod > 0 {
		return r.DebouncePeriod
	}
	if d := r.dynamicDebounce.Load(); d > 0 {
		return time.Duration(d)
	}
	return defaultDebouncePeriod
}

func (r *NodeReconciler) backoffPeriod() time.Duration {
	if r.BackoffPeriod > 0 {
		return r.BackoffPeriod
	}
	return defaultBackoffPeriod
}

func (r *NodeReconciler) clock() time.Time {
	if r.now != nil {
		return r.now()
	}
	return time.Now()
}

// podResizeInfeasible reports whether the kubelet has marked a pending resize
// Infeasible (§6.4) via the PodResizePending condition.
func podResizeInfeasible(pod *corev1.Pod) bool {
	for _, c := range pod.Status.Conditions {
		if c.Type == corev1.PodResizePending && c.Reason == corev1.PodReasonInfeasible {
			return true
		}
	}
	return false
}

// SetupWithManager registers the node field index and the pod/node watches that
// feed node keys into the reconciler, debounced per §6.2.
func (r *NodeReconciler) SetupWithManager(mgr ctrl.Manager) error {
	if err := mgr.GetFieldIndexer().IndexField(context.Background(), &corev1.Pod{}, podNodeNameIndex,
		func(o client.Object) []string {
			nodeName := o.(*corev1.Pod).Spec.NodeName
			if nodeName == "" {
				return nil
			}
			return []string{nodeName}
		}); err != nil {
		return fmt.Errorf("index pods by %s: %w", podNodeNameIndex, err)
	}

	return ctrl.NewControllerManagedBy(mgr).
		Named("node").
		Watches(&corev1.Pod{}, r.podEventHandler(),
			builder.WithPredicates(predicate.Funcs{UpdateFunc: podUpdateRelevant})).
		Watches(&corev1.Node{}, handler.EnqueueRequestsFromMapFunc(nodeToRequests),
			builder.WithPredicates(predicate.Funcs{UpdateFunc: nodeAllocatableChanged})).
		Complete(r)
}

// podEventHandler enqueues the node a pod is (or was) bound to, delayed by the
// debounce period so a burst of pods landing on one node collapses to a single
// recompute (§6.2 step 3).
func (r *NodeReconciler) podEventHandler() handler.EventHandler {
	enqueue := func(q workqueue.TypedRateLimitingInterface[reconcile.Request], nodeName string) {
		if nodeName == "" {
			return
		}
		q.AddAfter(reconcile.Request{NamespacedName: types.NamespacedName{Name: nodeName}}, r.enqueueDelay())
	}
	return handler.Funcs{
		CreateFunc: func(_ context.Context, e event.CreateEvent, q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
			enqueue(q, e.Object.(*corev1.Pod).Spec.NodeName)
		},
		UpdateFunc: func(_ context.Context, e event.UpdateEvent, q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
			enqueue(q, e.ObjectNew.(*corev1.Pod).Spec.NodeName)
			if old := e.ObjectOld.(*corev1.Pod).Spec.NodeName; old != "" && old != e.ObjectNew.(*corev1.Pod).Spec.NodeName {
				enqueue(q, old) // rare rebind: recompute the node it left, too
			}
		},
		DeleteFunc: func(_ context.Context, e event.DeleteEvent, q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
			enqueue(q, e.Object.(*corev1.Pod).Spec.NodeName)
		},
	}
}

// podUpdateRelevant filters pod updates down to the ones that can change a
// node's slack or a managed limit (§6.2 step 1): binding, terminal transition,
// or a CPU-request change (e.g. VPA resizing an unmanaged pod).
func podUpdateRelevant(e event.UpdateEvent) bool {
	oldPod, ok1 := e.ObjectOld.(*corev1.Pod)
	newPod, ok2 := e.ObjectNew.(*corev1.Pod)
	if !ok1 || !ok2 {
		return false
	}
	if oldPod.Spec.NodeName != newPod.Spec.NodeName {
		return true
	}
	if eligibility.PodTerminal(oldPod) != eligibility.PodTerminal(newPod) {
		return true
	}
	return eligibility.PodCPURequestMilli(eligibility.ResizableContainers(oldPod)) != eligibility.PodCPURequestMilli(eligibility.ResizableContainers(newPod))
}

func nodeToRequests(_ context.Context, o client.Object) []reconcile.Request {
	return []reconcile.Request{{NamespacedName: types.NamespacedName{Name: o.GetName()}}}
}

// nodeAllocatableChanged passes only node updates that move allocatable CPU;
// heartbeat/status churn (which fires constantly) is dropped (§6.2 step 2).
func nodeAllocatableChanged(e event.UpdateEvent) bool {
	oldNode, ok1 := e.ObjectOld.(*corev1.Node)
	newNode, ok2 := e.ObjectNew.(*corev1.Node)
	if !ok1 || !ok2 {
		return false
	}
	return oldNode.Status.Allocatable.Cpu().MilliValue() != newNode.Status.Allocatable.Cpu().MilliValue()
}
