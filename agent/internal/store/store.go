// Package store holds the projected rows of one (cluster, GVR). It implements cache.Store so a
// client-go Reflector can drive it, keys rows by uid, hands out dense u32 handles (internal only),
// maintains a namespace index, and records every change in a feed the view engine drains.
package store

import (
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"unique"

	"github.com/zyno-io/v8s/agent/internal/ingest"
	"k8s.io/client-go/tools/cache"
)

// Event is one change. Old is nil for an add, New is nil for a delete. Rows are immutable, so the
// view engine may keep and compare them freely.
type Event struct {
	H        uint32
	Gen      uint32
	Old, New *ingest.PodRow
}

// Item is a live row with its handle (used for snapshots).
type Item struct {
	H, Gen uint32
	Row    *ingest.PodRow
}

type nsList struct{ hs []uint32 }

type Store struct {
	mu    sync.RWMutex
	rows  []*ingest.PodRow // by handle; nil = free
	gens  []uint32         // bumped on every allocation of a handle
	pos   []uint32         // position of the handle inside its namespace list
	free  []uint32
	byUID map[string]uint32
	ns    map[unique.Handle[string]]*nsList
	feed  []Event
	rv    string
	// mark is scratch for Replace (epoch-marked handles).
	mark  []uint32
	epoch uint32

	synced bool

	// notify is called (outside the lock) after a mutation appended to the feed.
	notify atomic.Pointer[func()]
	// OnReplace is called after every Replace (initial sync or relist); OnActivity after any other mutation.
	OnReplace  func(n int)
	OnActivity func()
}

var _ cache.Store = (*Store)(nil)

func New() *Store {
	return &Store{byUID: make(map[string]uint32), ns: make(map[unique.Handle[string]]*nsList)}
}

func asRow(obj interface{}) (*ingest.PodRow, error) {
	if r, ok := obj.(*ingest.PodRow); ok {
		return r, nil
	}
	if d, ok := obj.(cache.DeletedFinalStateUnknown); ok {
		if r, ok := d.Obj.(*ingest.PodRow); ok {
			return r, nil
		}
	}
	return nil, fmt.Errorf("store: unexpected object %T", obj)
}

// ---- mutation (callers hold s.mu) -----------------------------------------------------

func (s *Store) upsertLocked(row *ingest.PodRow) uint32 {
	uid := row.UID()
	if h, ok := s.byUID[uid]; ok {
		old := s.rows[h]
		if old.CellsEqual(row) {
			row.SetVer(old.Ver())
			s.rows[h] = row
			return h
		}
		row.SetVer(old.Ver() + 1)
		s.rows[h] = row
		s.feed = append(s.feed, Event{H: h, Gen: s.gens[h], Old: old, New: row})
		return h
	}
	var h uint32
	if n := len(s.free); n > 0 {
		h = s.free[n-1]
		s.free = s.free[:n-1]
	} else {
		h = uint32(len(s.rows))
		s.rows = append(s.rows, nil)
		s.gens = append(s.gens, 0)
		s.pos = append(s.pos, 0)
		s.mark = append(s.mark, 0)
	}
	s.gens[h]++
	row.SetVer(1)
	s.rows[h] = row
	s.byUID[uid] = h
	l := s.ns[row.NS()]
	if l == nil {
		l = &nsList{}
		s.ns[row.NS()] = l
	}
	s.pos[h] = uint32(len(l.hs))
	l.hs = append(l.hs, h)
	s.feed = append(s.feed, Event{H: h, Gen: s.gens[h], New: row})
	return h
}

func (s *Store) deleteLocked(h uint32) {
	old := s.rows[h]
	if old == nil {
		return
	}
	delete(s.byUID, old.UID())
	l := s.ns[old.NS()]
	p := s.pos[h]
	last := l.hs[len(l.hs)-1]
	l.hs[p] = last
	s.pos[last] = p
	l.hs = l.hs[:len(l.hs)-1]
	if len(l.hs) == 0 {
		delete(s.ns, old.NS())
	}
	s.rows[h] = nil
	s.free = append(s.free, h)
	s.feed = append(s.feed, Event{H: h, Gen: s.gens[h], Old: old})
}

func (s *Store) after(replace bool, n int) {
	if replace {
		if s.OnReplace != nil {
			s.OnReplace(n)
		}
	} else if s.OnActivity != nil {
		s.OnActivity()
	}
	if f := s.notify.Load(); f != nil {
		(*f)()
	}
}

// ---- cache.Store ----------------------------------------------------------------------

// SetNotify installs (or with nil removes) the callback invoked after every mutation.
func (s *Store) SetNotify(f func()) {
	if f == nil {
		s.notify.Store(nil)
		return
	}
	s.notify.Store(&f)
}

func (s *Store) Add(obj interface{}) error { return s.Update(obj) }
func (s *Store) Resync() error             { return nil }
func (s *Store) Bookmark(rv string)        { s.mu.Lock(); s.rv = rv; s.mu.Unlock(); s.after(false, 0) }
func (s *Store) LastStoreSyncResourceVersion() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.rv
}

