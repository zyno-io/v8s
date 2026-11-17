package problems

import (
	"container/heap"
	"context"
	"fmt"
	"log/slog"
	"slices"
	"sort"
	"strconv"
	"sync"
	"time"

	"github.com/zyno-io/v8s/agent/internal/ingest"
	"github.com/zyno-io/v8s/agent/internal/store"
	"github.com/zyno-io/v8s/agent/pkg/protocol"
)

// Defaults (docs/problems.md): evaluation interval and the hold before a resolved problem clears.
const (
	DefaultInterval = 250 * time.Millisecond
	DefaultHold     = 30 * time.Second
)

type nk struct{ ns, name string }

type table struct {
	mod  *ingest.Module
	st   *store.Store
	rows map[nk]*ingest.Row
}

type qev struct {
	gr       string
	old, new *ingest.Row
}

type ckey struct {
	det int
	k   Key
}

type mdraft struct{ d Draft }

type pstate struct {
	id      string
	det     int
	members map[ckey]*mdraft
	emptyAt time.Time
	pub     *published
	// armedUntil is the snooze deadline already pushed on the heap for this problem (0: none).
	armedUntil int64
}

type published struct {
	sub            Subject
	sev            Severity
	title, msg     string
	since, changed int64
	count          int
	source         string
	refs           []Ref
	acked          bool
	row            *ingest.Row
	owners         []string
}

type dlKind uint8

const (
	dlKey dlKind = iota
	dlPID
)

type deadline struct {
	at   time.Time
	kind dlKind
	k    Key
	pid  string
}

type dlHeap []deadline

func (h dlHeap) Len() int           { return len(h) }
func (h dlHeap) Less(i, j int) bool { return h[i].at.Before(h[j].at) }
func (h dlHeap) Swap(i, j int)      { h[i], h[j] = h[j], h[i] }
func (h *dlHeap) Push(x any)        { *h = append(*h, x.(deadline)) }
func (h *dlHeap) Pop() any {
	o := *h
	n := len(o)
	x := o[n-1]
	*h = o[:n-1]
	return x
}

// ClassCount is the live number of problems of a class in one cluster.
type ClassCount struct{ Total, Acked int }

// Engine is the problems engine of one cluster.
type Engine struct {
	h       *Hub
	cluster string
	st      *store.Store
	log     *slog.Logger

	qmu   sync.Mutex
	queue []qev

	mu       sync.Mutex
	dets     []*Detector
	byInput  map[string][]int
	byTrig   map[string][]int
	interest map[string]bool
	tables   map[string]*table
	enabled  []bool
	ready    []bool
	fullEval []bool
	disco    map[string]bool
	hasDisco bool
	dirty    map[Key]struct{}
	contribs map[ckey][]string
	first    map[ckey]map[string]time.Time // first time a finding without an evidence time was seen
	probs    map[string]*pstate
	touched  map[string]bool
	heap     dlHeap
	counts   []ClassCount
	idx      indexes
	rv       uint64
	umu      sync.Mutex // guards unavail (set from watch goroutines, read by passes)
	unavail  map[string]string
	aggOwner map[string]map[string]struct{} // owner key → pids of aggregates over that owner
	prog     map[Key]progState              // last observed rollout progress of workloads (Ctx.ProgressSince)

	// Now, Hold and Interval are injectable for tests.
	Now      func() time.Time
	Hold     time.Duration
	Interval time.Duration

	// LastPass records the cost of the latest pass (benchmarks, diagnostics).
	LastPass PassStats
}

// PassStats describes one evaluation pass.
type PassStats struct {
	Events, Dirty, Evals int
	Took                 time.Duration
}

