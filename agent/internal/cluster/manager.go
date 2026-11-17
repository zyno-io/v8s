package cluster

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"runtime"
	"runtime/debug"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/zyno-io/v8s/agent/internal/disco"
	"github.com/zyno-io/v8s/agent/internal/ingest"
	"github.com/zyno-io/v8s/agent/internal/settings"
	"github.com/zyno-io/v8s/agent/internal/view"
	"github.com/zyno-io/v8s/agent/pkg/protocol"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

// DeselectGrace is how long a deselected cluster keeps its ingest running (a quick reselect is free).
var DeselectGrace = 10 * time.Second

// Manager owns the selected clusters.
type Manager struct {
	src Source
	eng *view.Engine
	log *slog.Logger
	set *settings.Store

	// Linger is how long an unreferenced on-demand watch stays warm; Budget caps watches per cluster;
	// DiscoveryEvery is the periodic discovery refresh. Set before the first Select.
	Linger         time.Duration
	Budget         int
	DiscoveryEvery time.Duration

	reconcileMu sync.Mutex
	mu          sync.Mutex
	runners     map[string]*runner // includes clusters in their deselect grace period
	selected    []string
	scopes      map[*Scope]struct{}

	lmu       sync.Mutex
	listeners map[chan struct{}]struct{}
	lastKick  atomic.Int64
	ctx       context.Context
}

func NewManager(ctx context.Context, src Source, eng *view.Engine, log *slog.Logger) *Manager {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return &Manager{src: src, eng: eng, log: log, runners: map[string]*runner{}, listeners: map[chan struct{}]struct{}{},
		ctx: ctx, selected: []string{}, scopes: map[*Scope]struct{}{},
		Linger: DefaultLinger, Budget: DefaultBudget, DiscoveryEvery: DefaultDiscoveryEvery}
}

func (m *Manager) Source() Source { return m.src }

// SetSettings attaches the settings store: remembered selection, groups and linked write contexts.
func (m *Manager) SetSettings(s *settings.Store) { m.set = s }

func (m *Manager) settings() protocol.Settings {
	if m.set == nil {
		return protocol.Settings{AutoSwitchWrite: true}
	}
	return m.set.Get()
}

// Links returns the linked write context of a cluster ("" = none) and whether auto-switch is on.
func (m *Manager) Links(clusterID string) (string, bool) {
	s := m.settings()
	return s.Links[clusterID], s.AutoSwitchWrite
}

// List returns the clusters (contexts used as linked write contexts are not listed separately) and
// the remembered selection.
func (m *Manager) List() protocol.ClustersListResult {
	all := m.src.Clusters()
	s := m.settings()
	known := map[string]bool{}
	for _, c := range all {
		known[c.ID] = true
	}
	hidden := map[string]bool{}
	for from, to := range s.Links {
		if known[from] && known[to] && from != to {
			hidden[to] = true
		}
	}
	groupsOf := map[string][]string{}
	for g, ids := range s.Groups {
		for _, id := range ids {
			groupsOf[id] = append(groupsOf[id], g)
		}
	}
	cs := make([]protocol.Cluster, 0, len(all))
	for _, c := range all {
		if hidden[c.ID] {
			continue
		}
		c.Groups = groupsOf[c.ID]
		sort.Strings(c.Groups)
		if c.Groups == nil {
			c.Groups = []string{}
		}
		if w := s.Links[c.ID]; w != "" && known[w] {
			c.WriteContext = w
		}
		cs = append(cs, c)
	}
	var sel []string
	m.mu.Lock()
	sel = append(sel, m.selected...)
	m.mu.Unlock()
	if len(sel) == 0 {
		for _, id := range s.Selection {
			if known[id] && !hidden[id] {
				sel = append(sel, id)
			}
		}
	}
	if len(sel) == 0 {
		sel = m.src.DefaultSelection()
	}
	if sel == nil {
		sel = []string{}
	}
	return protocol.ClustersListResult{Clusters: cs, Selected: sel}
}

