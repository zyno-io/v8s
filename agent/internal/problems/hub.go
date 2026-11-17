package problems

import (
	"context"
	"errors"
	"log/slog"
	"reflect"
	"sort"
	"sync"
	"time"

	"github.com/zyno-io/v8s/agent/pkg/protocol"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// FetchFunc reads one live object (problems.evidence).
type FetchFunc func(ctx context.Context, cluster, gvr, ns, name string) (*unstructured.Unstructured, error)

// HubConfig wires the hub to settings and the cluster connections.
type HubConfig struct {
	Settings func() protocol.Settings
	Patch    func(patch map[string]any) (protocol.Settings, error)
	Fetch    FetchFunc
	Log      *slog.Logger
	// Now, Hold and Interval are injectable; defaults time.Now, DefaultHold, DefaultInterval.
	Now      func() time.Time
	Hold     time.Duration
	Interval time.Duration
}

// Hub owns the per-cluster engines and the user's class preferences and acknowledgements.
type Hub struct {
	log      *slog.Logger
	cfg      HubConfig
	Now      func() time.Time
	Hold     time.Duration
	Interval time.Duration

	mu      sync.Mutex
	engines map[string]*Engine
	global  map[string]bool
	perCl   map[string]map[string]bool
	acks    map[string]Ack
	lastDoc map[string]any // the acks document last read from or written to settings

	// OnPrefsChange is called (outside locks) when the effective class preferences changed.
	OnPrefsChange func()

	persistMu  sync.Mutex
	persisting bool
	dirty      bool
	wg         sync.WaitGroup
}

// Ack is one acknowledgement.
type Ack struct {
	At      int64  `json:"at"`
	Seen    int64  `json:"seen,omitempty"` // last time the problem was live (unix ms; refreshed at most every ackSeenEvery)
	Until   int64  `json:"until,omitempty"`
	Rank    int    `json:"rank"`
	Count   int    `json:"count"`
	Cluster string `json:"cluster"`
}

// NewHub builds a hub and loads the preferences and acknowledgements from the settings.
func NewHub(cfg HubConfig) *Hub {
	h := &Hub{cfg: cfg, log: cfg.Log, Now: cfg.Now, Hold: cfg.Hold, Interval: cfg.Interval, engines: map[string]*Engine{},
		global: map[string]bool{}, perCl: map[string]map[string]bool{}, acks: map[string]Ack{}}
	if h.log == nil {
		h.log = slog.New(slog.DiscardHandler)
	}
	if h.Now == nil {
		h.Now = time.Now
	}
	if h.Hold == 0 {
		h.Hold = DefaultHold
	}
	if h.Interval == 0 {
		h.Interval = DefaultInterval
	}
	if cfg.Settings != nil {
		h.loadSettings(cfg.Settings())
	}
	return h
}

func (h *Hub) loadSettings(s protocol.Settings) (prefsChanged bool) {
	global, perCl := map[string]bool{}, map[string]map[string]bool{}
	if cl, ok := s.ProblemPrefs["classes"].(map[string]any); ok {
		for k, v := range cl {
			if b, ok := v.(bool); ok {
				global[k] = b
			}
		}
	}
	if cs, ok := s.ProblemPrefs["clusters"].(map[string]any); ok {
		for id, m := range cs {
			mm, _ := m.(map[string]any)
			for k, v := range mm {
				if b, ok := v.(bool); ok {
					if perCl[id] == nil {
						perCl[id] = map[string]bool{}
					}
					perCl[id][k] = b
				}
			}
		}
	}
	h.mu.Lock()
	prefsChanged = !reflect.DeepEqual(global, h.global) || !reflect.DeepEqual(perCl, h.perCl)
	h.global, h.perCl = global, perCl
	if !reflect.DeepEqual(s.Acks, h.lastDoc) && !(len(s.Acks) == 0 && len(h.lastDoc) == 0) {
		h.lastDoc = cloneDoc(s.Acks)
		h.acks = parseAcks(s.Acks)
	}
	h.mu.Unlock()
	return prefsChanged
}

func cloneDoc(m map[string]any) map[string]any {
	out := make(map[string]any, len(m))
	for k, v := range m {
		if mm, ok := v.(map[string]any); ok {
			c := make(map[string]any, len(mm))
			for a, b := range mm {
				c[a] = b
			}
			out[k] = c
		} else {
			out[k] = v
		}
	}
	return out
}

func num(v any) int64 {
	switch x := v.(type) {
	case float64:
		return int64(x)
	case int64:
		return x
	case int:
		return int64(x)
	case uint64:
		return int64(x)
	}
	return 0
}

func parseAcks(doc map[string]any) map[string]Ack {
	out := map[string]Ack{}
	for id, v := range doc {
		m, ok := v.(map[string]any)
		if !ok {
			continue
		}
		cl, _ := m["cluster"].(string)
		out[id] = Ack{At: num(m["at"]), Seen: num(m["seen"]), Until: num(m["until"]), Rank: int(num(m["rank"])), Count: int(num(m["count"])), Cluster: cl}
	}
	return out
}

func ackDoc(a Ack) map[string]any {
	m := map[string]any{"at": a.At, "rank": a.Rank, "count": a.Count, "cluster": a.Cluster}
	if a.Until > 0 {
		m["until"] = a.Until
	}
	if a.Seen > 0 {
		m["seen"] = a.Seen
	}
	return m
}

// SettingsChanged reloads preferences and acknowledgements (call from settings.Store.OnChange).
func (h *Hub) SettingsChanged(s protocol.Settings) {
	changed := h.loadSettings(s)
	h.mu.Lock()
	es := make([]*Engine, 0, len(h.engines))
	for _, e := range h.engines {
		es = append(es, e)
	}
	h.mu.Unlock()
	for _, e := range es {
		e.Refresh()
	}
	if changed && h.OnPrefsChange != nil {
		h.OnPrefsChange()
	}
}

func (h *Hub) classEnabled(cluster string, d *Detector) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	if v, ok := h.perCl[cluster][d.Class]; ok {
		return v
	}
	if v, ok := h.global[d.Class]; ok {
		return v
	}
	return d.Default
}