func newEngine(h *Hub, cluster string) *Engine {
	e := &Engine{h: h, cluster: cluster, st: store.New(), log: h.log,
		byInput: map[string][]int{}, byTrig: map[string][]int{}, interest: map[string]bool{}, tables: map[string]*table{},
		dirty: map[Key]struct{}{}, contribs: map[ckey][]string{}, first: map[ckey]map[string]time.Time{}, probs: map[string]*pstate{}, touched: map[string]bool{},
		aggOwner: map[string]map[string]struct{}{}, prog: map[Key]progState{}, unavail: map[string]string{},
		Now: h.Now, Hold: h.Hold, Interval: h.Interval}
	e.idx.init()
	e.dets = Detectors()
	n := len(e.dets)
	e.enabled, e.ready, e.fullEval, e.counts = make([]bool, n), make([]bool, n), make([]bool, n), make([]ClassCount, n)
	for _, gr := range builtinGRs {
		e.interest[gr] = true
	}
	for i, d := range e.dets {
		for _, gr := range d.Inputs {
			e.byInput[gr] = append(e.byInput[gr], i)
			e.interest[gr] = true
		}
		for _, gr := range d.Triggers {
			e.byTrig[gr] = append(e.byTrig[gr], i)
			e.interest[gr] = true
		}
		e.enabled[i] = h.classEnabled(cluster, d)
	}
	e.st.SetNotify(nil)
	return e
}

// Store is the cluster's problems store, to be attached to the view engine as "v8s/problems".
func (e *Engine) Store() *store.Store { return e.st }

// Cluster is the cluster id.
func (e *Engine) Cluster() string { return e.cluster }

func (e *Engine) ctx() *Ctx { return &Ctx{e: e} }

// Interested reports whether the engine reads rows of gvr.
func (e *Engine) Interested(gvr string) bool { return e.interest[GR(gvr)] }

// Attach starts observing a store (tier-0 or baseline watch) of gvr: its current rows are replayed and every
// later change is queued for the next pass. It is a no-op for kinds no detector reads.
func (e *Engine) Attach(gvr string, st *store.Store) {
	gr := GR(gvr)
	if !e.interest[gr] {
		return
	}
	e.mu.Lock()
	t := e.tables[gr]
	if t == nil {
		t = &table{mod: ingest.Lookup(gvr), rows: map[nk]*ingest.Row{}}
		e.tables[gr] = t
	}
	t.st = st
	e.mu.Unlock()
	st.SetTap(func(old, new *ingest.Row) {
		e.qmu.Lock()
		e.queue = append(e.queue, qev{gr, old, new})
		e.qmu.Unlock()
	})
	st.Scan(func(r *ingest.Row) bool {
		e.qmu.Lock()
		e.queue = append(e.queue, qev{gr, nil, r})
		e.qmu.Unlock()
		return true
	})
}

// Detach stops observing gvr and drops its rows (the watch was stopped).
func (e *Engine) Detach(gvr string) {
	gr := GR(gvr)
	e.mu.Lock()
	t := e.tables[gr]
	var rows []*ingest.Row
	if t != nil {
		if t.st != nil {
			t.st.SetTap(nil)
		}
		for _, r := range t.rows {
			rows = append(rows, r)
		}
		t.st = nil
	}
	e.mu.Unlock()
	e.qmu.Lock()
	for _, r := range rows {
		e.queue = append(e.queue, qev{gr, r, nil})
	}
	e.qmu.Unlock()
}

// SetDiscovery tells the engine which GRs the cluster serves (class gates, "cert-manager is present").
func (e *Engine) SetDiscovery(grs map[string]bool) {
	e.mu.Lock()
	e.disco, e.hasDisco = grs, true
	e.mu.Unlock()
}

// SetUnavailable records that a baseline kind cannot be read (no permission), shown by problems.classes.
func (e *Engine) SetUnavailable(gr, reason string) {
	e.umu.Lock()
	if reason == "" {
		delete(e.unavail, gr)
	} else {
		e.unavail[gr] = reason
	}
	e.umu.Unlock()
}

func (e *Engine) isUnavailable(gr string) bool {
	e.umu.Lock()
	defer e.umu.Unlock()
	return e.unavail[gr] != ""
}

