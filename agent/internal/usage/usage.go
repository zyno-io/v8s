// Package usage turns metrics.k8s.io lists into handle-indexed snapshots the view engine joins to store rows
// when it sorts and encodes. Rows stay immutable: a poll produces a new Snap, never a row update.
package usage

import (
	"time"

	"github.com/zyno-io/v8s/agent/internal/store"
	metricsv1beta1 "k8s.io/metrics/pkg/apis/metrics/v1beta1"
)

// VerBase is the first Slot.Ver a runner hands out (seq starts here).
//
// Ver invariant: seq starts at VerBase per runner and only increases. A slot's Ver never decreases while it is
// non-zero; it only drops to 0 when the snapshot is dropped. That makes row.Ver()+slot.Ver change whenever the
// content a subscription encodes changes (the wire version, docs/performance.md). A collision would need at
// least 65,536 row updates between two sends of the same id.
const VerBase uint32 = 1 << 16

// PodSample is one PodMetrics item reduced to what the table shows.
type PodSample struct {
	NS, Name string
	At       int64  // unix ms (the metrics timestamp)
	CPU      uint32 // millicores: ceil(sum of container nanocores / 1e6), as kubectl top
	MemKi    uint32 // KiB
}

// NodeSample is one NodeMetrics item.
type NodeSample struct {
	Name  string
	At    int64
	CPU   uint32
	MemKi uint32
}

// NodeAlloc is a node's allocatable amounts (a pod's %N denominator).
type NodeAlloc struct{ CPU, MemMi uint32 }

// Slot is one row's usage, indexed by store handle.
type Slot struct {
	Gen       uint32 // store handle generation: a reused handle never inherits usage
	Ver       uint32 // poll seq when this slot's values last changed (>= VerBase; 0 = no slot)
	CPU       uint32 // millicores
	MemKi     uint32
	NodeCPU   uint32 // pods: allocatable of the pod's node; nodes: own allocatable; 0 unknown
	NodeMemMi uint32
	Has       bool // a sample exists (false: in scope, but metrics-server reports nothing)
}

// Snap is the usage of one (cluster, kind) at one poll. It is immutable once built.
type Snap struct {
	Store *store.Store // the store the handles belong to
	Seq   uint32
	At    time.Time
	Scope []string // namespaces covered, nil = all
	Slots []Slot   // indexed by store handle
}

// Lookup returns the slot of handle h, or nil unless h is in range, the generation matches and the slot is live.
func (s *Snap) Lookup(h, gen uint32) *Slot {
	if s == nil || int(h) >= len(s.Slots) {
		return nil
	}
	sl := &s.Slots[h]
	if sl.Ver == 0 || sl.Gen != gen {
		return nil
	}
	return sl
}

// Covers reports whether every namespace of ns is in the snapshot's scope (an empty ns means all namespaces).
func (s *Snap) Covers(ns []string) bool {
	if s == nil {
		return false
	}
	if s.Scope == nil {
		return true
	}
	if len(ns) == 0 {
		return false
	}
next:
	for _, n := range ns {
		for _, c := range s.Scope {
			if c == n {
				continue next
			}
		}
		return false
	}
	return true
}

// FromPodMetrics reduces a decoded PodMetricsList to samples.
func FromPodMetrics(l *metricsv1beta1.PodMetricsList) []PodSample {
	out := make([]PodSample, len(l.Items))
	for i := range l.Items {
		pm := &l.Items[i]
		var nano, mem int64
		for j := range pm.Containers {
			u := pm.Containers[j].Usage
			if q, ok := u["cpu"]; ok {
				nano += q.ScaledValue(-9)
			}
			if q, ok := u["memory"]; ok {
				mem += q.Value()
			}
		}
		out[i] = PodSample{NS: pm.Namespace, Name: pm.Name, At: pm.Timestamp.UnixMilli(), CPU: milli(nano), MemKi: kib(mem)}
	}
	return out
}

