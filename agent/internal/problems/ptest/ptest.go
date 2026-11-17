// Package ptest is the test harness of the problems engine: an injectable clock and a hub with one cluster
// engine fed from in-memory stores, for detector tests in any package.
package ptest

import (
	"sync"
	"testing"
	"time"

	"github.com/zyno-io/v8s/agent/internal/ingest"
	. "github.com/zyno-io/v8s/agent/internal/problems"
	"github.com/zyno-io/v8s/agent/internal/store"
	"github.com/zyno-io/v8s/agent/pkg/protocol"
)

// clock is an injectable clock.
type Clock struct {
	mu sync.Mutex
	t  time.Time
}

func NewClock() *Clock { return &Clock{t: time.Date(2026, 11, 17, 12, 0, 0, 0, time.UTC)} }
func (c *Clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}
func (c *Clock) Advance(d time.Duration) { c.mu.Lock(); c.t = c.t.Add(d); c.mu.Unlock() }

// harness is a hub with one cluster engine and stores for the kinds a test feeds.
type Harness struct {
	T      *testing.T
	Clk    *Clock
	Hub    *Hub
	E      *Engine
	Stores map[string]*store.Store
	Set    protocol.Settings
}

func New(t *testing.T, gvrs ...string) *Harness {
	t.Helper()
	return NewWithSettings(t, protocol.Settings{ProblemPrefs: map[string]any{}, Acks: map[string]any{}}, gvrs...)
}

// NewWithSettings is New over an existing settings document (an agent restart).
func NewWithSettings(t *testing.T, set protocol.Settings, gvrs ...string) *Harness {
	t.Helper()
	h := &Harness{T: t, Clk: NewClock(), Stores: map[string]*store.Store{}}
	h.Set = set
	h.Hub = NewHub(HubConfig{Settings: func() protocol.Settings { return h.Set }, Now: h.Clk.Now, Hold: 30 * time.Second,
		Patch: func(p map[string]any) (protocol.Settings, error) {
			if a, ok := p["acks"].(map[string]any); ok {
				for k, v := range a {
					if v == nil {
						delete(h.Set.Acks, k)
					} else {
						h.Set.Acks[k] = v
					}
				}
			}
			return h.Set, nil
		}})
	h.E = h.Hub.Cluster("c1")
	h.E.Now = h.Clk.Now
	for _, g := range gvrs {
		h.Sync(g)
	}
	return h
}

// sync attaches an empty, synced store for gvr.
func (h *Harness) Sync(gvr string) *store.Store {
	st := store.New()
	h.Stores[gvr] = st
	h.E.Attach(gvr, st)
	_ = st.Replace(nil, "1")
	return st
}

func (h *Harness) Put(gvr string, r *ingest.Row) {
	st := h.Stores[gvr]
	if st == nil {
		st = h.Sync(gvr)
	}
	_ = st.Update(r)
}

func (h *Harness) Del(gvr string, r *ingest.Row) { _ = h.Stores[gvr].Delete(r) }

// pass runs one evaluation pass.
func (h *Harness) Pass() { h.E.Pass() }

// advance moves the clock and runs a pass.
func (h *Harness) Advance(d time.Duration) { h.Clk.Advance(d); h.E.Pass() }

func (h *Harness) ByClass(class string) []Problem {
	var out []Problem
	for _, p := range h.E.Problems() {
		if p.Class == class {
			out = append(out, p)
		}
	}
	return out
}

// SetClass enables or disables a class globally through the settings (what the UI does with settings.set).
func (h *Harness) SetClass(class string, on bool) {
	h.Set.ProblemPrefs = map[string]any{"classes": map[string]any{class: on}}
	h.Hub.SettingsChanged(h.Set)
}
