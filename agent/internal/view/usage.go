package view

import (
	"github.com/zyno-io/v8s/agent/internal/ingest"
	"github.com/zyno-io/v8s/agent/internal/usage"
)

// Usage ranks: a value sorts before n/a, which sorts before none, in both sort directions.
const (
	rankVal  uint8 = 0
	rankNA   uint8 = 1 // a denominator is missing (no request, no limit, unknown node)
	rankNone uint8 = 2 // no data (no slot, or metrics-server reports nothing for the row)
)

// usageOrd returns the value of a SrcUsage / SrcRes column of one row in sort units (CPU millicores, memory
// KiB, percentages per-mille) and its rank. slot is nil when the view has no usage for the row.
func usageOrd(k ingest.UseKind, row *ingest.Row, slot *usage.Slot) (val uint64, rank uint8) {
	switch k {
	case ingest.ResCPUReq:
		c, _ := row.Requests()
		return resOrd(uint64(c))
	case ingest.ResCPULim:
		c, _ := row.Limits()
		return resOrd(uint64(c))
	case ingest.ResMemReq:
		_, m := row.Requests()
		return resOrd(m >> 10)
	case ingest.ResMemLim:
		_, m := row.Limits()
		return resOrd(m >> 10)
	}
	if slot == nil || !slot.Has {
		return 0, rankNone
	}
	switch k {
	case ingest.UseCPU:
		return uint64(slot.CPU), rankVal
	case ingest.UseMem:
		return uint64(slot.MemKi), rankVal
	case ingest.UseCPUPctReq:
		c, _ := row.Requests()
		return pct(uint64(slot.CPU), uint64(c))
	case ingest.UseCPUPctLim:
		c, _ := row.Limits()
		return pct(uint64(slot.CPU), uint64(c))
	case ingest.UseCPUPctNode:
		return pct(uint64(slot.CPU), uint64(slot.NodeCPU))
	case ingest.UseMemPctReq:
		_, m := row.Requests()
		return pct(uint64(slot.MemKi), m>>10)
	case ingest.UseMemPctLim:
		_, m := row.Limits()
		return pct(uint64(slot.MemKi), m>>10)
	case ingest.UseMemPctNode:
		return pct(uint64(slot.MemKi), uint64(slot.NodeMemMi)<<10)
	}
	return 0, rankNone
}

func resOrd(v uint64) (uint64, uint8) {
	if v == 0 {
		return 0, rankNone
	}
	return v, rankVal
}

// pct is use/den in per-mille, rounded half up; den == 0 is n/a. Both are in the same unit.
func pct(use, den uint64) (uint64, uint8) {
	if den == 0 {
		return 0, rankNA
	}
	return (use*1000 + den/2) / den, rankVal
}

// slot returns the usage slot of an entry, nil when the view holds no snapshot for its cluster or the handle
// no longer matches (a reused handle never inherits usage). Callers hold e.mu.
func (v *View) slot(en *entry) *usage.Slot {
	if int(en.cl) >= len(v.use) {
		return nil
	}
	return v.use[en.cl].Lookup(en.h, en.gen)
}

// setUse swaps the view's snapshot of one cluster (nil clears). Callers hold e.mu and, for a view sorted by
// usage, re-sort afterwards in the same critical section (invariant 2).
func (v *View) setUse(cl uint16, sn *usage.Snap) {
	for int(cl) >= len(v.use) {
		v.use = append(v.use, nil)
	}
	v.use[cl] = sn
}

// SetUsage publishes the usage snapshot of (cluster, gvr); nil clears it. It is ignored unless the pair is
// attached and sn was built from the attached store (invariant 1: a snapshot applies only to the store it was
// built from). Views sorted by a usage column are re-sorted in the same critical section as the swap
// (invariant 2); every other view only gets its usage-showing subscriptions marked dirty.
func (e *Engine) SetUsage(cluster, gvr string, sn *usage.Snap) {
	e.mu.Lock()
	cl, ok1 := e.idx[cluster]
	gv, ok2 := e.gvrs[gvr]
	var rec *srcRec
	if ok1 && ok2 {
		rec = e.srcs[srcKey{cl, gv}]
	}
	if rec == nil || !rec.attached || (sn != nil && sn.Store != rec.st) || rec.use == sn {
		e.mu.Unlock()
		return
	}
	rec.use = sn
	for _, v := range e.views {
		if v.gv != gv {
			continue
		}
		v.setUse(cl, sn)
		if v.useSort {
			v.sort(v.entries)
		}
		for s := range v.subs {
			if v.useSort || s.showUsage {
				s.dirty.Store(true)
			}
		}
	}
	e.mu.Unlock()
	e.notify()
}

// SetUsage sets whether the subscription encodes usage columns (false: they are nil). It marks the
// subscription dirty when the flag changed.
func (s *Sub) SetUsage(show bool) {
	s.e.mu.Lock()
	defer s.e.mu.Unlock()
	if s.showUsage != show {
		s.showUsage = show
		s.dirty.Store(true)
	}
}

// wireVer is the version the subscription sends and diffs for a window entry: row.Ver() changes with every
// row update and slot.Ver with every change of the slot's values, so the sum changes whenever any encoded cell
// does (the usage.VerBase invariant). A subscription that does not show usage sees row.Ver() alone (invariant 3).
func (s *Sub) wireVer(en *entry) (ver uint32, sl *usage.Slot) {
	if !s.showUsage {
		return en.row.Ver(), nil
	}
	sl = s.v.slot(en)
	if sl == nil {
		return en.row.Ver(), nil
	}
	return en.row.Ver() + sl.Ver, sl
}
