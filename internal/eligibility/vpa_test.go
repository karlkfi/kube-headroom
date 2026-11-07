package eligibility

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const (
	kindDeployment  = "Deployment"
	kindStatefulSet = "StatefulSet"
	kindJob         = "Job"
	kindCronJob     = "CronJob"
	modeInPlace     = "InPlaceOrRecreate"
	vpaName         = "web-vpa"
	targetWeb       = "web"
	rsWeb           = "web-6d4cf56db6"
	cSidecar        = "sidecar"
)

// ownedPod builds a pod with one app and one sidecar container under a controller.
func ownedPod(ownerKind, ownerName string) *corev1.Pod {
	pod := burstablePod("p", container(cApp, 100, 0), container(cSidecar, 100, 0))
	if ownerKind != "" {
		yes := true
		pod.OwnerReferences = []metav1.OwnerReference{{Kind: ownerKind, Name: ownerName, Controller: &yes}}
	}
	return pod
}

func TestLimitManagingVPAReadsTheVPAsModes(t *testing.T) {
	requestsOnly := VPAContainerPolicy{ContainerName: vpaAllNames, ControlledValues: vpaRequestsOnly}
	tests := []struct {
		name     string
		mode     string
		policies []VPAContainerPolicy
		want     bool
	}{
		{name: "everything defaulted", want: true},
		{name: "Auto", mode: "Auto", want: true},
		{name: "Recreate writes limits at each replacement", mode: "Recreate", want: true},
		{name: "Initial writes limits at creation", mode: "Initial", want: true},
		{name: modeInPlace, mode: modeInPlace, want: true},
		{name: "recommendation mode writes nothing", mode: vpaOff, want: false},
		{name: "recommendation mode with default values", mode: vpaOff,
			policies: []VPAContainerPolicy{{ContainerName: vpaAllNames}}, want: false},
		{name: "RequestsOnly", mode: modeInPlace, policies: []VPAContainerPolicy{requestsOnly}, want: false},
		{name: "explicit RequestsAndLimits",
			policies: []VPAContainerPolicy{{ContainerName: vpaAllNames, ControlledValues: "RequestsAndLimits"}}, want: true},
		{name: "a policy with no controlledValues defaults to limits",
			policies: []VPAContainerPolicy{{ContainerName: vpaAllNames}}, want: true},
		{name: "a policy for another container leaves these defaulted",
			policies: []VPAContainerPolicy{{ContainerName: "another-container", ControlledValues: vpaRequestsOnly}}, want: true},
		{name: "a named policy beats the wildcard, in either order",
			policies: []VPAContainerPolicy{
				{ContainerName: cApp, ControlledValues: vpaRequestsOnly},
				{ContainerName: vpaAllNames},
				{ContainerName: cSidecar, ControlledValues: vpaRequestsOnly},
			}, want: false},
		{name: "one container of two is enough",
			policies: []VPAContainerPolicy{requestsOnly, {ContainerName: cSidecar}}, want: true},
		{name: "scaling turned off for the containers",
			policies: []VPAContainerPolicy{{ContainerName: vpaAllNames, Mode: vpaOff}}, want: false},
		{name: "memory only leaves CPU alone",
			policies: []VPAContainerPolicy{{ContainerName: vpaAllNames, ControlledResources: []string{"memory"}}}, want: false},
		{name: "no resources at all leaves CPU alone",
			policies: []VPAContainerPolicy{{ContainerName: vpaAllNames, ControlledResources: []string{}}}, want: false},
		{name: "CPU named explicitly",
			policies: []VPAContainerPolicy{{ContainerName: vpaAllNames, ControlledResources: []string{"cpu"}}}, want: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pod := ownedPod(kindDaemonSet, targetWeb)
			vpas := []VPA{{Name: vpaName, TargetKind: kindDaemonSet, TargetName: targetWeb, UpdateMode: tt.mode, Policies: tt.policies}}
			if got := LimitManagingVPA(pod, ResizableContainers(pod), vpas) != ""; got != tt.want {
				t.Errorf("LimitManagingVPA() matched = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestLimitManagingVPAResolvesTargetRef(t *testing.T) {
	tests := []struct {
		name               string
		ownerKind, owner   string
		targetKind, target string
		want               bool
	}{
		{"Deployment through its ReplicaSet", kindReplicaSet, rsWeb, kindDeployment, targetWeb, true},
		{"a custom rollout through its ReplicaSet", kindReplicaSet, rsWeb, "Rollout", targetWeb, true},
		{"a hyphenated Deployment", kindReplicaSet, "web-api-6d4cf56db6", kindDeployment, "web-api", true},
		{"a Deployment whose name only prefixes the ReplicaSet's", kindReplicaSet, "web-api-6d4cf56db6", kindDeployment, targetWeb, false},
		{"a bare ReplicaSet", kindReplicaSet, targetWeb, kindReplicaSet, targetWeb, true},
		{"a ReplicaSet target does not match by parent name", kindReplicaSet, rsWeb, kindReplicaSet, targetWeb, false},
		{"a StatefulSet", kindStatefulSet, targetWeb, kindStatefulSet, targetWeb, true},
		{"DaemonSet", kindDaemonSet, targetWeb, kindDaemonSet, targetWeb, true},
		{"a custom controller owning pods directly", "CloneSet", targetWeb, "CloneSet", targetWeb, true},
		{"CronJob through its Job", kindJob, "web-29384756", kindCronJob, targetWeb, true},
		{"a bare Job", kindJob, targetWeb, kindJob, targetWeb, true},
		{"same name, another kind", kindStatefulSet, targetWeb, kindDeployment, targetWeb, false},
		{"same kind, another name", kindStatefulSet, "web-0", kindStatefulSet, targetWeb, false},
		{"a StatefulSet does not match by parent name", kindStatefulSet, "web-0", kindDeployment, targetWeb, false},
		{"an owner with no hyphen has no parent", kindReplicaSet, "web", kindDeployment, targetWeb, false},
		{"a pod with no owner", "", "", kindDeployment, targetWeb, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pod := ownedPod(tt.ownerKind, tt.owner)
			vpas := []VPA{{Name: vpaName, TargetKind: tt.targetKind, TargetName: tt.target}}
			if got := LimitManagingVPA(pod, ResizableContainers(pod), vpas) != ""; got != tt.want {
				t.Errorf("LimitManagingVPA() matched = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestLimitManagingVPAIgnoresAnOwnerThatIsNotTheController(t *testing.T) {
	pod := burstablePod("p", container(cApp, 100, 0))
	pod.OwnerReferences = []metav1.OwnerReference{{Kind: kindDaemonSet, Name: targetWeb}}
	vpas := []VPA{{Name: vpaName, TargetKind: kindDaemonSet, TargetName: targetWeb}}
	if got := LimitManagingVPA(pod, ResizableContainers(pod), vpas); got != "" {
		t.Errorf("LimitManagingVPA() = %q, want none", got)
	}
}

func TestLimitManagingVPANamesAnyVPAThatWrites(t *testing.T) {
	pod := ownedPod(kindDaemonSet, targetWeb)
	vpas := []VPA{
		{Name: "older-requests-only", TargetKind: kindDaemonSet, TargetName: targetWeb,
			Policies: []VPAContainerPolicy{{ContainerName: vpaAllNames, ControlledValues: vpaRequestsOnly}}},
		{Name: "unrelated", TargetKind: kindDaemonSet, TargetName: "another-target"},
		{Name: vpaName, TargetKind: kindDaemonSet, TargetName: targetWeb},
	}
	if got := LimitManagingVPA(pod, ResizableContainers(pod), vpas); got != vpaName {
		t.Errorf("LimitManagingVPA() = %q, want %q", got, vpaName)
	}
}
