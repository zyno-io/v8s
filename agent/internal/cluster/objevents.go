package cluster

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/zyno-io/v8s/agent/internal/ingest"
	"github.com/zyno-io/v8s/agent/internal/store"
	"github.com/zyno-io/v8s/agent/internal/view"
	"github.com/zyno-io/v8s/agent/pkg/protocol"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/client-go/tools/cache"
)

const errNoEventsForThisKind = "this kind has no events"

// ObjectEventsWait bounds how long OpenObjectEvents waits for the first list.
var ObjectEventsWait = 15 * time.Second

var (
	ErrClusterNotSelected = errors.New("cluster is not selected")
	ErrEventsTimeout      = errors.New("timed out loading the object's events")
	ErrEventsUnsupported  = errors.New("this cluster backend cannot list events of one namespace")
	errEventsWatchStopped = errors.New("the object's events watch was stopped")
)

// ObjectEvents identifies the events of one object: the namespaced, field-selected watch that lists them.
//
// Events of a namespaced object live in its namespace; events of a cluster-scoped object live in "default"
// (docs/performance.md §5). The selector uses kind, name and namespace, never uid: the kubelet records Node
// events with the node name as uid. Known limitation: a namespaced kind in "default" and a cluster-scoped kind
// with the same Kind and name share one view filter (Kind/Name) and so show each other's events.
type ObjectEvents struct {
	Cluster, Kind, Name string
	InvolvedNS          string // "" for cluster-scoped objects
	EventNS             string // InvolvedNS, or "default"
	key                 string // ObjectEventsGVR|EventNS|Kind|InvolvedNS|Name
}

func newObjectEvents(cluster, kind, ns, name string) *ObjectEvents {
	oe := &ObjectEvents{Cluster: cluster, Kind: kind, Name: name, InvolvedNS: ns, EventNS: ns}
	if ns == "" {
		oe.EventNS = metav1.NamespaceDefault
	}
	oe.key = strings.Join([]string{ingest.ObjectEventsGVR, oe.EventNS, kind, ns, name}, "|")
	return oe
}

// Key is the watch's key in the runner (not a GVR).
func (o *ObjectEvents) Key() string { return o.key }

// Selector is the field selector of the events watch.
func (o *ObjectEvents) Selector() string {
	return fields.Set{
		"involvedObject.kind":      o.Kind,
		"involvedObject.name":      o.Name,
		"involvedObject.namespace": o.InvolvedNS,
	}.AsSelector().String()
}

// Rewrite turns an involved query into a plain filtered view over the shared object-events store.
func (o *ObjectEvents) Rewrite(q protocol.ViewQuery) protocol.ViewQuery {
	q.GVR = ingest.ObjectEventsGVR
	q.Involved = nil
	q.Clusters = []string{o.Cluster}
	q.Namespaces = []string{o.EventNS}
	q.Filters = append(slices.Clip(q.Filters), protocol.Filter{Col: "object", Op: "eq", Value: o.Kind + "/" + o.Name})
	return q
}

func badReq(msg string) error { return &view.BadQueryError{Msg: msg} }

// OpenObjectEvents starts (or joins) the events watch of one object and waits for its first list. On success the
// caller owns the returned scope (Close releases it; the watch then lingers EventsLinger). On any failure the
// scope is closed and an unreferenced watch is discarded, so a failing watch does not retry during the linger and
// a reopen after a fix starts fresh.
func (m *Manager) OpenObjectEvents(ctx context.Context, ref protocol.ObjectRef) (*Scope, *ObjectEvents, error) {
	if ref.Name == "" {
		return nil, nil, badReq("involved: name is required")
	}
	var r *runner
	for _, x := range m.selectedRunners() {
		if x.id == ref.Cluster {
			r = x
		}
	}
	if r == nil {
		return nil, nil, ErrClusterNotSelected
	}
	kctx, kcancel := context.WithTimeout(ctx, ObjectEventsWait)
	kind, namespaced, err := r.kindOf(kctx, ref.GVR)
	kcancel()
	if errors.Is(err, context.DeadlineExceeded) && ctx.Err() == nil {
		err = ErrEventsTimeout
	}
	if err != nil {
		return nil, nil, err
	}
	ns := ref.Namespace
	if namespaced && ns == "" {
		return nil, nil, badReq("involved: namespace is required for " + ref.GVR)
	}
	if !namespaced {
		ns = ""
	}
	oe := newObjectEvents(ref.Cluster, kind, ns, ref.Name)
	s := &Scope{m: m, gvr: ingest.ObjectEventsGVR, clusters: []string{ref.Cluster}, held: map[string]func(){}, ev: oe}
	m.mu.Lock()
	m.scopes[s] = struct{}{}
	m.mu.Unlock()
	fail := func(err error) (*Scope, *ObjectEvents, error) {
		s.Close()
		r.discardEvents(oe.key)
		return nil, nil, err
	}
	m.reconcile(s)
	m.reconcileMu.Lock()
	held, serr := len(s.held) > 0, s.err
	m.reconcileMu.Unlock()
	if !held {
		if serr == nil {
			serr = ErrNotServed
		}
		return fail(serr)
	}
	r.mu.Lock()
	w := r.watches[oe.key]
	r.mu.Unlock()
	if w == nil {
		return fail(errEventsWatchStopped)
	}
	timeout := time.NewTimer(ObjectEventsWait)
	defer timeout.Stop()
	tick := time.NewTicker(50 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case <-w.synced:
			return s, oe, nil
		case <-w.done:
			return fail(errEventsWatchStopped)
		case <-ctx.Done():
			return fail(ctx.Err())
		case <-timeout.C:
			return fail(ErrEventsTimeout)
		case <-tick.C:
			if err := w.lastErr(); err != nil && (apierrors.IsForbidden(err) || apierrors.IsUnauthorized(err) || apierrors.IsNotFound(err)) {
				return fail(err)
			}
		}
	}
}