// Listen returns a channel signalled (coalesced) whenever any cluster state changes.
func (m *Manager) Listen() (<-chan struct{}, func()) {
	ch := make(chan struct{}, 1)
	m.lmu.Lock()
	m.listeners[ch] = struct{}{}
	m.lmu.Unlock()
	return ch, func() { m.lmu.Lock(); delete(m.listeners, ch); m.lmu.Unlock() }
}

func (m *Manager) kick() {
	m.lmu.Lock()
	for ch := range m.listeners {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
	m.lmu.Unlock()
}

// kickThrottled limits progress-driven signals (the receiver also throttles).
func (m *Manager) kickThrottled() {
	now := time.Now().UnixNano()
	last := m.lastKick.Load()
	if now-last > int64(50*time.Millisecond) && m.lastKick.CompareAndSwap(last, now) {
		m.kick()
	}
}

// States returns the state of every currently selected cluster.
func (m *Manager) States() protocol.ClusterStates {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := protocol.ClusterStates{States: make([]protocol.ClusterState, 0, len(m.selected))}
	for _, id := range m.selected {
		if r := m.runners[id]; r != nil {
			out.States = append(out.States, r.state())
		}
	}
	return out
}

// Select makes ids the selected set: new clusters start ingesting (tier-0 watches), removed ones stop
// after a grace period. The selection is remembered in settings.
func (m *Manager) Select(ids []string) (protocol.ClusterStates, error) {
	known := map[string]bool{}
	for _, c := range m.src.Clusters() {
		known[c.ID] = true
	}
	want := map[string]bool{}
	var list []string
	for _, id := range ids {
		if !known[id] {
			return protocol.ClusterStates{}, fmt.Errorf("unknown cluster %q", id)
		}
		if !want[id] {
			want[id] = true
			list = append(list, id)
		}
	}
	sort.Strings(list)

	m.mu.Lock()
	for _, id := range m.selected {
		if want[id] {
			continue
		}
		if r := m.runners[id]; r != nil {
			r.detachAll()
			r.stopTimer = time.AfterFunc(DeselectGrace, func() { m.stopIfDeselected(id) })
		}
	}
	for _, id := range list {
		r := m.runners[id]
		if r != nil {
			if r.stopTimer != nil { // reselected during grace
				r.stopTimer.Stop()
				r.stopTimer = nil
				r.attachAll()
			}
			continue
		}
		nr, err := m.start(id)
		if err != nil {
			m.log.Warn("cluster start failed", "cluster", id, "err", err)
			nr = m.failed(id, err)
		}
		m.runners[id] = nr
	}
	m.selected = list
	m.mu.Unlock()
	if m.set != nil {
		if _, err := m.set.Patch(map[string]any{"selection": toAny(list)}); err != nil {
			m.log.Warn("could not remember cluster selection", "err", err)
		}
	}
	m.reconcileAll()
	m.kick()
	return m.States(), nil
}

func toAny(ss []string) []any {
	out := make([]any, len(ss))
	for i, s := range ss {
		out[i] = s
	}
	return out
}

func (m *Manager) stopIfDeselected(id string) {
	m.mu.Lock()
	r := m.runners[id]
	if r == nil || r.stopTimer == nil {
		m.mu.Unlock()
		return
	}
	delete(m.runners, id)
	m.mu.Unlock()
	before := heapAlloc()
	r.stop()
	m.src.Release(id)
	m.log.Info("cluster stopped", "cluster", id)
	runtime.GC()
	maybeFree(before, heapAlloc())
}

// Running reports whether ingest for id is still alive (including during its grace period).
func (m *Manager) Running(id string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.runners[id] != nil
}

func (m *Manager) failed(id string, err error) *runner {
	ctx, cancel := context.WithCancel(m.ctx)
	r := &runner{m: m, id: id, ctx: ctx, cancel: cancel, done: make(chan struct{}), watches: map[string]*watchEntry{}, discKick: make(chan struct{}, 1)}
	close(r.done)
	phase, msg := classify(err)
	r.phase.Store(phase)
	r.message.Store(msg)
	return r
}

func (m *Manager) start(id string) (*runner, error) {
	be, err := m.src.Backend(id)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(m.ctx)
	r := &runner{m: m, id: id, be: be, ctx: ctx, cancel: cancel, done: make(chan struct{}), watches: map[string]*watchEntry{},
		discKick: make(chan struct{}, 1), attached: true}
	r.phase.Store(protocol.PhaseConnecting)
	r.message.Store("")
	r.startTier0()
	return r, nil
}

// ---- reading what is watched -----------------------------------------------------------

// WatchInfo returns the running watches of a cluster (diagnostics and tests).
func (m *Manager) WatchInfo(id string) []WatchInfo {
	m.mu.Lock()
	r := m.runners[id]
	m.mu.Unlock()
	if r == nil {
		return nil
	}
	return r.info()
}

func (m *Manager) selectedRunners() []*runner {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]*runner, 0, len(m.selected))
	for _, id := range m.selected {
		if r := m.runners[id]; r != nil && r.be != nil {
			out = append(out, r)
		}
	}
	return out
}

