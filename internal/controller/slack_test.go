package controller

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	resourcehelper "k8s.io/component-helpers/resource"
	"sigs.k8s.io/controller-runtime/pkg/event"

	"github.com/karlkfi/kube-headroom/internal/eligibility"
	"github.com/karlkfi/kube-headroom/internal/policy"
)

func cpuList(milli int64) corev1.ResourceList {
	return corev1.ResourceList{corev1.ResourceCPU: *resource.NewMilliQuantity(milli, resource.DecimalSI)}
}

func cpuContainer(name string, milli int64) corev1.Container {
	return corev1.Container{Name: name, Resources: corev1.ResourceRequirements{Requests: cpuList(milli)}}
}

func sidecarContainer(name string, milli int64) corev1.Container {
	c := cpuContainer(name, milli)
	always := corev1.ContainerRestartPolicyAlways
	c.RestartPolicy = &always
	return c
}

func slackPod(name string, spec corev1.PodSpec) *corev1.Pod {
	return &corev1.Pod{
		Name: name, Namespace: nsA, Spec: spec,
		Status: corev1.PodStatus{Phase: corev1.PodRunning, QOSClass: corev1.PodQOSBurstable},
	}
}

func inputFor(pod *corev1.Pod, managed bool) policy.PodInput {
	return buildPodInput(pod, eligibility.ResizableContainers(pod), managed)
}

// The want values are worked by hand from the KEP-753 formula so the table does
// not lean on the helper it checks; the helper call then proves they are what
// the scheduler computes, with the options of framework.PodInfo.CalculateResource.
func TestSlackBooksWhatTheSchedulerBooks(t *testing.T) {
	apps := []corev1.Container{cpuContainer(cApp, 200)}
	resizing := slackPod("k", corev1.PodSpec{Containers: apps})
	resizing.Status.ContainerStatuses = []corev1.ContainerStatus{{Name: cApp, AllocatedResources: cpuList(500)}}
	tests := []struct {
		name string
		pod  *corev1.Pod
		want int64
	}{
		{"app only", slackPod("a", corev1.PodSpec{Containers: apps}), 200},
		{
			"init below the app sum is invisible",
			slackPod("b", corev1.PodSpec{Containers: apps, InitContainers: []corev1.Container{cpuContainer("i", 100)}}),
			200,
		},
		{
			"init above the app sum wins",
			slackPod("c", corev1.PodSpec{Containers: apps, InitContainers: []corev1.Container{cpuContainer("i", 1000)}}),
			1000,
		},
		{
			"init containers take the max, not the sum",
			slackPod("d", corev1.PodSpec{Containers: apps, InitContainers: []corev1.Container{cpuContainer("i", 1000), cpuContainer("j", 700)}}),
			1000,
		},
		{
			"overhead is added",
			slackPod("e", corev1.PodSpec{Containers: apps, Overhead: cpuList(250)}),
			450,
		},
		{
			"overhead is added on top of the init max",
			slackPod("f", corev1.PodSpec{Containers: apps, InitContainers: []corev1.Container{cpuContainer("i", 1000)}, Overhead: cpuList(250)}),
			1250,
		},
		{
			"sidecar running before the init stacks on it",
			slackPod("g", corev1.PodSpec{Containers: apps, InitContainers: []corev1.Container{sidecarContainer("s", 300), cpuContainer("i", 1000)}}),
			1300,
		},
		{
			"sidecar starting after the init does not stack on it",
			slackPod("h", corev1.PodSpec{Containers: apps, InitContainers: []corev1.Container{cpuContainer("i", 1000), sidecarContainer("s", 300)}}),
			1000,
		},
		{
			"sidecar counts in the steady state once the init is small",
			slackPod("i", corev1.PodSpec{Containers: apps, InitContainers: []corev1.Container{cpuContainer("i", 100), sidecarContainer("s", 300)}}),
			500,
		},
		{
			"pod-level request replaces the container aggregate",
			slackPod("j", corev1.PodSpec{Containers: apps, Resources: &corev1.ResourceRequirements{Requests: cpuList(1000)}}),
			1000,
		},
		{"a shrink still allocated at the old size books the old size", resizing, 500},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			reqs := resourcehelper.PodRequests(tc.pod, resourcehelper.PodResourcesOptions{
				UseStatusResources: true,
				InPlacePodLevelResourcesVerticalScalingEnabled: true,
			})
			if got := reqs.Cpu().MilliValue(); got != tc.want {
				t.Fatalf("table is wrong: scheduler books %dm, want %dm", got, tc.want)
			}
			stats, _ := policy.Compute(16000, []policy.PodInput{inputFor(tc.pod, false)}, policy.DefaultConfig())
			if stats.TotalRequestsMilli != tc.want {
				t.Errorf("slack books %dm, scheduler books %dm", stats.TotalRequestsMilli, tc.want)
			}
		})
	}
}

