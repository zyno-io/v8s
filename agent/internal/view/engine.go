package view

import (
	"context"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"github.com/zyno-io/v8s/agent/internal/ingest"
	"github.com/zyno-io/v8s/agent/internal/store"
	"github.com/zyno-io/v8s/agent/pkg/protocol"
)

// rebuildThreshold: a feed with at least this many events is applied as a bulk snapshot merge
// instead of per-event binary-search inserts.
const rebuildThreshold = 512

const MaxWindow = 20000

type clusterRec struct {
	name     string
	st       *store.Store
	attached bool
	evbuf    []store.Event
	snap     []store.Item
}

// Engine owns the materialized views of every selected cluster. All mutation happens under mu.
type Engine struct {
	mu    sync.Mutex
	names []string
	strs  []ingest.Str
	recs  []*clusterRec
	idx   map[string]uint16
	views map[string]*View

	wake chan struct{}

	lmu       sync.Mutex
	listeners map[chan struct{}]struct{}

	// Debounce is how long Run waits after the first change before applying the feed (batching).
	Debounce time.Duration
	// IdleTTL is how long a view without subscribers is kept (rebuilding costs ~10 ms).
	IdleTTL time.Duration
}

func NewEngine() *Engine {
	return &Engine{
		idx: make(map[string]uint16), views: make(map[string]*View), wake: make(chan struct{}, 1),
		listeners: make(map[chan struct{}]struct{}),
		Debounce:  8 * time.Millisecond, IdleTTL: 60 * time.Second,
	}
}

func (e *Engine) signal() {
	select {
	case e.wake <- struct{}{}:
	default:
	}
}

// Listen returns a channel that receives a (coalesced) signal whenever any subscription became dirty.
func (e *Engine) Listen() (<-chan struct{}, func()) {
	ch := make(chan struct{}, 1)
	e.lmu.Lock()
	e.listeners[ch] = struct{}{}
	e.lmu.Unlock()
	return ch, func() { e.lmu.Lock(); delete(e.listeners, ch); e.lmu.Unlock() }
}

func (e *Engine) notify() {
	e.lmu.Lock()
	for ch := range e.listeners {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
	e.lmu.Unlock()
}

// Run applies store changes until ctx ends: first change after quiet is applied after Debounce.
func (e *Engine) Run(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-e.wake:
		}
		if e.Debounce > 0 {
			t := time.NewTimer(e.Debounce)
			select {
			case <-ctx.Done():
				t.Stop()
				return
			case <-t.C:
			}
		}
		e.Pump()
	}
}

// Attach makes a cluster's store part of every view whose query accepts it.
func (e *Engine) Attach(name string, st *store.Store) {
	e.mu.Lock()
	cl, ok := e.idx[name]
	if !ok {
		cl = uint16(len(e.recs))
		e.idx[name] = cl
		e.names = append(e.names, name)
		e.strs = append(e.strs, ingest.Str(intern(name)))
		e.recs = append(e.recs, &clusterRec{name: name})
	}
	rec := e.recs[cl]
	rec.st, rec.attached = st, true
	st.SetNotify(e.signal)
	// Bulk-load: drain + snapshot atomically, then merge into every view.
	events, snap, _ := st.Drain(rec.evbuf, 0, rec.snap, nil)
	rec.evbuf, rec.snap = events, snap
	changed := false
	for _, v := range e.views {
		changed = v.replaceCluster(cl, snap) || changed
	}
	e.mu.Unlock()
	if changed {
		e.markAllDirty()
	}
}

// Detach removes a cluster's rows from every view (the store itself is not touched).
func (e *Engine) Detach(name string) {
	e.mu.Lock()
	cl, ok := e.idx[name]
	if !ok || !e.recs[cl].attached {
		e.mu.Unlock()
		return
	}
	rec := e.recs[cl]
	rec.attached = false
	rec.st.SetNotify(nil)
	rec.st, rec.evbuf, rec.snap = nil, nil, nil
	for _, v := range e.views {
		v.replaceCluster(cl, nil)
	}
	e.mu.Unlock()
	e.markAllDirty()
}

func (e *Engine) markAllDirty() {
	e.mu.Lock()
	for _, v := range e.views {
		v.markDirty()
	}
	e.mu.Unlock()
	e.notify()
}

// Pump drains every attached store's change feed into the views and marks affected subscriptions dirty.
func (e *Engine) Pump() bool {
	e.mu.Lock()
	changed := e.pumpLocked()
	if e.IdleTTL > 0 {
		now := time.Now()
		for k, v := range e.views {
			if len(v.subs) == 0 && now.Sub(v.idleSince) > e.IdleTTL {
				delete(e.views, k)
			}
		}
	}
	e.mu.Unlock()
	if changed {
		e.notify()
	}
	return changed
}

