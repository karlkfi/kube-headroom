package v1alpha1

// GroupName is the API group and the single prefix for every label and
// annotation Headroom owns. Defined once here so migrating the prefix later
// (design doc §9.1) is a one-line change; it equals the CRD's apiGroup by
// design (cert-manager style).
const GroupName = "kube-headroom.dev"

// Label and annotation keys (design doc §6.3, §8.1), all derived from GroupName.
const (
	// LabelEnabled on a Namespace opts its pods in ("true").
	LabelEnabled = GroupName + "/enabled"
	// LabelManaged on a Pod opts it out ("false") even in an enabled namespace.
	LabelManaged = GroupName + "/managed"
	// AnnotationStatus carries the per-pod computed status JSON (§8.1).
	AnnotationStatus = GroupName + "/status"
	// AnnotationMaxCPU is an optional per-pod ceiling in milli/quantity form (§5.3).
	AnnotationMaxCPU = GroupName + "/max-cpu"
)
