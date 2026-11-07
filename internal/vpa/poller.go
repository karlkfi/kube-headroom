// Package vpa keeps a cluster-wide view of VerticalPodAutoscalers for the §6.3
// exclusion check. VPA is an optional CRD, so a watch does not fit: a controller
// watching a kind the apiserver does not serve logs an error every ten seconds and
// fails its cache sync after two minutes. Listing on a period costs a request per
// page, and treats a CRD installed or removed later like any other change.
package vpa

import (
	"context"
	"fmt"
	"reflect"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/karlkfi/kube-headroom/internal/eligibility"
)

const (
	defaultPeriod = 30 * time.Second
	listTimeout   = 10 * time.Second
	// pageSize bounds what one list holds in memory. An item decodes to about 10KB
	// of maps, status included, and only the spec fields in eligibility.VPA are kept.
	pageSize = 500
	// changeBuffer holds namespace changes while no controller drains them, as on a
	// replica that has not won the lease. Its first reconciles cover every node anyway.
	changeBuffer = 1024

	stateServed    = "served"
	stateAbsent    = "absent"
	stateForbidden = "forbidden"
	stateFailing   = "failing"
)

var listKind = schema.GroupVersionKind{
	Group: "autoscaling.k8s.io", Version: "v1", Kind: "VerticalPodAutoscalerList",
}

// Poller lists VerticalPodAutoscalers on a period and serves them from memory, so
// neither a reconcile nor an admission request reads the API per pod. Every replica
// runs it, because every replica serves the webhook.
type Poller struct {
	// Period between lists; defaults to 30s. Tests shorten it.
	Period time.Duration

	reader  client.Reader
	changes chan event.GenericEvent

	mu     sync.RWMutex
	byNS   map[string][]eligibility.VPA
	synced bool
	state  string
}

// NewPoller returns a Poller reading through reader, normally the manager's APIReader:
// a caching reader would watch the kind this package exists to avoid watching.
func NewPoller(reader client.Reader) *Poller {
	return &Poller{reader: reader, changes: make(chan event.GenericEvent, changeBuffer)}
}

// NeedLeaderElection keeps the Poller running on replicas that only serve the webhook.
func (p *Poller) NeedLeaderElection() bool { return false }

// Start lists at once and then every Period until ctx ends.
func (p *Poller) Start(ctx context.Context) error {
	period := p.Period
	if period <= 0 {
		period = defaultPeriod
	}
	p.Refresh(ctx)
	ticker := time.NewTicker(period)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			p.Refresh(ctx)
		}
	}
}

// Synced reports whether the first list has finished, whatever it found. A nil
// Poller is synced, which leaves callers built without one managing every pod.
func (p *Poller) Synced() bool {
	if p == nil {
		return true
	}
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.synced
}

// ForNamespace returns the namespace's VPAs. Callers must not modify the result.
func (p *Poller) ForNamespace(namespace string) []eligibility.VPA {
	if p == nil {
		return nil
	}
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.byNS[namespace]
}

// Changes delivers one event, naming the namespace, per namespace whose VPAs changed
// in a way the eligibility rule reads. Status updates never send one.
func (p *Poller) Changes() <-chan event.GenericEvent { return p.changes }

// Refresh lists once. An API that is absent or forbidden means no VPA can be seen, so
// the view empties; any other failure keeps the last view. Each state is logged once,
// on the way in.
func (p *Poller) Refresh(ctx context.Context) {
	byNS, err := p.list(ctx)
	if ctx.Err() != nil {
		return // shutting down: the failure says nothing about the API
	}

	state := stateServed
	switch {
	case err == nil:
	case meta.IsNoMatchError(err) || apierrors.IsNotFound(err):
		state = stateAbsent
	case apierrors.IsForbidden(err):
		state = stateForbidden
	default:
		state = stateFailing
	}

	p.mu.Lock()
	previous, previousState := p.byNS, p.state
	if state != stateFailing {
		p.byNS = byNS
	}
	p.synced, p.state = true, state
	current := p.byNS
	p.mu.Unlock()

	if state != previousState {
		log := logf.FromContext(ctx).WithName("vpa-poller")
		switch state {
		case stateFailing:
			log.Error(err, "listing VerticalPodAutoscalers failed; keeping the last list")
		default:
			log.Info("VerticalPodAutoscaler API state changed", "state", state)
		}
	}
	if state != stateFailing {
		p.notify(previous, current)
	}
}

func (p *Poller) notify(previous, current map[string][]eligibility.VPA) {
	send := func(namespace string) {
		select {
		case p.changes <- event.GenericEvent{Object: &corev1.Namespace{Name: namespace}}:
		default:
		}
	}
	for namespace, vpas := range current {
		if !reflect.DeepEqual(previous[namespace], vpas) {
			send(namespace)
		}
	}
	for namespace := range previous {
		if _, ok := current[namespace]; !ok {
			send(namespace)
		}
	}
}

// list reads every page, or returns an error and nothing: half a list would read as
// VPAs having been deleted.
func (p *Poller) list(ctx context.Context) (map[string][]eligibility.VPA, error) {
	byNS := map[string][]eligibility.VPA{}
	token := ""
	for {
		page := &unstructured.UnstructuredList{}
		page.SetGroupVersionKind(listKind)
		pageCtx, cancel := context.WithTimeout(ctx, listTimeout)
		err := p.reader.List(pageCtx, page, client.Limit(pageSize), client.Continue(token))
		cancel()
		if err != nil {
			return nil, err
		}
		if err := parse(page, byNS); err != nil {
			return nil, err
		}
		if token = page.GetContinue(); token == "" {
			return byNS, nil
		}
	}
}

func parse(list *unstructured.UnstructuredList, byNS map[string][]eligibility.VPA) error {
	for i := range list.Items {
		item := &list.Items[i]
		var spec struct {
			TargetRef struct {
				Kind string `json:"kind"`
				Name string `json:"name"`
			} `json:"targetRef"`
			UpdatePolicy struct {
				UpdateMode string `json:"updateMode"`
			} `json:"updatePolicy"`
			ResourcePolicy struct {
				ContainerPolicies []eligibility.VPAContainerPolicy `json:"containerPolicies"`
			} `json:"resourcePolicy"`
		}
		raw, _, err := unstructured.NestedMap(item.Object, "spec")
		if err != nil {
			return fmt.Errorf("read spec of %s/%s: %w", item.GetNamespace(), item.GetName(), err)
		}
		if err := runtime.DefaultUnstructuredConverter.FromUnstructured(raw, &spec); err != nil {
			return fmt.Errorf("decode spec of %s/%s: %w", item.GetNamespace(), item.GetName(), err)
		}
		byNS[item.GetNamespace()] = append(byNS[item.GetNamespace()], eligibility.VPA{
			Name:       item.GetName(),
			TargetKind: spec.TargetRef.Kind,
			TargetName: spec.TargetRef.Name,
			UpdateMode: spec.UpdatePolicy.UpdateMode,
			Policies:   spec.ResourcePolicy.ContainerPolicies,
		})
	}
	return nil
}