// FromNodeMetrics reduces a decoded NodeMetricsList to samples.
func FromNodeMetrics(l *metricsv1beta1.NodeMetricsList) []NodeSample {
	out := make([]NodeSample, len(l.Items))
	for i := range l.Items {
		nm := &l.Items[i]
		var nano, mem int64
		if q, ok := nm.Usage["cpu"]; ok {
			nano = q.ScaledValue(-9)
		}
		if q, ok := nm.Usage["memory"]; ok {
			mem = q.Value()
		}
		out[i] = NodeSample{Name: nm.Name, At: nm.Timestamp.UnixMilli(), CPU: milli(nano), MemKi: kib(mem)}
	}
	return out
}

func milli(nano int64) uint32 {
	if nano <= 0 {
		return 0
	}
	m := (nano + 999_999) / 1_000_000 // ceil, as kubectl top
	if m > 1<<32-1 {
		return 1<<32 - 1
	}
	return uint32(m)
}

func kib(b int64) uint32 {
	if b <= 0 {
		return 0
	}
	k := b >> 10
	if k > 1<<32-1 {
		return 1<<32 - 1
	}
	return uint32(k)
}

type podKey struct{ ns, name string }

// BuildPods joins pod samples to the store items of one pod store. items must be a snapshot of st (restricted
// to scope, nil = all); the slots are indexed by store handle. nodes maps node name to allocatable (may be nil).
// prev, when built from the same store, lets unchanged slots keep their Ver so only changed rows are re-sent.
func BuildPods(st *store.Store, items []store.Item, scope []string, s []PodSample, nodes map[string]NodeAlloc, prev *Snap, seq uint32) *Snap {
	by := make(map[podKey]*PodSample, len(s))
	for i := range s {
		by[podKey{s[i].NS, s[i].Name}] = &s[i]
	}
	sn := &Snap{Store: st, Seq: seq, At: time.Now(), Scope: scope, Slots: make([]Slot, maxHandle(items))}
	var p []Slot
	if prev != nil && prev.Store == st {
		p = prev.Slots
	}
	for i := range items {
		it := &items[i]
		row := it.Row
		sl := &sn.Slots[it.H]
		sl.Gen = it.Gen
		if m := by[podKey{row.NS().Value(), row.Name()}]; m != nil && m.At >= row.CreatedMs() {
			// A sample older than the row belongs to an earlier pod of the same name (a recreated StatefulSet pod).
			sl.Has, sl.CPU, sl.MemKi = true, m.CPU, m.MemKi
		}
		if len(nodes) > 0 {
			if n, ok := nodes[row.Node().Value()]; ok {
				sl.NodeCPU, sl.NodeMemMi = n.CPU, n.MemMi
			}
		}
		stamp(sl, p, it.H, seq)
	}
	return sn
}

// BuildNodes joins node samples to the items of the node store; each slot also carries the node's own
// allocatable in NodeCPU/NodeMemMi, so %CPU of a node is computed by the same code as a pod's %N.
func BuildNodes(st *store.Store, items []store.Item, s []NodeSample, prev *Snap, seq uint32) *Snap {
	by := make(map[string]*NodeSample, len(s))
	for i := range s {
		by[s[i].Name] = &s[i]
	}
	sn := &Snap{Store: st, Seq: seq, At: time.Now(), Slots: make([]Slot, maxHandle(items))}
	var p []Slot
	if prev != nil && prev.Store == st {
		p = prev.Slots
	}
	for i := range items {
		it := &items[i]
		sl := &sn.Slots[it.H]
		sl.Gen = it.Gen
		if m := by[it.Row.Name()]; m != nil {
			sl.Has, sl.CPU, sl.MemKi = true, m.CPU, m.MemKi
		}
		cpu, mem := it.Row.Allocatable()
		sl.NodeCPU, sl.NodeMemMi = cpu, uint32(min(mem>>20, 1<<32-1))
		stamp(sl, p, it.H, seq)
	}
	return sn
}

func maxHandle(items []store.Item) int {
	n := 0
	for i := range items {
		if h := int(items[i].H) + 1; h > n {
			n = h
		}
	}
	return n
}

// stamp sets sl.Ver: inherited from the previous snapshot's slot when it is for the same generation and
// carries identical values, otherwise seq.
func stamp(sl *Slot, prev []Slot, h, seq uint32) {
	if int(h) < len(prev) {
		if o := &prev[h]; o.Ver != 0 {
			c := *sl
			c.Ver = o.Ver
			if c == *o {
				sl.Ver = o.Ver
				return
			}
		}
	}
	sl.Ver = seq
}