func (e *Engine) pumpLocked() bool {
	changed := false
	for cl, rec := range e.recs {
		if !rec.attached {
			continue
		}
		events, snap, took := rec.st.Drain(rec.evbuf, rebuildThreshold, rec.snap, nil)
		rec.evbuf, rec.snap = events, snap
		if len(events) == 0 {
			continue
		}
		for _, v := range e.views {
			var c bool
			if took {
				c = v.replaceCluster(uint16(cl), snap)
			} else {
				c = v.apply(uint16(cl), events)
			}
			if c {
				v.markDirty()
				changed = true
			}
		}
		clear(rec.evbuf)
	}
	return changed
}

// Stats reports engine sizes (for tests and diagnostics).
func (e *Engine) Views() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return len(e.views)
}

// ---- views ----------------------------------------------------------------------------

// View is a materialized sorted list for one normalized query, shared by equal queries.
type View struct {
	e         *Engine
	n         *Norm
	entries   []entry
	subs      map[*Sub]struct{}
	cmp       cmpFn
	key       keyFn
	match     func(r *ingest.PodRow, cl uint16) bool
	idleSince time.Time
}

func (v *View) markDirty() {
	for s := range v.subs {
		s.dirty.Store(true)
	}
}

func (v *View) find(row *ingest.PodRow, cl uint16, h uint32) (int, bool) {
	probe := mkEntry(row, cl, h, 0)
	return slices.BinarySearchFunc(v.entries, probe, func(a, b entry) int { return v.cmp(&a, &b) })
}

func (v *View) insert(en entry) {
	i, _ := v.find(en.row, en.cl, en.h)
	v.entries = slices.Insert(v.entries, i, en)
}

func (v *View) apply(cl uint16, evs []store.Event) bool {
	changed := false
	for i := range evs {
		ev := &evs[i]
		oldIn := ev.Old != nil && v.match(ev.Old, cl)
		newIn := ev.New != nil && v.match(ev.New, cl)
		switch {
		case oldIn && newIn:
			en := mkEntry(ev.New, cl, ev.H, ev.Gen)
			if pos, ok := v.find(ev.Old, cl, ev.H); ok {
				if v.cmp(&v.entries[pos], &en) == 0 {
					v.entries[pos] = en
					changed = true
					continue
				}
				v.entries = slices.Delete(v.entries, pos, pos+1)
			}
			v.insert(en)
			changed = true
		case oldIn:
			if pos, ok := v.find(ev.Old, cl, ev.H); ok {
				v.entries = slices.Delete(v.entries, pos, pos+1)
				changed = true
			}
		case newIn:
			v.insert(mkEntry(ev.New, cl, ev.H, ev.Gen))
			changed = true
		}
	}
	return changed
}

// replaceCluster replaces all entries of one cluster with the (filtered) snapshot.
func (v *View) replaceCluster(cl uint16, items []store.Item) bool {
	kept := v.entries[:0]
	removed := false
	for _, en := range v.entries {
		if en.cl != cl {
			kept = append(kept, en)
		} else {
			removed = true
		}
	}
	clear(v.entries[len(kept):])
	var fresh []entry
	for i := range items {
		it := &items[i]
		if v.match(it.Row, cl) {
			fresh = append(fresh, mkEntry(it.Row, cl, it.H, it.Gen))
		}
	}
	if len(fresh) == 0 {
		v.entries = kept
		return removed
	}
	v.sort(fresh)
	if len(kept) == 0 {
		v.entries = fresh
		return true
	}
	out := make([]entry, len(kept)+len(fresh))
	mergeInto(out, kept, fresh, v.cmp)
	v.entries = out
	return true
}

// viewFor returns the shared view for n, building it if needed. Caller holds e.mu.
func (e *Engine) viewFor(n *Norm) *View {
	if v, ok := e.views[n.Key]; ok {
		return v
	}
	v := &View{e: e, n: n, subs: make(map[*Sub]struct{})}
	v.cmp = e.buildCmp(n)
	v.key = e.buildKey(n)
	v.match = e.buildMatch(n)
	// Drain every cluster atomically with its snapshot so no event is applied twice; existing
	// views get the events, the new view the snapshot.
	var all []entry
	for cl, rec := range e.recs {
		if !rec.attached {
			continue
		}
		events, snap, _ := rec.st.Drain(rec.evbuf, 0, rec.snap, n.Namespaces)
		rec.evbuf, rec.snap = events, snap
		if len(events) > 0 {
			// Events were computed against full-state; the snapshot above may be namespace-restricted,
			// which is fine for the new view but existing views need the events.
			for _, ov := range e.views {
				if ov.apply(uint16(cl), events) {
					ov.markDirty()
				}
			}
			clear(rec.evbuf)
		}
		for i := range snap {
			it := &snap[i]
			if v.match(it.Row, uint16(cl)) {
				all = append(all, mkEntry(it.Row, uint16(cl), it.H, it.Gen))
			}
		}
	}
	v.sort(all)
	v.entries = all
	e.views[n.Key] = v
	return v
}