// Kinds returns the union of the kinds the selected clusters serve (from their discovery).
func (m *Manager) Kinds() []protocol.Kind {
	by := map[string]*protocol.Kind{}
	for _, r := range m.selectedRunners() {
		d := r.disc.Load()
		if d == nil { // discovery pending: the curated kinds are known to be served
			for _, mod := range ingest.CuratedModules() {
				addKind(by, disco.Resource{Group: mod.Group, Version: mod.Version, Resource: mod.Resource, Kind: mod.Kind,
					Namespaced: mod.Namespaced, ShortNames: mod.Short, Verbs: []string{"list", "watch"}}, r.id)
			}
			continue
		}
		for _, res := range d.list {
			if res.Listable() {
				addKind(by, res, r.id)
			}
		}
	}
	out := make([]protocol.Kind, 0, len(by))
	for _, k := range by {
		sort.Strings(k.Clusters)
		out = append(out, *k)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Kind != out[j].Kind {
			return out[i].Kind < out[j].Kind
		}
		return out[i].GVR < out[j].GVR
	})
	return out
}

func addKind(by map[string]*protocol.Kind, r disco.Resource, cluster string) {
	gvr := r.GVR()
	k := by[gvr]
	if k == nil {
		mod := ingest.Lookup(gvr)
		short := r.ShortNames
		if short == nil {
			short = []string{}
		}
		k = &protocol.Kind{GVR: gvr, Group: r.Group, Version: r.Version, Kind: r.Kind, Plural: r.Resource, ShortNames: short,
			Namespaced: r.Namespaced, Available: true, Curated: mod != nil && mod.Curated, Clusters: []string{}}
		by[gvr] = k
	}
	k.Clusters = append(k.Clusters, cluster)
}

// Resolve maps an object's apiVersion and kind to its resource using the cluster's discovery cache.
func (m *Manager) Resolve(clusterID, apiVersion, kind string) (gvr string, namespaced bool, err error) {
	m.mu.Lock()
	r := m.runners[clusterID]
	m.mu.Unlock()
	if r == nil || r.disc.Load() == nil {
		// no discovery yet: ask the backend directly
		be, e := m.src.Backend(clusterID)
		if e != nil {
			return "", false, e
		}
		res, e := be.Discover(m.ctx)
		if e != nil {
			return "", false, e
		}
		return matchKind(res, apiVersion, kind)
	}
	return matchKind(r.disc.Load().list, apiVersion, kind)
}

func matchKind(res []disco.Resource, apiVersion, kind string) (string, bool, error) {
	group, version := "", apiVersion
	if g, v, ok := strings.Cut(apiVersion, "/"); ok {
		group, version = g, v
	}
	for _, r := range res {
		if r.Kind == kind && r.Group == group && r.Version == version {
			return r.GVR(), r.Namespaced, nil
		}
	}
	for _, r := range res { // same group, other served version of the kind
		if r.Kind == kind && r.Group == group {
			return disco.Resource{Group: r.Group, Version: version, Resource: r.Resource}.GVR(), r.Namespaced, nil
		}
	}
	return "", false, apierrors.NewNotFound(schema.GroupResource{Group: group, Resource: kind}, kind)
}