// Unavailable returns the kinds that could not be read, with the reason.
func (e *Engine) Unavailable() map[string]string {
	e.umu.Lock()
	defer e.umu.Unlock()
	out := make(map[string]string, len(e.unavail))
	for k, v := range e.unavail {
		out[k] = v
	}
	return out
}

// Counts returns the live problem counts per class.
func (e *Engine) Counts() map[string]ClassCount {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := map[string]ClassCount{}
	for i, c := range e.counts {
		if c.Total > 0 {
			out[e.dets[i].Class] = c
		}
	}
	return out
}

// ClassEnabled reports whether a class is enabled in this cluster (preferences and gates).
func (e *Engine) ClassEnabled(class string) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	for i, d := range e.dets {
		if d.Class == class {
			return e.enabled[i] && e.gateOK(d)
		}
	}
	return false
}

func (e *Engine) gateOK(d *Detector) bool {
	return d.Gate == "" || (e.hasDisco && e.disco[d.Gate]) || e.tables[d.Gate] != nil
}

// Run evaluates every Interval until ctx ends.
func (e *Engine) Run(ctx context.Context) {
	iv := e.Interval
	if iv <= 0 {
		iv = DefaultInterval
	}
	t := time.NewTicker(iv)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			e.Pass()
		}
	}
}

// Refresh re-reads the class preferences and acknowledgements (after settings changed).
func (e *Engine) Refresh() {
	e.mu.Lock()
	defer e.mu.Unlock()
	now := e.Now()
	for i, d := range e.dets {
		on := e.h.classEnabled(e.cluster, d)
		if on == e.enabled[i] {
			continue
		}
		e.enabled[i] = on
		if on {
			e.ready[i], e.fullEval[i] = false, true
		} else {
			e.dropClass(i, now)
		}
	}
	for _, p := range e.probs {
		if p.pub != nil {
			e.touched[p.id] = e.touched[p.id] || false
		}
	}
	e.flush(now)
}

// dropClass removes every problem of detector i at once (the class was disabled).
func (e *Engine) dropClass(i int, now time.Time) {
	for ck := range e.contribs {
		if ck.det == i {
			delete(e.contribs, ck)
		}
	}
	for id, p := range e.probs {
		if p.det != i {
			continue
		}
		clear(p.members)
		if len(p.members) == 0 {
			e.unpublish(p)
			delete(e.probs, id)
		} else {
			e.touched[id] = true
		}
	}
}

// ---- the pass ---------------------------------------------------------------------------

// Pass runs one evaluation pass: queued events update the tables and indexes and mark keys dirty (plus their
// dependents), due deadlines mark keys dirty, every dirty key is re-evaluated, and the changed problems are
// published.
func (e *Engine) Pass() {
	start := time.Now()
	e.qmu.Lock()
	batch := e.queue
	e.queue = nil
	e.qmu.Unlock()

	e.mu.Lock()
	defer e.mu.Unlock()
	now := e.Now()
	c := e.ctx()
	stats := PassStats{Events: len(batch)}
	for i := range batch {
		e.applyEvent(c, &batch[i])
	}
	for len(e.heap) > 0 && !e.heap[0].at.After(now) {
		d := heap.Pop(&e.heap).(deadline)
		switch d.kind {
		case dlKey:
			e.dirty[d.k] = struct{}{}
		case dlPID:
			e.touched[d.pid] = e.touched[d.pid] || false
		}
	}
	// detectors that just became ready (all their inputs synced) or were just enabled: evaluate everything
	for i, d := range e.dets {
		ok := e.enabled[i] && e.gateOK(d) && e.inputsReady(d)
		if ok && !e.ready[i] {
			e.fullEval[i] = true
		}
		e.ready[i] = ok
	}
	for i, d := range e.dets {
		if !e.fullEval[i] || !e.ready[i] {
			continue
		}
		e.fullEval[i] = false
		// contributions whose rows vanished while the detector could not see its inputs (a watch stopped and
		// restarted, the object deleted meanwhile) would otherwise stay forever
		for ck := range e.contribs {
			if ck.det != i || ck.k.GR == extGR {
				continue
			}
			if t := e.tables[ck.k.GR]; t == nil || t.rows[nk{ck.k.NS, ck.k.Name}] == nil {
				e.applyContrib(ck, nil, true, now)
			}
		}
		for _, gr := range d.Inputs {
			if t := e.tables[gr]; t != nil {
				for k, r := range t.rows {
					stats.Evals++
					e.evalDet(c, i, Key{gr, k.ns, k.name}, r, now)
				}
			}
		}
	}
	stats.Dirty = len(e.dirty)
	for k := range e.dirty {
		delete(e.dirty, k)
		var row *ingest.Row
		if t := e.tables[k.GR]; t != nil {
			row = t.rows[nk{k.NS, k.Name}]
		}
		for _, di := range e.byInput[k.GR] {
			if e.ready[di] {
				stats.Evals++
				e.evalDet(c, di, k, row, now)
			}
		}
	}
	e.flush(now)
	stats.Took = time.Since(start)
	e.LastPass = stats
}