// ClassEnabled reports whether a class is enabled for a cluster (preferences over the default).
func (h *Hub) ClassEnabled(cluster, class string) bool {
	d := Lookup(class)
	return d != nil && h.classEnabled(cluster, d)
}

// Cluster returns the engine of a cluster, creating it.
func (h *Hub) Cluster(id string) *Engine {
	h.mu.Lock()
	e := h.engines[id]
	h.mu.Unlock()
	if e != nil {
		return e
	}
	ne := newEngine(h, id) // reads the preferences: not under h.mu
	h.mu.Lock()
	defer h.mu.Unlock()
	if e = h.engines[id]; e == nil {
		e = ne
		h.engines[id] = e
	}
	return e
}

// Engine returns the engine of a cluster or nil.
func (h *Hub) Engine(id string) *Engine {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.engines[id]
}

// Drop forgets a cluster's engine (the cluster was released).
func (h *Hub) Drop(id string) {
	h.mu.Lock()
	delete(h.engines, id)
	h.mu.Unlock()
}

func (h *Hub) engineList() []*Engine {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make([]*Engine, 0, len(h.engines))
	for _, e := range h.engines {
		out = append(out, e)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].cluster < out[j].cluster })
	return out
}

// ---- acknowledgements ----------------------------------------------------------------------

// ackThreshold is the count at which an acknowledged problem counts as having grown: +50%.
func ackThreshold(n int) int { return n + max(1, n/2) }

// ackSeenEvery bounds how often a live problem refreshes (and so re-persists) its acknowledgement's last-seen time.
const ackSeenEvery = 6 * time.Hour

// AckMaxAge is how long an acknowledgement of a problem that is not live is kept before GCAcks drops it: long
// enough for a GitOps recreation, an agent restart or a class that was switched off for a while.
const AckMaxAge = 7 * 24 * time.Hour