// Search finds objects by name (case-insensitive substring) across every watched kind of every selected
// cluster, prefix matches first, at most limit results.
func (m *Manager) Search(text string, limit int) []protocol.SearchHit {
	text = strings.ToLower(strings.TrimSpace(text))
	if text == "" {
		return []protocol.SearchHit{}
	}
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	out := make([]protocol.SearchHit, 0, limit)
	for pass := 0; pass < 2 && len(out) < limit; pass++ {
		for _, r := range m.selectedRunners() {
			r.mu.Lock()
			ws := make([]*watchEntry, 0, len(r.watches))
			for _, w := range r.watches {
				ws = append(ws, w)
			}
			r.mu.Unlock()
			sort.Slice(ws, func(i, j int) bool { return ws[i].gvr < ws[j].gvr })
			for _, w := range ws {
				mod := ingest.Lookup(w.gvr)
				if mod == nil {
					continue
				}
				w.st.Scan(func(row *ingest.Row) bool {
					name := strings.ToLower(row.Name())
					hit := false
					if pass == 0 {
						hit = strings.HasPrefix(name, text)
					} else {
						hit = !strings.HasPrefix(name, text) && strings.Contains(name, text)
					}
					if hit {
						out = append(out, protocol.SearchHit{Cluster: r.id, GVR: w.gvr, Kind: mod.Kind, Namespace: row.GetNamespace(), Name: row.Name()})
					}
					return len(out) < limit
				})
				if len(out) >= limit {
					return out
				}
			}
		}
	}
	return out
}

// ---- view scopes -----------------------------------------------------------------------

// Scope keeps the watches of one kind alive for a view: one watch per selected cluster in scope. It
// follows selection changes and discovery updates, and releases everything on Close.
type Scope struct {
	m        *Manager
	gvr      string
	clusters []string // empty = every selected cluster
	held     map[string]func()
	closed   bool
}

// OpenScope references the kind's watch on every cluster in scope that serves it.
func (m *Manager) OpenScope(gvr string, clusters []string) *Scope {
	s := &Scope{m: m, gvr: gvr, clusters: clusters, held: map[string]func(){}}
	m.mu.Lock()
	m.scopes[s] = struct{}{}
	m.mu.Unlock()
	m.reconcile(s)
	return s
}

// Close releases the scope's watch references (the watches linger).
func (s *Scope) Close() {
	s.m.mu.Lock()
	delete(s.m.scopes, s)
	s.m.mu.Unlock()
	s.m.reconcileMu.Lock()
	defer s.m.reconcileMu.Unlock()
	s.closed = true
	for id, rel := range s.held {
		rel()
		delete(s.held, id)
	}
}

func (m *Manager) reconcileAll() {
	m.mu.Lock()
	scopes := make([]*Scope, 0, len(m.scopes))
	for s := range m.scopes {
		scopes = append(scopes, s)
	}
	m.mu.Unlock()
	for _, r := range m.selectedRunners() {
		if r.disc.Load() != nil {
			r.startInvalidation()
		}
	}
	for _, s := range scopes {
		m.reconcile(s)
	}
}

func (m *Manager) reconcile(s *Scope) {
	m.reconcileMu.Lock()
	defer m.reconcileMu.Unlock()
	if s.closed {
		return
	}
	in := map[string]*runner{}
	for _, r := range m.selectedRunners() {
		if len(s.clusters) > 0 && !contains(s.clusters, r.id) {
			continue
		}
		in[r.id] = r
	}
	for id, rel := range s.held {
		if in[id] == nil {
			rel()
			delete(s.held, id)
		}
	}
	for id, r := range in {
		if s.held[id] != nil {
			continue
		}
		if _, ok := r.serves(s.gvr); !ok {
			continue
		}
		rel, err := r.acquire(s.gvr)
		if err != nil {
			m.log.Debug("watch not started", "cluster", id, "gvr", s.gvr, "err", err.Error())
			continue
		}
		s.held[id] = rel
	}
}