// kindOf returns the Kind and scope of a resource from the agent's own knowledge (never the client's).
func (r *runner) kindOf(ctx context.Context, gvr string) (kind string, namespaced bool, err error) {
	if mod := ingest.Lookup(gvr); mod != nil {
		if mod.Virtual || mod.Synthetic || gvr == ingest.ObjectEventsGVR {
			return "", false, badReq(errNoEventsForThisKind)
		}
		return mod.Kind, mod.Namespaced, nil
	}
	for {
		if d := r.disc.Load(); d != nil {
			res := d.byG[gvr]
			if res == nil {
				return "", false, apierrors.NewNotFound(schemaGR(gvr), gvr)
			}
			return res.Kind, res.Namespaced, nil
		}
		select {
		case <-ctx.Done():
			return "", false, ctx.Err()
		case <-time.After(25 * time.Millisecond):
		}
	}
}

// discardEvents stops the events watch under key when nothing references it.
func (r *runner) discardEvents(key string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if w := r.watches[key]; w != nil && w.part != nil && w.refs == 0 {
		r.stopLocked(w)
	}
}

// acquireEvents references the object's events watch, starting it when needed.
func (r *runner) acquireEvents(oe *ObjectEvents) (func(), error) {
	return r.acquireKey(oe.key, r.m.EventsLinger, func() (*watchEntry, error) {
		if err := r.makeRoomLocked(); err != nil {
			return nil, err
		}
		return r.startEventsLocked(oe)
	})
}

// oevStoreLocked returns the shared object-events store, creating and attaching it on first use.
func (r *runner) oevStoreLocked() *store.Store {
	if r.oev == nil {
		r.oev = store.New()
		if r.attached {
			r.m.eng.AttachKind(r.id, ingest.ObjectEventsGVR, r.oev)
		}
	}
	return r.oev
}

// startEventsLocked starts one object's events LIST+WATCH. Caller holds r.mu. It touches neither the problems
// engine nor the cluster phase, and its rows never reach the tier-0 events store.
func (r *runner) startEventsLocked(oe *ObjectEvents) (*watchEntry, error) {
	mod := ingest.Lookup(ingest.EventsGVR)
	api, err := r.be.API(ingest.EventsGVR, mod)
	if err != nil {
		return nil, err
	}
	nsr, ok := api.(ingest.Namespacer)
	if !ok {
		return nil, ErrEventsUnsupported
	}
	w := &watchEntry{gvr: oe.key, done: make(chan struct{}), lastUse: time.Now(), synced: make(chan struct{})}
	w.part = store.NewPart(r.oevStoreLocked(), protocol.MaxObjectEvents, ingest.EventLastSeen)
	var once sync.Once
	w.part.OnSynced = func() { once.Do(func() { close(w.synced) }) }
	wctx, cancel := context.WithCancel(r.ctx)
	w.cancel = cancel
	sel := oe.Selector()
	lw := &ingest.ListerWatcher{
		API: nsr.InNamespace(oe.EventNS), Mod: mod,
		Tweak: func(o *metav1.ListOptions) { o.FieldSelector = sel }, NoWatchList: true,
		Hooks: ingest.Hooks{
			OnOK: func() { w.setErr(nil) },
			OnError: func(err error) {
				if wctx.Err() != nil || apierrors.IsResourceExpired(err) || apierrors.IsGone(err) {
					return
				}
				w.setErr(err)
				r.m.log.Debug("object events watch error", "cluster", r.id, "key", oe.key, "err", err.Error())
			},
		},
	}
	refl := cache.NewReflectorWithOptions(lw, &ingest.Row{}, w.part, cache.ReflectorOptions{Name: oe.key + "/" + r.id, TypeDescription: ingest.EventsGVR})
	refl.WatchListPageSize = 500
	r.watches[oe.key] = w
	r.wg.Add(1)
	go func() {
		defer r.wg.Done()
		defer close(w.done)
		refl.RunWithContext(wctx)
	}()
	return w, nil
}