// A node the scheduler treats as full must collapse managed limits to request.
func TestSchedulerFullNodeHasNoSlack(t *testing.T) {
	migrator := slackPod("migrator", corev1.PodSpec{
		Containers:     []corev1.Container{cpuContainer(cApp, 200)},
		InitContainers: []corev1.Container{cpuContainer("migrate", 2000)},
	})
	sandboxed := slackPod("sandboxed", corev1.PodSpec{
		Containers: []corev1.Container{cpuContainer(cApp, 750)},
		Overhead:   cpuList(250),
	})
	managed := slackPod("managed", corev1.PodSpec{Containers: []corev1.Container{cpuContainer(cApp, 1000)}})

	stats, decisions := policy.Compute(4000, []policy.PodInput{
		inputFor(migrator, false), inputFor(sandboxed, false), inputFor(managed, true),
	}, policy.DefaultConfig())

	if stats.SlackMilli != 0 {
		t.Errorf("slack = %dm on a node the scheduler has full", stats.SlackMilli)
	}
	if got := decisions[0].TargetLimitMilli; got != 1000 {
		t.Errorf("managed target = %dm, want its 1000m request", got)
	}
}

// Booking only moves slack: a managed pod's share and per-container split stay
// on what its resizable containers request.
func TestBookedExcessDoesNotChangeDistributionBasis(t *testing.T) {
	pod := slackPod("p", corev1.PodSpec{
		Containers:     []corev1.Container{cpuContainer(cApp, 300), cpuContainer("worker", 200)},
		InitContainers: []corev1.Container{cpuContainer("i", 1000)},
		Overhead:       cpuList(250),
	})
	rcs := eligibility.ResizableContainers(pod)

	stats, decisions := policy.Compute(4000, []policy.PodInput{inputFor(pod, true)}, policy.DefaultConfig())

	if stats.TotalRequestsMilli != 1250 || stats.ManagedRequestsMilli != 500 || stats.SlackMilli != 2750 {
		t.Fatalf("total=%dm managed=%dm slack=%dm, want 1250m, 500m, 2750m",
			stats.TotalRequestsMilli, stats.ManagedRequestsMilli, stats.SlackMilli)
	}
	target := decisions[0].TargetLimitMilli
	if target != 3250 {
		t.Errorf("target = %dm, want request 500m + slack 2750m", target)
	}
	split := splitLimit(target, rcs)
	if split[cApp]+split["worker"] != target {
		t.Errorf("split %v does not sum to target %dm", split, target)
	}
}

// A kubelet actuating a resize moves the booking through status alone, with no
// spec change, and that has to reach the node's slack.
func TestPodUpdateRelevantOnBookingChange(t *testing.T) {
	spec := corev1.PodSpec{Containers: []corev1.Container{cpuContainer(cApp, 200)}}
	before := slackPod("p", spec)
	before.Status.ContainerStatuses = []corev1.ContainerStatus{{Name: cApp, AllocatedResources: cpuList(500)}}

	settled := before.DeepCopy()
	settled.Status.ContainerStatuses[0].AllocatedResources = cpuList(200)
	if !podUpdateRelevant(event.UpdateEvent{ObjectOld: before, ObjectNew: settled}) {
		t.Error("a status-only booking change was filtered out")
	}

	restarted := before.DeepCopy()
	restarted.Status.ContainerStatuses[0].RestartCount = 1
	if podUpdateRelevant(event.UpdateEvent{ObjectOld: before, ObjectNew: restarted}) {
		t.Error("a status change that leaves the booking alone passed the filter")
	}
}
