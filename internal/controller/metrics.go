package controller

import (
	"github.com/prometheus/client_golang/prometheus"
	"sigs.k8s.io/controller-runtime/pkg/metrics"
)

// Prometheus surface for the node reconciler (design §8.1, and
// docs/development/kubernetes-conventions.md). The guiding rule from the
// conventions doc: anyone reading an event, an annotation, and a metric for the
// same fact should see one vocabulary, not three. So the resize counter's
// `result` labels mirror the §6.4 outcome table and, where one exists, the
// event Reason for the same fact.
//
// Everything registers on controller-runtime's own registry so it is exposed on
// the manager's /metrics endpoint next to the built-in controller metrics; no
// separate HTTP server.

// metricNamespace prefixes every Headroom metric.
const metricNamespace = "headroom"

// nodeLabel is the shared label key for the node-scoped gauges.
const nodeLabel = "node"

// resize result label values on resizesTotal, one per §6.4 outcome.
const (
	resultApplied     = "applied"      // resize patch accepted (event: CPULimitAdjusted)
	resultDryRun      = "dry-run"      // would apply; no patch issued (dry-run mode, §9.3)
	resultInfeasible  = "infeasible"   // kubelet marked the resize Infeasible (event: ResizeInfeasible)
	resultQuotaDenied = "quota-denied" // limits.cpu ResourceQuota 403 (event: ResizeForbidden)
	resultConflict    = "conflict"     // stale generation; recompute next reconcile
	resultError       = "error"        // unexpected patch error
)

var (
	// nodeFactor is the proportional-policy factor F = 1 + slack/managedRequests
	// (design §5.1) — the single number that explains every managed limit on the
	// node. It is a raw policy input, exposed for debugging per the conventions.
	nodeFactor = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Namespace: metricNamespace,
		Name:      "node_factor",
		Help:      "Proportional-policy factor F = 1 + slack/managedRequests for the node.",
	}, []string{nodeLabel})

	// nodeSlackCores is the node's CPU slack in cores (allocatable minus the sum
	// of all pod requests, floored at 0) — the other raw policy input (§5.4).
	nodeSlackCores = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Namespace: metricNamespace,
		Name:      "node_slack_cores",
		Help:      "Node CPU slack in cores (allocatable minus the sum of all pod requests), floored at 0.",
	}, []string{nodeLabel})

	// nodeManagedPods is the count of Headroom-managed pods on the node (N in the
	// policy), the distribution denominator surfaced for correlation.
	nodeManagedPods = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Namespace: metricNamespace,
		Name:      "node_managed_pods",
		Help:      "Number of Headroom-managed pods bound to the node.",
	}, []string{nodeLabel})

	// resizesTotal counts CPU-limit resize decisions by outcome. The applied and
	// dry-run series are the money counters (rate of writes / would-be writes);
	// the rest mirror the §6.4 refusal paths for alerting.
	resizesTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: metricNamespace,
		Name:      "resizes_total",
		Help:      "CPU-limit resize decisions by outcome (applied, dry-run, infeasible, quota-denied, conflict, error).",
	}, []string{"result"})

	// reconcileDuration is the wall-clock latency of a single node reconcile.
	reconcileDuration = prometheus.NewHistogram(prometheus.HistogramOpts{
		Namespace: metricNamespace,
		Name:      "reconcile_duration_seconds",
		Help:      "Wall-clock duration of a node reconcile.",
		Buckets:   prometheus.DefBuckets,
	})
)

func init() {
	metrics.Registry.MustRegister(nodeFactor, nodeSlackCores, nodeManagedPods, resizesTotal, reconcileDuration)
	// Pre-create the counter series so each exports 0 from process start, making
	// rate()-based alerts well-defined before the first resize ever happens.
	for _, r := range []string{resultApplied, resultDryRun, resultInfeasible, resultQuotaDenied, resultConflict, resultError} {
		resizesTotal.WithLabelValues(r)
	}
}