func (e *Engine) inputsReady(d *Detector) bool {
	if d.Inputs == nil {
		return false // external classes are fed by Inject
	}
	// at least one input kind must be watched (a detector over several kinds works with those that exist: a
	// cluster without StatefulSets), every watched input must have synced; joined kinds (Triggers) must all be
	// watched and synced, else the class cannot judge (it is reported as unavailable instead of guessing).
	any := false
	for _, gr := range d.Inputs {
		if e.isUnavailable(gr) {
			continue // an unreadable kind (no permission, aggregated API down) is absent, not a reason to stay silent
		}
		if t := e.tables[gr]; t != nil && t.st != nil {
			any = true
			if !t.st.Synced() {
				return false
			}
		}
	}
	if !any {
		return false
	}
	for _, gr := range d.Triggers {
		if e.isUnavailable(gr) && slices.Contains(d.Optional, gr) {
			continue
		}
		t := e.tables[gr]
		if t == nil || t.st == nil || !t.st.Synced() {
			return false
		}
	}
	return true
}

func (e *Engine) applyEvent(c *Ctx, ev *qev) {
	t := e.tables[ev.gr]
	if t == nil {
		return
	}
	var k nk
	if ev.new != nil {
		k = nk{ev.new.GetNamespace(), ev.new.Name()}
	} else {
		k = nk{ev.old.GetNamespace(), ev.old.Name()}
	}
	old := t.rows[k]
	if ev.new == nil {
		if cur := t.rows[k]; cur != nil && cur.UID() != ev.old.UID() && ev.old.UID() != "" {
			return // a stale delete of a replaced object
		}
		delete(t.rows, k)
	} else {
		t.rows[k] = ev.new
	}
	key := Key{ev.gr, k.ns, k.name}
	e.idx.update(e, ev.gr, key, old, ev.new)
	for _, di := range e.byInput[ev.gr] {
		if e.enabled[di] {
			e.dirty[key] = struct{}{}
			break
		}
	}
	for _, di := range e.byTrig[ev.gr] {
		d := e.dets[di]
		if !e.enabled[di] || d.Dependents == nil {
			continue
		}
		for _, dk := range d.Dependents(c, key, old, ev.new) {
			e.dirty[dk] = struct{}{}
		}
	}
	for _, dk := range e.idx.dependents(e, ev.gr, key, old, ev.new) {
		e.dirty[dk] = struct{}{}
	}
}

func (e *Engine) evalDet(c *Ctx, di int, k Key, row *ingest.Row, now time.Time) {
	d := e.dets[di]
	ck := ckey{di, k}
	if row == nil {
		delete(e.prog, k)
		e.applyContrib(ck, nil, true, now)
		return
	}
	var drafts []Draft
	func() {
		defer func() {
			if r := recover(); r != nil {
				e.log.Error("detector panic", "class", d.Class, "key", fmt.Sprint(k), "panic", r)
			}
		}()
		drafts = d.Evaluate(c, k, row)
	}()
	e.applyContrib(ck, drafts, false, now)
}