// ackHolds reports whether the problem is currently acknowledged (until is the snooze deadline, 0 none). An
// acknowledgement that has expired or that the problem outgrew (severity rose, count grew past the threshold)
// is dropped for good.
func (h *Hub) ackHolds(id string, now time.Time, rank, count int) (held bool, until int64) {
	h.mu.Lock()
	a, ok := h.acks[id]
	if !ok {
		h.mu.Unlock()
		return false, 0
	}
	material := (a.Until > 0 && now.UnixMilli() >= a.Until) || rank < a.Rank || (a.Count > 0 && count >= ackThreshold(a.Count))
	persist := false
	if material {
		delete(h.acks, id)
	} else if now.UnixMilli()-a.Seen >= ackSeenEvery.Milliseconds() {
		a.Seen = now.UnixMilli()
		h.acks[id] = a
		persist = true
	}
	h.mu.Unlock()
	if material || persist {
		h.persistAsync()
	}
	if material {
		return false, 0
	}
	return true, a.Until
}

// ackResolved drops the acknowledgement of a problem that resolved (a recurrence is a new problem).
func (h *Hub) ackResolved(id string) {
	h.mu.Lock()
	_, ok := h.acks[id]
	delete(h.acks, id)
	h.mu.Unlock()
	if ok {
		h.persistAsync()
	}
}

func (h *Hub) persistAsync() {
	h.persistMu.Lock()
	h.dirty = true
	if h.persisting {
		h.persistMu.Unlock()
		return
	}
	h.persisting = true
	h.wg.Add(1)
	h.persistMu.Unlock()
	go func() {
		defer h.wg.Done()
		for {
			h.persistMu.Lock()
			if !h.dirty {
				h.persisting = false
				h.persistMu.Unlock()
				return
			}
			h.dirty = false
			h.persistMu.Unlock()
			if err := h.persist(); err != nil {
				h.log.Warn("could not persist acknowledgements", "err", err)
			}
		}
	}()
}

// Flush waits for pending background persistence of acknowledgements (tests, shutdown).
func (h *Hub) Flush() { h.wg.Wait() }

// persist writes the difference between the in-memory acknowledgements and the settings document.
func (h *Hub) persist() error {
	if h.cfg.Patch == nil {
		return nil
	}
	h.mu.Lock()
	want := make(map[string]any, len(h.acks))
	for id, a := range h.acks {
		want[id] = ackDoc(a)
	}
	patch := map[string]any{}
	for id := range h.lastDoc {
		if _, ok := want[id]; !ok {
			patch[id] = nil
		}
	}
	for id, v := range want {
		if !reflect.DeepEqual(normalizeDoc(v), normalizeDoc(h.lastDoc[id])) {
			patch[id] = v
		}
	}
	h.lastDoc = want
	h.mu.Unlock()
	if len(patch) == 0 {
		return nil
	}
	_, err := h.cfg.Patch(map[string]any{"acks": patch})
	return err
}

func normalizeDoc(v any) any {
	m, ok := v.(map[string]any)
	if !ok {
		return v
	}
	out := map[string]any{}
	for k, x := range m {
		out[k] = num(x)
		if s, ok := x.(string); ok {
			out[k] = s
		}
	}
	return out
}

var errUnknown = errors.New("no such problem")

// Ack acknowledges a problem (until unix ms, 0 = until it materially changes).
func (h *Hub) Ack(id string, until int64) error {
	for _, e := range h.engineList() {
		if p, ok := e.Problem(id); ok {
			h.mu.Lock()
			h.acks[id] = Ack{At: h.Now().UnixMilli(), Seen: h.Now().UnixMilli(), Until: until, Rank: p.Severity.Rank(), Count: p.Count, Cluster: e.cluster}
			h.mu.Unlock()
			if err := h.persist(); err != nil {
				return err
			}
			e.refreshAck(id)
			return nil
		}
	}
	return errUnknown
}

// Unack removes an acknowledgement.
func (h *Hub) Unack(id string) error {
	h.mu.Lock()
	a, ok := h.acks[id]
	delete(h.acks, id)
	h.mu.Unlock()
	if !ok {
		return nil
	}
	if err := h.persist(); err != nil {
		return err
	}
	if e := h.Engine(a.Cluster); e != nil {
		e.refreshAck(id)
	}
	return nil
}

