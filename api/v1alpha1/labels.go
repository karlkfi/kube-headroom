package v1alpha1

// GroupName is the API group and the single prefix for every label and
// annotation Headroom owns. Defined once here so migrating the prefix later
// (design doc §9.1) is a one-line change; it equals the CRD's apiGroup by
// design (cert-manager style).
const GroupName = "kube-headroom.dev"

// Label and annotation keys (design doc §6.3, §8.1), all derived from GroupName.
const (
	// LabelMode selects Headroom's management mode for an object. On a
	// Namespace, ModeManaged opts its pods in; on a Pod, ModeUnmanaged opts it
	// out even inside a managed namespace. The values are enum keywords, not
	// booleans, so unquoted YAML 1.1 tokens (`true`/`false`/`yes`/`no`) can't
	// coerce and silently change meaning. The gate is fail-closed: any absent
	// or unrecognized value means "not managed".
	LabelMode = GroupName + "/mode"
	// AnnotationStatus carries the per-pod computed status JSON (§8.1).
	AnnotationStatus = GroupName + "/status"
	// AnnotationMaxCPU is an optional per-pod ceiling in milli/quantity form (§5.3).
	AnnotationMaxCPU = GroupName + "/max-cpu"
)

// Enum-keyword values for LabelMode (design doc §6.3).
const (
	// ModeManaged on a Namespace enrolls its pods for CPU-limit management.
	ModeManaged = "managed"
	// ModeUnmanaged on a Pod excludes it, overriding its namespace enrollment.
	ModeUnmanaged = "unmanaged"
)
