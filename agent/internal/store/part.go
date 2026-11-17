package store

import (
	"sort"
	"sync"

	"github.com/zyno-io/v8s/agent/internal/ingest"
	"k8s.io/client-go/tools/cache"
)

// Part is one writer's slice of a shared Store: the cache.ReflectorStore of one object-events watch. Many
// Parts write into one Store; each touches only the uids it owns, so a Replace or Close never removes another
// writer's rows. A Part keeps at most max rows, the highest by rank (events: lastSeen). Callers must keep
// Parts' uid sets disjoint (the object-events selectors guarantee it).
//
// Lock order: Part.mu, then Store.mu. After Close every method is a no-op, so a late reflector write cannot
// resurrect rows or delete a successor's.
type Part struct {
	s    *Store
	max  int
	rank func(*ingest.Row) int64

	mu     sync.Mutex
	own    map[string]int64 // uid -> rank
	closed bool
	synced bool

	// OnSynced is called once, after the first Replace, outside mu.
	OnSynced func()
}

var _ cache.ReflectorStore = (*Part)(nil)

func NewPart(s *Store, max int, rank func(*ingest.Row) int64) *Part {
	return &Part{s: s, max: max, rank: rank, own: make(map[string]int64)}
}

func (p *Part) Add(obj interface{}) error { return p.Update(obj) }

func (p *Part) Update(obj interface{}) error {
	row, err := asRow(obj)
	if err != nil {
		return err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return nil
	}
	uid, rk := row.UID(), p.rank(row)
	var del []string
	if _, owned := p.own[uid]; !owned && p.max > 0 && len(p.own) >= p.max {
		minUID, minRank, first := "", int64(0), true
		for u, r := range p.own {
			if first || r < minRank {
				minUID, minRank, first = u, r, false
			}
		}
		if rk <= minRank {
			return nil // older than everything kept
		}
		delete(p.own, minUID)
		del = []string{minUID}
	}
	p.own[uid] = rk
	p.s.Apply([]*ingest.Row{row}, del)
	return nil
}

func (p *Part) Delete(obj interface{}) error {
	row, err := asRow(obj)
	if err != nil {
		return err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return nil
	}
	uid := row.UID()
	if _, owned := p.own[uid]; !owned {
		return nil
	}
	delete(p.own, uid)
	p.s.Apply(nil, []string{uid})
	return nil
}

// Replace swaps this part's rows for the list (the newest max of it).
func (p *Part) Replace(list []interface{}, rv string) error {
	rows := make([]*ingest.Row, 0, len(list))
	for _, o := range list {
		r, err := asRow(o)
		if err != nil {
			return err
		}
		rows = append(rows, r)
	}
	if p.max > 0 && len(rows) > p.max {
		sort.SliceStable(rows, func(i, j int) bool { return p.rank(rows[i]) > p.rank(rows[j]) })
		rows = rows[:p.max]
	}
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return nil
	}
	next := make(map[string]int64, len(rows))
	for _, r := range rows {
		next[r.UID()] = p.rank(r)
	}
	var del []string
	for uid := range p.own {
		if _, ok := next[uid]; !ok {
			del = append(del, uid)
		}
	}
	p.own = next
	p.s.Apply(rows, del)
	first := !p.synced
	p.synced = true
	p.mu.Unlock()
	if first && p.OnSynced != nil {
		p.OnSynced()
	}
	return nil
}

func (p *Part) Resync() error { return nil }

// Close removes this part's rows and turns every later write into a no-op.
func (p *Part) Close() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return
	}
	p.closed = true
	del := make([]string, 0, len(p.own))
	for uid := range p.own {
		del = append(del, uid)
	}
	p.own = nil
	p.s.Apply(nil, del)
}