func (e *Engine) alive(k refKey) bool {
	rec := e.recs[k.cl]
	if !rec.attached {
		return false
	}
	_, ok := rec.st.Row(k.h, k.gen)
	return ok
}

// ---- subscriptions --------------------------------------------------------------------

type refKey struct {
	cl     uint16
	h, gen uint32
}

// Sub is one subscription: a window over a (shared) view plus the per-subscription wire-id map and
// "last sent" state used to compute patches. A Sub must be driven from one goroutine (Prepare,
// Commit, Update); only Dirty/MarkDirty are safe from elsewhere.
type Sub struct {
	e      *Engine
	v      *View
	gen    uint32
	win    protocol.Window
	closed bool

	nextID uint32
	idOf   map[refKey]uint32
	refOf  map[uint32]refKey

	sentIDs    []uint32
	sentVer    map[uint32]uint32
	sentTotal  int
	sentOffset int
	sentGen    uint32
	sentOnce   bool

	mark  map[uint32]struct{}
	dirty atomic.Bool
}

func clampWindow(w protocol.Window) protocol.Window {
	w.Limit = max(0, min(w.Limit, MaxWindow))
	return w
}

// Subscribe creates a subscription on the shared view for q.
func (e *Engine) Subscribe(q protocol.ViewQuery, gen uint32, w protocol.Window) (*Sub, error) {
	n, err := Normalize(q)
	if err != nil {
		return nil, err
	}
	s := &Sub{e: e, gen: gen, win: clampWindow(w), idOf: make(map[refKey]uint32), refOf: make(map[uint32]refKey),
		sentVer: make(map[uint32]uint32), mark: make(map[uint32]struct{})}
	e.mu.Lock()
	s.attach(e.viewFor(n))
	e.mu.Unlock()
	s.dirty.Store(true)
	return s, nil
}

func (s *Sub) attach(v *View) {
	s.v = v
	v.subs[s] = struct{}{}
}

func (s *Sub) detach() {
	if s.v == nil {
		return
	}
	delete(s.v.subs, s)
	if len(s.v.subs) == 0 {
		s.v.idleSince = time.Now()
		if s.e.IdleTTL <= 0 {
			delete(s.e.views, s.v.n.Key)
		}
	}
	s.v = nil
}

// Update changes the query (when q != nil) and/or the window (when w != nil); gen is the client's
// current generation. Ids stay stable across query changes.
func (s *Sub) Update(q *protocol.ViewQuery, gen uint32, w *protocol.Window) error {
	var n *Norm
	if q != nil {
		var err error
		if n, err = Normalize(*q); err != nil {
			return err
		}
	}
	s.e.mu.Lock()
	defer s.e.mu.Unlock()
	if s.closed {
		return nil
	}
	s.gen = gen
	if w != nil {
		s.win = clampWindow(*w)
	}
	if n != nil && n.Key != s.v.n.Key {
		s.detach()
		s.attach(s.e.viewFor(n))
	}
	s.dirty.Store(true)
	return nil
}

func (s *Sub) Close() {
	s.e.mu.Lock()
	defer s.e.mu.Unlock()
	if !s.closed {
		s.closed = true
		s.detach()
	}
}

func (s *Sub) Dirty() bool { return s.dirty.Load() }
func (s *Sub) MarkDirty()  { s.dirty.Store(true) }
func (s *Sub) Gen() uint32 { return s.gen }
func (s *Sub) Total() int  { s.e.mu.Lock(); defer s.e.mu.Unlock(); return s.totalLocked() }
func (s *Sub) totalLocked() int {
	if s.v == nil {
		return 0
	}
	return len(s.v.entries)
}

func (s *Sub) idFor(en *entry) uint32 {
	k := refKey{en.cl, en.h, en.gen}
	if id, ok := s.idOf[k]; ok {
		return id
	}
	s.nextID++
	s.idOf[k] = s.nextID
	s.refOf[s.nextID] = k
	return s.nextID
}

func (s *Sub) indexOfID(id uint32) (int, bool) {
	k, ok := s.refOf[id]
	if !ok {
		return 0, false
	}
	rec := s.e.recs[k.cl]
	if rec.attached {
		if row, ok := rec.st.Row(k.h, k.gen); ok {
			if i, ok := s.v.find(row, k.cl, k.h); ok && s.v.entries[i].gen == k.gen {
				return i, true
			}
		}
	}
	for i := range s.v.entries { // the view may lag the store by a pump: fall back to a scan
		en := &s.v.entries[i]
		if en.cl == k.cl && en.h == k.h && en.gen == k.gen {
			return i, true
		}
	}
	return 0, false
}