// applyContrib replaces the findings contributed by one (detector, key) pair.
func (e *Engine) applyContrib(ck ckey, drafts []Draft, immediate bool, now time.Time) {
	d := e.dets[ck.det]
	var newPIDs []string
	var seenFirst map[string]time.Time
	seen := map[string]*mdraft{}
	for i := range drafts {
		dr := drafts[i]
		id := ID(e.cluster, d.Class, ownerOr(dr), dr.Disc)
		since := msToTime(dr.Since)
		if since.IsZero() { // no evidence time: when the engine first saw it (kept across passes, also while pending)
			since = e.first[ck][id]
			if since.IsZero() {
				since = now
			}
			dr.Since = since.UnixMilli()
			if seenFirst == nil {
				seenFirst = map[string]time.Time{}
			}
			seenFirst[id] = since
		}
		if dr.Grace > 0 {
			if at := since.Add(dr.Grace); at.After(now) {
				heap.Push(&e.heap, deadline{at: at, kind: dlKey, k: ck.k})
				continue
			}
		}
		if dr.TTL > 0 {
			at := since.Add(dr.TTL)
			if !at.After(now) {
				continue
			}
			heap.Push(&e.heap, deadline{at: at, kind: dlKey, k: ck.k})
		}
		for _, w := range dr.Wake {
			if w.After(now) {
				heap.Push(&e.heap, deadline{at: w, kind: dlKey, k: ck.k})
			}
		}
		if _, dup := seen[id]; !dup {
			newPIDs = append(newPIDs, id)
		}
		seen[id] = &mdraft{d: dr}
	}
	if seenFirst == nil {
		delete(e.first, ck)
	} else {
		e.first[ck] = seenFirst
	}
	for _, id := range e.contribs[ck] {
		if _, keep := seen[id]; keep {
			continue
		}
		if p := e.probs[id]; p != nil {
			delete(p.members, ck)
			e.touch(id, immediate)
		}
	}
	for id, m := range seen {
		p := e.probs[id]
		if p == nil {
			p = &pstate{id: id, det: ck.det, members: map[ckey]*mdraft{}}
			e.probs[id] = p
		}
		p.members[ck] = m
		e.touch(id, false)
	}
	if len(newPIDs) == 0 {
		delete(e.contribs, ck)
	} else {
		e.contribs[ck] = newPIDs
	}
}

func ownerOr(d Draft) Subject {
	if d.Owner != nil {
		return *d.Owner
	}
	return d.Subject
}

func (e *Engine) heapPushAckExpiry(id string, at time.Time) {
	heap.Push(&e.heap, deadline{at: at, kind: dlPID, pid: id})
}

func (e *Engine) touch(id string, immediate bool) { e.touched[id] = e.touched[id] || immediate }

// ---- external findings --------------------------------------------------------------------

const extGR = "ext"

// Inject replaces the findings of an external class (no row inputs: cluster.unreachable, the Hubble classes)
// contributed by source+key. Empty drafts withdraw them.
func (e *Engine) Inject(class, source, key string, drafts []Draft) {
	e.mu.Lock()
	defer e.mu.Unlock()
	for i, d := range e.dets {
		if d.Class != class {
			continue
		}
		now := e.Now()
		if !e.enabled[i] {
			drafts = nil
		}
		e.applyContrib(ckey{i, Key{extGR, source, key}}, drafts, false, now)
		e.flush(now)
		return
	}
}

// ---- publishing ---------------------------------------------------------------------------

func (e *Engine) flush(now time.Time) {
	if len(e.touched) == 0 {
		return
	}
	ids := make([]string, 0, len(e.touched))
	for id := range e.touched {
		ids = append(ids, id)
	}
	imm := e.touched
	e.touched = map[string]bool{}
	c := e.ctx()
	for _, id := range ids {
		if p := e.probs[id]; p != nil {
			e.recompute(c, p, imm[id], now)
		}
	}
}

