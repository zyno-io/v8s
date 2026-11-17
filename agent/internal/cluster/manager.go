package cluster

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/zyno-io/v8s/agent/internal/ingest"
	"github.com/zyno-io/v8s/agent/internal/store"
	"github.com/zyno-io/v8s/agent/internal/view"
	"github.com/zyno-io/v8s/agent/pkg/protocol"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/client-go/tools/cache"
)

// DeselectGrace is how long a deselected cluster keeps its ingest running (a quick reselect is free).
var DeselectGrace = 10 * time.Second

type runner struct {
	id     string
	st     *store.Store
	cancel context.CancelFunc
	done   chan struct{}

	phase   atomic.Value // protocol.ClusterPhase
	synced  atomic.Int64
	message atomic.Value // string
	everOK  atomic.Bool

	stopTimer *time.Timer // pending grace-period stop
}

func (r *runner) state() protocol.ClusterState {
	msg, _ := r.message.Load().(string)
	return protocol.ClusterState{ID: r.id, Phase: r.phase.Load().(protocol.ClusterPhase), Synced: int(r.synced.Load()), Message: msg}
}

// Manager owns the selected clusters.
type Manager struct {
	src Source
	eng *view.Engine
	log *slog.Logger

	mu       sync.Mutex
	runners  map[string]*runner // includes clusters in their deselect grace period
	selected []string
	known    map[string]bool

	lmu       sync.Mutex
	listeners map[chan struct{}]struct{}
	lastKick  atomic.Int64
	ctx       context.Context
}

func NewManager(ctx context.Context, src Source, eng *view.Engine, log *slog.Logger) *Manager {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	m := &Manager{src: src, eng: eng, log: log, runners: map[string]*runner{}, listeners: map[chan struct{}]struct{}{}, ctx: ctx}
	m.selected = []string{}
	return m
}

func (m *Manager) Source() Source { return m.src }

// List returns the clusters and the remembered selection.
func (m *Manager) List() protocol.ClustersListResult {
	cs := m.src.Clusters()
	if cs == nil {
		cs = []protocol.Cluster{}
	}
	sel := m.src.DefaultSelection()
	m.mu.Lock()
	if len(m.selected) > 0 {
		sel = append([]string(nil), m.selected...)
	}
	m.mu.Unlock()
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

// Select makes ids the selected set: new clusters start ingesting, removed ones stop after a grace period.
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
			m.eng.Detach(id)
			r.stopTimer = time.AfterFunc(DeselectGrace, func() { m.stopIfDeselected(id) })
		}
	}
	for _, id := range list {
		r := m.runners[id]
		if r != nil {
			if r.stopTimer != nil { // reselected during grace
				r.stopTimer.Stop()
				r.stopTimer = nil
				m.eng.Attach(id, r.st)
			}
			continue
		}
		nr, err := m.start(id)
		if err != nil {
			m.log.Warn("cluster start failed", "cluster", id, "err", err)
			nr = m.failed(id, err)
		}
		m.runners[id] = nr
		m.eng.Attach(id, nr.st)
	}
	m.selected = list
	m.mu.Unlock()
	m.kick()
	return m.States(), nil
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
	r.cancel()
	<-r.done
	r.st.Reset() // free memory
	m.src.Release(id)
	m.log.Info("cluster stopped", "cluster", id)
}

// Running reports whether ingest for id is still alive (including during its grace period).
func (m *Manager) Running(id string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.runners[id] != nil
}

func (m *Manager) failed(id string, err error) *runner {
	ctx, cancel := context.WithCancel(m.ctx)
	r := &runner{id: id, st: store.New(), cancel: cancel, done: make(chan struct{})}
	close(r.done)
	_ = ctx
	phase, msg := classify(err)
	r.phase.Store(phase)
	r.message.Store(msg)
	return r
}

func (m *Manager) start(id string) (*runner, error) {
	api, err := m.src.PodAPI(id)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(m.ctx)
	r := &runner{id: id, st: store.New(), cancel: cancel, done: make(chan struct{})}
	r.phase.Store(protocol.PhaseConnecting)
	r.message.Store("")
	setPhase := func(p protocol.ClusterPhase, msg string) {
		if r.phase.Load() != p || r.message.Load() != msg {
			r.phase.Store(p)
			r.message.Store(msg)
			m.kick()
		}
	}
	lw := &ingest.ListerWatcher{API: api, Hooks: ingest.Hooks{
		OnSyncProgress: func(n int) {
			r.synced.Store(int64(n))
			if r.phase.Load() == protocol.PhaseConnecting {
				setPhase(protocol.PhaseSyncing, "")
			}
			m.kickThrottled()
		},
		OnOK: func() {
			if r.phase.Load() == protocol.PhaseConnecting {
				setPhase(protocol.PhaseSyncing, "")
			}
		},
		OnError: func(err error) {
			if ctx.Err() != nil {
				return
			}
			p, msg := classify(err)
			if r.everOK.Load() && p == protocol.PhaseUnreachable {
				p = protocol.PhaseReconnecting
			} else if r.everOK.Load() && p != protocol.PhaseAuthRequired {
				p = protocol.PhaseDegraded
			}
			m.log.Warn("cluster error", "cluster", id, "phase", p, "err", msg)
			setPhase(p, msg)
		},
	}}
	r.st.OnReplace = func(n int) {
		r.synced.Store(int64(n))
		r.everOK.Store(true)
		setPhase(protocol.PhaseReady, "")
	}
	r.st.OnActivity = func() {
		if p := r.phase.Load(); p == protocol.PhaseReconnecting || p == protocol.PhaseDegraded {
			setPhase(protocol.PhaseReady, "")
		}
	}
	refl := cache.NewReflectorWithOptions(lw, &ingest.PodRow{}, r.st, cache.ReflectorOptions{Name: "pods/" + id, TypeDescription: "pods"})
	go func() {
		defer close(r.done)
		refl.RunWithContext(ctx)
	}()
	return r, nil
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
		r.cancel()
		<-r.done
	}
}
