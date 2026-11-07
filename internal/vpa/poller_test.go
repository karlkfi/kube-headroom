package vpa

import (
	"context"
	"errors"
	"maps"
	"strings"
	"testing"

	"github.com/go-logr/logr/funcr"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
)

const (
	vpaGroup = "autoscaling.k8s.io"
	modeOff  = "Off"
	web      = "web"
	nsA      = "team-a"
	nsB      = "team-b"
	stateKey = `"state"="`
)

// stubReader answers List with whatever the spec last set.
type stubReader struct {
	client.Reader
	items []unstructured.Unstructured
	err   error
	lists int
}

func (s *stubReader) List(_ context.Context, list client.ObjectList, _ ...client.ListOption) error {
	s.lists++
	if s.err != nil {
		return s.err
	}
	list.(*unstructured.UnstructuredList).Items = s.items
	return nil
}

func vpaObject(ns, name string, spec map[string]any) unstructured.Unstructured {
	return unstructured.Unstructured{Object: map[string]any{
		"apiVersion": vpaGroup + "/v1",
		"kind":       "VerticalPodAutoscaler",
		"metadata":   map[string]any{"namespace": ns, "name": name},
		"spec":       spec,
	}}
}

func targetSpec(name string) map[string]any {
	return map[string]any{"targetRef": map[string]any{"apiVersion": "apps/v1", "kind": "Deployment", "name": name}}
}

// drain returns the namespaces waiting on the Changes channel.
func drain(p *Poller) []string {
	var out []string
	for {
		select {
		case e := <-p.Changes():
			out = append(out, e.Object.GetName())
		default:
			return out
		}
	}
}

// logContext captures the lines Refresh logs.
func logContext() (context.Context, *[]string) {
	var lines []string
	sink := funcr.New(func(prefix, args string) { lines = append(lines, prefix+" "+args) }, funcr.Options{})
	return logf.IntoContext(context.Background(), sink), &lines
}

var (
	errAbsentKind = &meta.NoKindMatchError{GroupKind: schema.GroupKind{Group: vpaGroup, Kind: "VerticalPodAutoscaler"}}
	errAbsentCRD  = apierrors.NewNotFound(schema.GroupResource{Group: vpaGroup, Resource: "verticalpodautoscalers"}, "")
	errForbidden  = apierrors.NewForbidden(schema.GroupResource{Group: vpaGroup, Resource: "verticalpodautoscalers"}, "", errors.New("rbac"))
)

func TestRefreshParsesWhatTheRuleReads(t *testing.T) {
	spec := targetSpec(web)
	spec["updatePolicy"] = map[string]any{"updateMode": modeOff}
	spec["resourcePolicy"] = map[string]any{"containerPolicies": []any{map[string]any{
		"containerName":       "*",
		"mode":                "Auto",
		"controlledValues":    "RequestsOnly",
		"controlledResources": []any{"cpu"},
		"minAllowed":          map[string]any{"cpu": "100m"},
	}}}
	p := NewPoller(&stubReader{items: []unstructured.Unstructured{vpaObject(nsA, web, spec)}})
	p.Refresh(context.Background())

	got := p.ForNamespace(nsA)
	if len(got) != 1 {
		t.Fatalf("ForNamespace = %+v, want one VPA", got)
	}
	v := got[0]
	if v.Name != web || v.TargetKind != "Deployment" || v.TargetName != web || v.UpdateMode != modeOff {
		t.Errorf("parsed %+v", v)
	}
	if len(v.Policies) != 1 || v.Policies[0].ContainerName != "*" || v.Policies[0].Mode != "Auto" ||
		v.Policies[0].ControlledValues != "RequestsOnly" || len(v.Policies[0].ControlledResources) != 1 {
		t.Errorf("parsed policies %+v", v.Policies)
	}
	if other := p.ForNamespace(nsB); len(other) != 0 {
		t.Errorf("ForNamespace(%s) = %+v, want none", nsB, other)
	}
}

// An empty controlledResources turns scaling off for every resource, where an absent
// one means the default of CPU and memory, so the parse must keep them apart.
func TestRefreshKeepsEmptyControlledResourcesApartFromAbsent(t *testing.T) {
	policy := func(extra map[string]any) map[string]any {
		spec := targetSpec(web)
		entry := map[string]any{"containerName": "*"}
		maps.Copy(entry, extra)
		spec["resourcePolicy"] = map[string]any{"containerPolicies": []any{entry}}
		return spec
	}
	p := NewPoller(&stubReader{items: []unstructured.Unstructured{
		vpaObject(nsA, "absent", policy(nil)),
		vpaObject(nsA, "empty", policy(map[string]any{"controlledResources": []any{}})),
	}})
	p.Refresh(context.Background())

	got := p.ForNamespace(nsA)
	if got[0].Policies[0].ControlledResources != nil {
		t.Errorf("absent controlledResources parsed as %#v, want nil", got[0].Policies[0].ControlledResources)
	}
	if got[1].Policies[0].ControlledResources == nil {
		t.Error("empty controlledResources parsed as nil, which reads as the default")
	}
}

func TestSyncedOnlyAfterTheFirstList(t *testing.T) {
	tests := []struct {
		name string
		err  error
	}{
		{"a list", nil},
		{"an absent kind", errAbsentKind},
		{"a removed CRD", errAbsentCRD},
		{"a refusal", errForbidden},
		{"a failure", errors.New("boom")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := NewPoller(&stubReader{err: tt.err})
			if p.Synced() {
				t.Fatal("Synced before any list")
			}
			p.Refresh(context.Background())
			if !p.Synced() {
				t.Fatal("not Synced after the first list")
			}
		})
	}
}