func (s *Store) Update(obj interface{}) error {
	row, err := asRow(obj)
	if err != nil {
		return err
	}
	s.mu.Lock()
	s.upsertLocked(row)
	s.mu.Unlock()
	s.after(false, 0)
	return nil
}

func (s *Store) Delete(obj interface{}) error {
	row, err := asRow(obj)
	if err != nil {
		return err
	}
	s.mu.Lock()
	if h, ok := s.byUID[row.UID()]; ok {
		s.deleteLocked(h)
	}
	s.mu.Unlock()
	s.after(false, 0)
	return nil
}

// Replace diffs the list against the current contents: unchanged rows produce no events, changed
// rows one update, new rows an add, missing rows a delete.
func (s *Store) Replace(list []interface{}, rv string) error {
	rows := make([]*ingest.PodRow, 0, len(list))
	for _, o := range list {
		r, err := asRow(o)
		if err != nil {
			return err
		}
		rows = append(rows, r)
	}
	s.mu.Lock()
	s.epoch++
	if s.epoch == 0 { // wrapped: clear marks
		clear(s.mark)
		s.epoch = 1
	}
	if cap(s.feed)-len(s.feed) < len(rows) && len(s.rows) == 0 {
		s.feed = append(make([]Event, 0, len(s.feed)+len(rows)), s.feed...)
	}
	for _, r := range rows {
		h := s.upsertLocked(r)
		s.mark[h] = s.epoch
	}
	for h := range s.rows {
		if s.rows[h] != nil && s.mark[h] != s.epoch {
			s.deleteLocked(uint32(h))
		}
	}
	s.rv = rv
	s.synced = true
	s.mu.Unlock()
	s.after(true, len(rows))
	return nil
}

func (s *Store) List() []interface{} {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]interface{}, 0, len(s.byUID))
	for _, r := range s.rows {
		if r != nil {
			out = append(out, r)
		}
	}
	return out
}

func (s *Store) ListKeys() []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]string, 0, len(s.byUID))
	for k := range s.byUID {
		out = append(out, k)
	}
	return out
}

func (s *Store) Get(obj interface{}) (interface{}, bool, error) {
	row, err := asRow(obj)
	if err != nil {
		return nil, false, err
	}
	return s.GetByKey(row.UID())
}

func (s *Store) GetByKey(uid string) (interface{}, bool, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	h, ok := s.byUID[uid]
	if !ok {
		return nil, false, nil
	}
	return s.rows[h], true, nil
}

// ---- view-engine API ------------------------------------------------------------------

// Synced reports whether the first Replace completed.
func (s *Store) Synced() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.synced
}

func (s *Store) Len() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.byUID)
}

// Drain moves the change feed into buf (reusing its capacity). When the feed holds more than
// snapOver events and snap is non-nil, it also returns a consistent snapshot of all live rows
// (restricted to namespaces when non-empty) taken in the same critical section.
func (s *Store) Drain(buf []Event, snapOver int, snap []Item, namespaces []string) (events []Event, items []Item, snapshotted bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	events = append(buf[:0], s.feed...)
	clear(s.feed)
	s.feed = s.feed[:0]
	if snapOver >= 0 && len(events) >= snapOver {
		return events, s.snapshotLocked(snap, namespaces), true
	}
	return events, snap, false
}

// Snapshot returns all live rows (restricted to namespaces when non-empty), appended to dst.
func (s *Store) Snapshot(dst []Item, namespaces []string) []Item {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.snapshotLocked(dst, namespaces)
}

func (s *Store) snapshotLocked(dst []Item, namespaces []string) []Item {
	dst = dst[:0]
	if len(namespaces) > 0 {
		for _, n := range namespaces {
			if l := s.ns[unique.Make(n)]; l != nil {
				for _, h := range l.hs {
					dst = append(dst, Item{h, s.gens[h], s.rows[h]})
				}
			}
		}
		return dst
	}
	if cap(dst) < len(s.byUID) {
		dst = make([]Item, 0, len(s.byUID))
	}
	for h, r := range s.rows {
		if r != nil {
			dst = append(dst, Item{uint32(h), s.gens[h], r})
		}
	}
	return dst
}

// Row returns the live row for a handle if its generation still matches.
func (s *Store) Row(h, gen uint32) (*ingest.PodRow, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if int(h) < len(s.rows) && s.gens[h] == gen && s.rows[h] != nil {
		return s.rows[h], true
	}
	return nil, false
}

// Namespaces returns the namespaces that currently hold rows.
func (s *Store) Namespaces() []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]string, 0, len(s.ns))
	for n := range s.ns {
		out = append(out, n.Value())
	}
	return out
}

// NamespaceCount returns the number of rows in a namespace.
func (s *Store) NamespaceCount(ns string) int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if l := s.ns[unique.Make(ns)]; l != nil {
		return len(l.hs)
	}
	return 0
}

// Reset drops all contents (used when a cluster is released).
func (s *Store) Reset() {
	s.mu.Lock()
	s.rows, s.gens, s.pos, s.free, s.mark, s.feed = nil, nil, nil, nil, nil, nil
	s.byUID = make(map[string]uint32)
	s.ns = make(map[unique.Handle[string]]*nsList)
	s.mu.Unlock()
}

var ErrClosed = errors.New("store closed")
