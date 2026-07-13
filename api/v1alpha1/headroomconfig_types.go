package v1alpha1

import (
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

// SingletonName is the only permitted name for a HeadroomConfig (enforced by a
// CEL validation rule on the type). The controller reads exactly this object.
const SingletonName = "cluster"

// PolicyType selects the slack-distribution policy. Only Proportional exists
// today (design doc §5.1); the enum leaves room for future policies.
// +kubebuilder:validation:Enum=Proportional
type PolicyType string

const PolicyProportional PolicyType = "Proportional"

// Deadband holds the hysteresis thresholds that suppress churny resizes
// (§6.2b). Values are percentages of the pod's current limit.
type Deadband struct {
	// GrowPercent skips a limit increase within this percent of the current limit.
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:validation:Maximum=100
	// +kubebuilder:default=10
	// +optional
	GrowPercent int32 `json:"growPercent,omitempty"`

	// ShrinkPercent skips a limit decrease within this percent of the current
	// limit. Tighter than grow: stale-generous limits are a fairness bug while
	// stale-tight limits are only an efficiency bug (§6.2b).
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:validation:Maximum=100
	// +kubebuilder:default=5
	// +optional
	ShrinkPercent int32 `json:"shrinkPercent,omitempty"`
}

// ExcludedOwner matches a pod against one of its ownerReferences so an operator
// can carve specific workloads out of an otherwise opted-in namespace (§6.3).
// Kind is required; APIGroup and Name narrow the match when set.
type ExcludedOwner struct {
	// Kind is the owner kind to exclude, e.g. "DaemonSet" or "StatefulSet".
	// +kubebuilder:validation:MinLength=1
	// +required
	Kind string `json:"kind"`

	// APIGroup restricts the match to owners in this API group (e.g. "apps").
	// Empty matches an owner in any group.
	// +optional
	APIGroup string `json:"apiGroup,omitempty"`

	// Name restricts the match to a single owner object by name. Empty matches
	// any owner of the given Kind.
	// +optional
	Name string `json:"name,omitempty"`
}

// Webhook configures the birth-limit mutating admission webhook (§6.5). The
// webhook seeds an absent CPU limit at pod-create time so short-lived pods (CI,
// batch) and boot-time-quota runtimes (JVM, boot-read GOMAXPROCS) — which the
// node reconciler may never reach in time — are born with a usable ceiling. The
// controller corrects it post-bind for pods that live long enough.
type Webhook struct {
	// Enabled turns birth-limit seeding on. When false the webhook handler is a
	// no-op even if the MutatingWebhookConfiguration is installed, so seeding can
	// be switched off without uninstalling the webhook. Note DryRun also
	// suppresses seeding (dry-run is observation-only, §9.3).
	// +kubebuilder:default=true
	// +optional
	Enabled *bool `json:"enabled,omitempty"`

	// InitialMultiplier seeds an absent CPU limit as requests.cpu × this — the
	// node-independent birth limit the webhook can know before scheduling (§6.5).
	// A container that already carries a CPU limit is left untouched. Values ≤ 1
	// yield no burst room and are treated as "no seeding"; pick a cluster-typical
	// factor (the default 2 gives a 2× birth ceiling). Capped at 100 so a bad
	// config cannot seed an unbounded birth limit (§5.3); the webhook additionally
	// clamps seeding to spec.maxMultiplier at runtime.
	// +kubebuilder:default="2"
	// +kubebuilder:validation:XValidation:rule="quantity(string(self)).compareTo(quantity('100')) <= 0",message="initialMultiplier must not exceed 100 (request × 100)"
	// +optional
	InitialMultiplier resource.Quantity `json:"initialMultiplier,omitempty"`
}

// RateLimits bounds the API-server write pressure from resize patching (§7).
type RateLimits struct {
	// PerNodePatchesPerSecond caps the resize patches issued per node.
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:default=10
	// +optional
	PerNodePatchesPerSecond int32 `json:"perNodePatchesPerSecond,omitempty"`

	// ClientQPS is the controller's global Kubernetes client QPS budget.
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:default=50
	// +optional
	ClientQPS int32 `json:"clientQPS,omitempty"`

	// ClientBurst is the controller's global client burst budget.
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:default=100
	// +optional
	ClientBurst int32 `json:"clientBurst,omitempty"`
}

// HeadroomConfigSpec is the cluster-wide policy configuration (design doc §9.3).
type HeadroomConfigSpec struct {
	// Policy selects the slack-distribution policy.
	// +kubebuilder:default=Proportional
	// +optional
	Policy PolicyType `json:"policy,omitempty"`

	// DryRun computes targets, updates annotations and metrics, but issues no
	// resize patches. Defaults true so a fresh install is safe and observable
	// before it is allowed to act (§9.3).
	// +kubebuilder:default=true
	// +optional
	DryRun *bool `json:"dryRun,omitempty"`

	// MinBurstFloor is the absolute burst floor for tiny requests, applied as
	// min(this, equalShareOfSlack) so it collapses under contention (§5.2).
	// +kubebuilder:default="1"
	// +optional
	MinBurstFloor resource.Quantity `json:"minBurstFloor,omitempty"`

	// MaxMultiplier caps a limit at request × this value; "0" disables (§5.3).
	// A finite cap must not exceed 100 (use "0" for an unbounded batch pool, not a
	// huge number) so it stays a real bound on both the reconciled limit and the
	// webhook's clamped birth limit.
	// +kubebuilder:default="10"
	// +kubebuilder:validation:XValidation:rule="quantity(string(self)).compareTo(quantity('100')) <= 0",message="maxMultiplier must not exceed 100 (use '0' to disable the cap)"
	// +optional
	MaxMultiplier resource.Quantity `json:"maxMultiplier,omitempty"`

	// Quantum rounds computed targets to a multiple of this to avoid arithmetic
	// churn (§5.3).
	// +kubebuilder:default="10m"
	// +optional
	Quantum resource.Quantity `json:"quantum,omitempty"`

	// Deadband holds the grow/shrink hysteresis thresholds.
	// +optional
	Deadband Deadband `json:"deadband,omitempty"`

	// DebouncePeriod collapses a burst of scheduling events on one node into a
	// single recompute (§6.2).
	// +kubebuilder:default="2s"
	// +optional
	DebouncePeriod metav1.Duration `json:"debouncePeriod,omitempty"`

	// RateLimits bounds API-server write pressure.
	// +optional
	RateLimits RateLimits `json:"rateLimits,omitempty"`

	// Webhook configures the birth-limit mutating admission webhook (§6.5).
	// +optional
	Webhook Webhook `json:"webhook,omitempty"`

	// NamespaceSelector selects namespaces whose pods are eligible. When unset,
	// the controller defaults to the label kube-headroom.dev/mode=managed (§6.3).
	// +optional
	NamespaceSelector *metav1.LabelSelector `json:"namespaceSelector,omitempty"`

	// ExcludedNamespaces are never managed regardless of labels.
	// +kubebuilder:default={"kube-system"}
	// +optional
	ExcludedNamespaces []string `json:"excludedNamespaces,omitempty"`

	// ExcludedOwners carves specific workloads out of management even inside an
	// opted-in namespace: a pod is skipped when any of its ownerReferences
	// matches an entry (§6.3). Use it for operator-managed or latency-critical
	// workloads that must keep their static limits.
	// +listType=atomic
	// +optional
	ExcludedOwners []ExcludedOwner `json:"excludedOwners,omitempty"`

	// ExcludedNodeSelector marks nodes whose pods Headroom must not manage — set
	// it to select static CPU/Memory Manager or NUMA-pinned nodes, where in-place
	// resize is prohibited (§6.3, §8.4). Windows nodes are excluded structurally
	// and need no selector. Nil selects no nodes (exclude none).
	// +optional
	ExcludedNodeSelector *metav1.LabelSelector `json:"excludedNodeSelector,omitempty"`
}

// HeadroomConfigStatus is the observed state of the controller.
type HeadroomConfigStatus struct {
	// ObservedGeneration is the .metadata.generation last reconciled.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// ManagedPods is the number of pods currently under management.
	// +optional
	ManagedPods int32 `json:"managedPods,omitempty"`

	// ManagedNodes is the number of nodes with at least one managed pod.
	// +optional
	ManagedNodes int32 `json:"managedNodes,omitempty"`

	// conditions represent the current state of the HeadroomConfig resource.
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Cluster,path=headroomconfigs,shortName=hcfg
// +kubebuilder:validation:XValidation:rule="self.metadata.name == 'cluster'",message="HeadroomConfig is a singleton; the only allowed name is 'cluster'"
// +kubebuilder:printcolumn:name="Policy",type=string,JSONPath=`.spec.policy`
// +kubebuilder:printcolumn:name="DryRun",type=boolean,JSONPath=`.spec.dryRun`
// +kubebuilder:printcolumn:name="ManagedPods",type=integer,JSONPath=`.status.managedPods`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// HeadroomConfig is the cluster-scoped, singleton configuration for Headroom.
type HeadroomConfig struct {
	metav1.TypeMeta `json:",inline"`

	// metadata is a standard object metadata
	// +optional
	metav1.ObjectMeta `json:"metadata,omitzero"`

	// spec defines the desired state of HeadroomConfig
	// +required
	Spec HeadroomConfigSpec `json:"spec"`

	// status defines the observed state of HeadroomConfig
	// +optional
	Status HeadroomConfigStatus `json:"status,omitzero"`
}

// +kubebuilder:object:root=true

// HeadroomConfigList contains a list of HeadroomConfig
type HeadroomConfigList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitzero"`
	Items           []HeadroomConfig `json:"items"`
}

func init() {
	SchemeBuilder.Register(func(s *runtime.Scheme) error {
		s.AddKnownTypes(SchemeGroupVersion, &HeadroomConfig{}, &HeadroomConfigList{})
		return nil
	})
}