func (s *Sub) bounds(total int) (start, end int) {
	off := s.win.Offset
	if s.win.AnchorID != 0 {
		if idx, ok := s.indexOfID(s.win.AnchorID); ok {
			off += idx
		}
	}
	start = max(0, min(off, total-s.win.Limit))
	start = max(0, start)
	end = min(total, start+s.win.Limit)
	return
}

// PendRow is a row to send.
type PendRow struct {
	ID      uint32
	Row     *ingest.PodRow
	Cluster ingest.Str
}

// Pending is the diff between the subscription's current window and what the client last received.
// The wire layer consumes it in frames (Commit after each) so a large first patch is split.
type Pending struct {
	s       *Sub
	Gen     uint32
	Total   int
	Offset  int
	IDs     []uint32
	SendIDs bool
	Rows    []PendRow
	Removed []uint32
	ri, di  int
	sent    bool
}

func (p *Pending) RemainingRows() []PendRow   { return p.Rows[p.ri:] }
func (p *Pending) RemainingRemoved() []uint32 { return p.Removed[p.di:] }
func (p *Pending) Done() bool {
	return p.sent && p.ri == len(p.Rows) && p.di == len(p.Removed) && !p.SendIDs
}

// Prepare computes the next patch for this subscription, or nil if the client is up to date.
// It clears the dirty flag first, so a change during sending re-arms it.
func (s *Sub) Prepare() *Pending {
	e := s.e
	e.mu.Lock()
	defer e.mu.Unlock()
	s.dirty.Store(false)
	if s.closed || s.v == nil {
		return nil
	}
	total := len(s.v.entries)
	start, end := s.bounds(total)
	win := s.v.entries[start:end]
	ids := make([]uint32, len(win))
	for i := range win {
		ids[i] = s.idFor(&win[i])
	}
	genChanged := !s.sentOnce || s.sentGen != s.gen
	idsChanged := genChanged || !slices.Equal(ids, s.sentIDs)
	var removed []uint32
	if idsChanged && len(s.sentIDs) > 0 {
		clear(s.mark)
		for _, id := range ids {
			s.mark[id] = struct{}{}
		}
		for _, id := range s.sentIDs {
			if _, ok := s.mark[id]; !ok {
				removed = append(removed, id)
			}
		}
	}
	if genChanged {
		clear(s.sentVer)
	}
	var rows []PendRow
	for i := range win {
		row := win[i].row
		if s.sentVer[ids[i]] != row.Ver() {
			rows = append(rows, PendRow{ID: ids[i], Row: row, Cluster: e.strs[win[i].cl]})
		}
	}
	if !idsChanged && len(rows) == 0 && len(removed) == 0 && s.sentOnce && total == s.sentTotal && start == s.sentOffset {
		return nil
	}
	if len(s.idOf) > 4096+2*total { // sweep ids of objects that no longer exist
		for k, id := range s.idOf {
			if !e.alive(k) {
				delete(s.idOf, k)
				delete(s.refOf, id)
			}
		}
	}
	return &Pending{s: s, Gen: s.gen, Total: total, Offset: start, IDs: ids, SendIDs: idsChanged, Rows: rows, Removed: removed}
}

// Commit records that the next nRows rows and nRemoved removed ids (and the ids list when withIDs)
// were sent in one frame.
func (p *Pending) Commit(nRows, nRemoved int, withIDs bool) {
	s := p.s
	s.e.mu.Lock()
	defer s.e.mu.Unlock()
	if withIDs {
		s.sentIDs = p.IDs
		p.SendIDs = false
	}
	for _, id := range p.Removed[p.di : p.di+nRemoved] {
		delete(s.sentVer, id)
		if k, ok := s.refOf[id]; ok && !s.e.alive(k) {
			delete(s.idOf, k)
			delete(s.refOf, id)
		}
	}
	p.di += nRemoved
	for _, r := range p.Rows[p.ri : p.ri+nRows] {
		s.sentVer[r.ID] = r.Row.Ver()
	}
	p.ri += nRows
	s.sentTotal, s.sentOffset, s.sentGen, s.sentOnce = p.Total, p.Offset, p.Gen, true
	p.sent = true
}

func (v *View) sort(es []entry) {
	if v.key != nil && len(es) > 256 {
		sortKeyed(es, v.cmp, v.key)
		return
	}
	sortEntries(es, v.cmp)
}