func (e *Engine) recompute(c *Ctx, p *pstate, immediate bool, now time.Time) {
	d := e.dets[p.det]
	if len(p.members) == 0 {
		if p.pub == nil {
			delete(e.probs, p.id)
			return
		}
		if immediate { // the subject went away (deleted, class disabled): GitOps may recreate it, so the ack stays
			e.unpublish(p)
			delete(e.probs, p.id)
			return
		}
		if e.Hold > 0 {
			if p.emptyAt.IsZero() {
				p.emptyAt = now
				heap.Push(&e.heap, deadline{at: now.Add(e.Hold), kind: dlPID, pid: p.id})
				return
			}
			if now.Before(p.emptyAt.Add(e.Hold)) {
				return
			}
		}
		// resolved and stayed resolved for the hold: a recurrence is a new problem, without the old acknowledgement
		e.unpublish(p)
		e.h.ackResolved(p.id)
		delete(e.probs, p.id)
		return
	}
	p.emptyAt = time.Time{}
	cks := make([]ckey, 0, len(p.members))
	for ck := range p.members {
		cks = append(cks, ck)
	}
	ms := make([]Draft, 0, len(cks))
	for _, ck := range cks {
		ms = append(ms, p.members[ck].d)
	}
	slices.SortFunc(ms, func(a, b Draft) int {
		if x := compareStr(a.Subject.key(), b.Subject.key()); x != 0 {
			return x
		}
		return compareStr(a.Disc, b.Disc)
	})
	out := ms[0]
	if d.Aggregate != nil {
		func() {
			defer func() {
				if r := recover(); r != nil {
					e.log.Error("aggregate panic", "class", d.Class, "panic", r)
				}
			}()
			out = d.Aggregate(c, ms)
		}()
	}
	e.publish(p, d, &out, ms, now)
}

func compareStr(a, b string) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}

func (e *Engine) publish(p *pstate, d *Detector, out *Draft, ms []Draft, now time.Time) {
	sev := d.severity(out)
	since := out.Since
	if since == 0 {
		for i := range ms {
			if s := ms[i].Since; s != 0 && (since == 0 || s < since) {
				since = s
			}
		}
	}
	prev := p.pub
	pb := &published{sub: out.Subject, sev: sev, title: out.Title, msg: out.Message, since: since, count: out.Count, source: out.Source, refs: out.Evidence}
	if out.Owner != nil && out.Subject == (Subject{}) {
		pb.sub = *out.Owner
	}
	pb.changed = now.UnixMilli()
	if prev != nil && prev.sev == sev && prev.title == pb.title && prev.msg == pb.msg && prev.count == pb.count {
		pb.changed = prev.changed
	}
	held, until := e.h.ackHolds(p.id, now, sev.Rank(), pb.count)
	pb.acked = held
	if held && until > 0 && p.armedUntil != until { // a snooze loaded from the settings has no deadline yet
		p.armedUntil = until
		e.heapPushAckExpiry(p.id, time.UnixMilli(until))
	}
	pb.row = e.buildRow(p, d, pb)
	if prev != nil && prev.row != nil && prev.row.CellsEqual(pb.row) {
		p.pub = pb
		pb.row = prev.row
		e.setOwners(p, prev, pb, ms)
		return
	}
	p.pub = pb
	e.setOwners(p, prev, pb, ms)
	_ = e.st.Update(pb.row)
	if prev == nil {
		e.counts[p.det].Total++
		if pb.acked {
			e.counts[p.det].Acked++
		}
		return
	}
	if prev.acked != pb.acked {
		if pb.acked {
			e.counts[p.det].Acked++
		} else {
			e.counts[p.det].Acked--
		}
	}
}

func (e *Engine) unpublish(p *pstate) {
	if p.pub == nil {
		return
	}
	_ = e.st.Delete(p.pub.row)
	e.counts[p.det].Total--
	if p.pub.acked {
		e.counts[p.det].Acked--
	}
	e.clearOwners(p, p.pub)
	p.pub = nil
	p.armedUntil = 0
}

