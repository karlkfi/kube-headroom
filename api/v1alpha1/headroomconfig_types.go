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
	// +kubebuilder:default="10"
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

	// NamespaceSelector selects namespaces whose pods are eligible. When unset,
	// the controller defaults to the label kube-headroom.dev/mode=managed (§6.3).
	// +optional
	NamespaceSelector *metav1.LabelSelector `json:"namespaceSelector,omitempty"`

	// ExcludedNamespaces are never managed regardless of labels.
	// +kubebuilder:default={"kube-system"}
	// +optional
	ExcludedNamespaces []string `json:"excludedNamespaces,omitempty"`
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