func contains(ss []string, s string) bool {
	for _, x := range ss {
		if x == s {
			return true
		}
	}
	return false
}

// ErrNotServed is returned by ResolveModule when no selected cluster serves the kind.
var ErrNotServed = errors.New("no selected cluster serves this kind")

// ResolveModule returns the module of gvr, starting the kind's watch (via a temporary scope) when a
// generic kind's Table schema is not known yet, and waiting for its first columnDefinitions.
func (m *Manager) ResolveModule(ctx context.Context, gvr string, clusters []string) (*ingest.Module, error) {
	if mod := ingest.Lookup(gvr); mod != nil {
		return mod, nil
	}
	// Wait for discovery of every selected cluster in scope, so "not served" is a real answer.
	for {
		pending, serving := false, false
		for _, r := range m.selectedRunners() {
			if len(clusters) > 0 && !contains(clusters, r.id) {
				continue
			}
			known, ok := r.serves(gvr)
			pending = pending || !known
			serving = serving || ok
		}
		if serving {
			break
		}
		if !pending {
			return nil, ErrNotServed
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(25 * time.Millisecond):
		}
	}
	s := m.OpenScope(gvr, clusters)
	defer s.Close()
	return ingest.WaitModule(ctx, gvr)
}

// Close stops every cluster.
func (m *Manager) Close() {
	m.mu.Lock()
	rs := make([]*runner, 0, len(m.runners))
	for _, r := range m.runners {
		if r.stopTimer != nil {
			r.stopTimer.Stop()
		}
		rs = append(rs, r)
	}
	m.runners = map[string]*runner{}
	m.mu.Unlock()
	for _, r := range rs {
		r.stop()
	}
}

func schemaGR(gvr string) schema.GroupResource {
	g, _, res, _ := ingest.ParseGVR(gvr)
	return schema.GroupResource{Group: g, Resource: res}
}

// classify maps an ingest error to a phase and a short, credential-free message.
func classify(err error) (protocol.ClusterPhase, string) {
	var ne net.Error
	msg := err.Error()
	switch {
	case apierrors.IsUnauthorized(err):
		return protocol.PhaseAuthRequired, "Authentication required"
	case apierrors.IsForbidden(err):
		return protocol.PhaseAuthRequired, "Access forbidden"
	case strings.Contains(msg, "getting credentials") || strings.Contains(msg, "exec plugin") || strings.Contains(msg, "executable"):
		return protocol.PhaseAuthRequired, "Credential plugin failed"
	case errors.As(err, &ne), strings.Contains(msg, "connection refused"), strings.Contains(msg, "no such host"),
		strings.Contains(msg, "i/o timeout"), strings.Contains(msg, "no route to host"), strings.Contains(msg, "dial tcp"),
		strings.Contains(msg, "context deadline exceeded"), strings.Contains(msg, "EOF"):
		return protocol.PhaseUnreachable, "Cluster unreachable"
	}
	if len(msg) > 120 {
		msg = msg[:120]
	}
	return protocol.PhaseDegraded, msg
}

// Changed tells listeners that settings affecting the cluster list or states changed.
func (m *Manager) Changed() { m.kick() }

// freeOSMemory is debug.FreeOSMemory (replaced in tests).
var freeOSMemory = debug.FreeOSMemory

// FreeThreshold is how much heap a deselect must release for the agent to return memory to the OS.
const FreeThreshold = 100 << 20

func heapAlloc() uint64 {
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	return ms.HeapAlloc
}

// maybeFree returns memory to the OS when more than FreeThreshold bytes were released, so the RSS
// actually falls (docs/performance.md §4).
func maybeFree(before, after uint64) bool {
	if before > after && before-after > FreeThreshold {
		freeOSMemory()
		return true
	}
	return false
}