func TestANilPollerSeesNothingAndIsSynced(t *testing.T) {
	var p *Poller
	if !p.Synced() || p.ForNamespace(nsA) != nil {
		t.Error("a nil Poller should be synced and see no VPAs")
	}
}

func TestRefreshLogsEachStateOnce(t *testing.T) {
	r := &stubReader{err: errAbsentKind}
	p := NewPoller(r)
	ctx, lines := logContext()

	for range 5 {
		p.Refresh(ctx)
	}
	if len(*lines) != 1 || !strings.Contains((*lines)[0], stateKey+stateAbsent) {
		t.Fatalf("after 5 lists of an absent API, logged %q, want one line", *lines)
	}

	r.err = errAbsentCRD // a removed CRD reads differently and is the same state
	p.Refresh(ctx)
	if len(*lines) != 1 {
		t.Fatalf("a second spelling of absent logged again: %q", *lines)
	}

	r.err = nil
	p.Refresh(ctx)
	p.Refresh(ctx)
	if len(*lines) != 2 || !strings.Contains((*lines)[1], stateKey+stateServed) {
		t.Fatalf("after the API appeared, logged %q", *lines)
	}

	r.err = errForbidden
	p.Refresh(ctx)
	r.err = errors.New("boom")
	p.Refresh(ctx)
	p.Refresh(ctx)
	if len(*lines) != 4 || !strings.Contains((*lines)[2], stateKey+stateForbidden) || !strings.Contains((*lines)[3], "boom") {
		t.Fatalf("logged %q", *lines)
	}
}

func TestRefreshKeepsTheLastListThroughAFailure(t *testing.T) {
	r := &stubReader{items: []unstructured.Unstructured{vpaObject(nsA, web, targetSpec(web))}}
	p := NewPoller(r)
	p.Refresh(context.Background())
	drain(p)

	r.err = errors.New("apiserver unavailable")
	p.Refresh(context.Background())
	if len(p.ForNamespace(nsA)) != 1 {
		t.Error("a failed list dropped the last view, which would resume managing pods under a VPA")
	}
	if got := drain(p); len(got) != 0 {
		t.Errorf("a failed list announced changes: %v", got)
	}

	r.err = nil
	p.Refresh(context.Background())
	if got := drain(p); len(got) != 0 {
		t.Errorf("recovering to the same list announced changes: %v", got)
	}
}

func TestRefreshEmptiesWhenTheAPIGoes(t *testing.T) {
	for _, err := range []error{errAbsentKind, errAbsentCRD, errForbidden} {
		r := &stubReader{items: []unstructured.Unstructured{vpaObject(nsA, web, targetSpec(web))}}
		p := NewPoller(r)
		p.Refresh(context.Background())
		drain(p)

		r.err = err
		p.Refresh(context.Background())
		if len(p.ForNamespace(nsA)) != 0 {
			t.Errorf("%v: still sees the VPA", err)
		}
		if got := drain(p); len(got) != 1 || got[0] != nsA {
			t.Errorf("%v: announced %v, want %s", err, got, nsA)
		}
	}
}

func TestChangesNamesOnlyNamespacesTheRuleWouldReadDifferently(t *testing.T) {
	r := &stubReader{}
	p := NewPoller(r)
	p.Refresh(context.Background())
	if got := drain(p); len(got) != 0 {
		t.Fatalf("an empty first list announced %v", got)
	}

	first := vpaObject(nsA, web, targetSpec(web))
	r.items = []unstructured.Unstructured{first, vpaObject(nsB, "api", targetSpec("api"))}
	p.Refresh(context.Background())
	if got := drain(p); len(got) != 2 {
		t.Fatalf("two new VPAs announced %v", got)
	}

	// Status and metadata churn, as the recommender writes constantly.
	churned := first.DeepCopy()
	churned.Object["status"] = map[string]any{"recommendation": map[string]any{}}
	churned.SetResourceVersion("99")
	r.items[0] = *churned
	p.Refresh(context.Background())
	if got := drain(p); len(got) != 0 {
		t.Fatalf("status churn announced %v", got)
	}

	changed := targetSpec(web)
	changed["updatePolicy"] = map[string]any{"updateMode": modeOff}
	r.items[0] = vpaObject(nsA, web, changed)
	p.Refresh(context.Background())
	if got := drain(p); len(got) != 1 || got[0] != nsA {
		t.Fatalf("a mode change announced %v, want %s", got, nsA)
	}

	r.items = r.items[:1]
	p.Refresh(context.Background())
	if got := drain(p); len(got) != 1 || got[0] != nsB {
		t.Fatalf("deleting a namespace's last VPA announced %v, want %s", got, nsB)
	}
}

// A replica that has not won the lease has nothing draining Changes, and its list has
// to keep moving because it still serves the webhook.
func TestRefreshNeverBlocksOnAnUndrainedChannel(t *testing.T) {
	r := &stubReader{}
	p := NewPoller(r)
	for i := range changeBuffer + 2 {
		name := string(rune('a'+i%26)) + strings.Repeat("x", i)
		r.items = []unstructured.Unstructured{vpaObject(name, web, targetSpec(web))}
		p.Refresh(context.Background())
	}
	if len(p.Changes()) != changeBuffer {
		t.Errorf("Changes holds %d, want it full at %d", len(p.Changes()), changeBuffer)
	}
}

func TestStartStopsWithItsContext(t *testing.T) {
	r := &stubReader{}
	p := NewPoller(r)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := p.Start(ctx); err != nil {
		t.Errorf("Start = %v", err)
	}
	if p.NeedLeaderElection() {
		t.Error("the Poller must run on replicas that only serve the webhook")
	}
}