// setOwners records which workloads a problem aggregates over, so a change of a workload's pod count recomposes it.
func (e *Engine) setOwners(p *pstate, prev *published, pb *published, ms []Draft) {
	if prev != nil {
		e.clearOwners(p, prev)
	}
	pb.owners = pb.owners[:0]
	for i := range ms {
		if ms[i].Owner != nil {
			k := ms[i].Owner.key()
			set := e.aggOwner[k]
			if set == nil {
				set = map[string]struct{}{}
				e.aggOwner[k] = set
			}
			set[p.id] = struct{}{}
			pb.owners = append(pb.owners, k)
		}
	}
}

func (e *Engine) clearOwners(p *pstate, pb *published) {
	for _, ok := range pb.owners {
		if set := e.aggOwner[ok]; set != nil {
			delete(set, p.id)
			if len(set) == 0 {
				delete(e.aggOwner, ok)
			}
		}
	}
}

// refreshAck recomposes one problem after its acknowledgement changed.
func (e *Engine) refreshAck(id string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if p := e.probs[id]; p != nil && p.pub != nil {
		e.touched[id] = e.touched[id] || false
		e.flush(e.Now())
	}
}

func (e *Engine) buildRow(p *pstate, d *Detector, pb *published) *ingest.Row {
	e.rv++
	r := ingest.NewRow(p.id, pb.sub.Name, strconv.FormatUint(e.rv, 10), pb.sub.NS, 0, false, numCells)
	r.PutS(pb.sev.String())
	r.PutN(int64(pb.sev.Rank()))
	r.PutS(d.Class)
	r.PutS(d.Family)
	r.PutS(pb.sub.Kind)
	r.PutS(pb.title)
	r.PutS(pb.msg)
	r.PutN(pb.since)
	r.PutN(pb.changed)
	r.PutN(int64(pb.count))
	r.PutS(pb.source)
	if pb.acked {
		r.PutS("true")
	} else {
		r.PutS("false")
	}
	r.SetTone(pb.sev.Tone())
	return r
}

// Problem is the public view of one published problem (evidence requests, tests).
type Problem struct {
	ID, Class, Family string
	Cluster           string
	Subject           Subject
	Severity          Severity
	Title, Message    string
	Since, Changed    int64
	Count             int
	Source            string
	Acked             bool
	Evidence          []Ref
}

// Problems returns every published problem of the cluster sorted by id (tests, diagnostics).
func (e *Engine) Problems() []Problem {
	e.mu.Lock()
	defer e.mu.Unlock()
	var out []Problem
	for _, p := range e.probs {
		if pr, ok := e.problemLocked(p); ok {
			out = append(out, pr)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// Problem returns one published problem.
func (e *Engine) Problem(id string) (Problem, bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if p := e.probs[id]; p != nil {
		return e.problemLocked(p)
	}
	return Problem{}, false
}

func (e *Engine) problemLocked(p *pstate) (Problem, bool) {
	if p.pub == nil {
		return Problem{}, false
	}
	d := e.dets[p.det]
	pb := p.pub
	return Problem{ID: p.id, Class: d.Class, Family: d.Family, Cluster: e.cluster, Subject: pb.sub, Severity: pb.sev, Title: pb.title,
		Message: pb.msg, Since: pb.since, Changed: pb.changed, Count: pb.count, Source: pb.source, Acked: pb.acked, Evidence: pb.refs}, true
}

// SetPhase feeds the cluster's connection state: cluster.unreachable while the agent cannot reach the API
// server or its credentials were rejected.
func (e *Engine) SetPhase(phase protocol.ClusterPhase, msg string) {
	var drafts []Draft
	switch phase {
	case protocol.PhaseUnreachable, protocol.PhaseReconnecting, protocol.PhaseAuthRequired:
		if msg == "" {
			msg = string(phase)
		}
		drafts = []Draft{{Subject: Subject{Kind: "Cluster", Name: e.cluster}, Title: "Cluster " + e.cluster + " is unreachable", Message: msg}}
	}
	e.Inject("cluster.unreachable", "agent", "", drafts)
}