// GCAcks drops acknowledgements of a cluster whose problem is not live and has not been seen for AckMaxAge.
// An acknowledgement of a problem that is merely absent right now is kept: it may be in a grace period after a
// restart, its object may be recreated by GitOps, or its class may be switched off. (A problem that resolved
// and stayed resolved loses its acknowledgement at once, in the engine.)
func (h *Hub) GCAcks(cluster string) {
	e := h.Engine(cluster)
	if e == nil {
		return
	}
	now := h.Now().UnixMilli()
	h.mu.Lock()
	var ids []string
	for id, a := range h.acks {
		if a.Cluster == cluster {
			ids = append(ids, id)
		}
	}
	h.mu.Unlock()
	live := map[string]bool{}
	for _, id := range ids {
		if _, ok := e.Problem(id); ok {
			live[id] = true
		}
	}
	changed := false
	h.mu.Lock()
	for _, id := range ids {
		a, ok := h.acks[id]
		if !ok {
			continue
		}
		switch {
		case live[id]: // a stable live problem never passes through ackHolds' refresh: keep its last-seen current
			if now-a.Seen >= ackSeenEvery.Milliseconds() {
				a.Seen = now
				h.acks[id] = a
				changed = true
			}
		case now-max(a.Seen, a.At) >= AckMaxAge.Milliseconds():
			delete(h.acks, id)
			changed = true
		}
	}
	h.mu.Unlock()
	if changed {
		h.persistAsync()
	}
}

// ---- classes -------------------------------------------------------------------------------

// Classes answers problems.classes.
func (h *Hub) Classes() protocol.ProblemsClassesResult {
	res := protocol.ProblemsClassesResult{Classes: []protocol.ProblemClassInfo{}, Unavailable: []protocol.ProblemUnavailable{}}
	engines := h.engineList()
	counts := make([]map[string]ClassCount, len(engines))
	for i, e := range engines {
		counts[i] = e.Counts()
		for gr, reason := range e.Unavailable() {
			res.Unavailable = append(res.Unavailable, protocol.ProblemUnavailable{Cluster: e.cluster, Kind: gr, Reason: reason})
		}
	}
	for _, d := range Detectors() {
		info := protocol.ProblemClassInfo{Class: d.Class, Family: d.Family, Title: d.Title, Severity: d.severity(&Draft{}).String(), Default: d.Default,
			Counts: map[string]protocol.ProblemCount{}}
		h.mu.Lock()
		info.Enabled = d.Default
		if v, ok := h.global[d.Class]; ok {
			info.Enabled = v
		}
		for id, m := range h.perCl {
			if v, ok := m[d.Class]; ok && v != info.Enabled {
				if info.EnabledIn == nil {
					info.EnabledIn = map[string]bool{}
				}
				info.EnabledIn[id] = v
			}
		}
		h.mu.Unlock()
		for i, e := range engines {
			if c, ok := counts[i][d.Class]; ok {
				info.Counts[e.cluster] = protocol.ProblemCount{Total: c.Total, Acked: c.Acked}
			}
		}
		res.Classes = append(res.Classes, info)
	}
	sort.Slice(res.Unavailable, func(i, j int) bool {
		if res.Unavailable[i].Cluster != res.Unavailable[j].Cluster {
			return res.Unavailable[i].Cluster < res.Unavailable[j].Cluster
		}
		return res.Unavailable[i].Kind < res.Unavailable[j].Kind
	})
	return res
}

// SettingsDocPrefs builds a settings patch toggling a class (helper for tests and the UI contract).
func SettingsDocPrefs(class string, on bool, cluster string) map[string]any {
	if cluster != "" {
		return map[string]any{"problemPrefs": map[string]any{"clusters": map[string]any{cluster: map[string]any{class: on}}}}
	}
	return map[string]any{"problemPrefs": map[string]any{"classes": map[string]any{class: on}}}
}
