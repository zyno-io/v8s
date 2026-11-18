package usage

import (
	"strconv"

	"github.com/zyno-io/v8s/agent/internal/ingest"
	"github.com/zyno-io/v8s/agent/internal/store"
	"github.com/zyno-io/v8s/agent/pkg/protocol"
)

// Cell indexes of the capacity module (after cluster and name).
const (
	CellNodes = iota
	CellNodesReporting
	CellCPUAlloc // millicores
	CellMemAlloc // MiB
	CellCPUReq
	CellMemReq
	CellCPULim
	CellMemLim
	CellCPUUse // millicores; -1 = unknown (no node metrics)
	CellMemUse // MiB; -1 = unknown
	CellPods
	CellPodsNoCPULim
	CellPodsNoMemLim
	numCapCells
)

// init registers the synthetic per-cluster capacity view behind the Overview CPU/Memory card: one row per
// cluster. It is not Virtual and not Curated, so it never appears in kinds.list.
func init() {
	ingest.Register(&ingest.Module{
		GVR: protocol.CapacityGVR, Kind: "Capacity", Synthetic: true, Usage: "capacity",
		DefaultSort: []protocol.Sort{{Col: "name"}},
		Cols: []ingest.Col{
			ingest.ColCluster(), ingest.ColName(),
			ingest.ColInt("nodes", "Nodes", 60), ingest.ColInt("nodesReporting", "Nodes reporting", 60),
			ingest.ColInt("cpuAlloc", "CPU allocatable (m)", 80), ingest.ColInt("memAlloc", "Memory allocatable (MiB)", 80),
			ingest.ColInt("cpuReq", "CPU requests (m)", 80), ingest.ColInt("memReq", "Memory requests (MiB)", 80),
			ingest.ColInt("cpuLim", "CPU limits (m)", 80), ingest.ColInt("memLim", "Memory limits (MiB)", 80),
			ingest.ColInt("cpuUse", "CPU usage (m)", 80), ingest.ColInt("memUse", "Memory usage (MiB)", 80),
			ingest.ColInt("podsCounted", "Pods counted", 60), ingest.ColInt("podsNoCPULim", "Pods without CPU limit", 60),
			ingest.ColInt("podsNoMemLim", "Pods without memory limit", 60),
		},
	})
}

// Capacity is one cluster's resource totals: the denominator is the allocatable of every node, control-plane and
// cordoned ones included (as kubectl describe nodes); requests and limits are those of the pods bound to a node
// that have not finished.
type Capacity struct {
	Nodes, NodesReporting      uint64
	CPUAlloc, MemAlloc         uint64 // millicores, bytes
	CPUReq, MemReq             uint64
	CPULim, MemLim             uint64
	CPUUse, MemUse             uint64 // millicores, bytes; valid only when HasUse
	HasUse                     bool
	Pods, PodsNoCPU, PodsNoMem uint64
}

// ScanCapacity aggregates the pod and node stores in one pass each: 50k pods take a few milliseconds, which is
// why it runs as a throttled scan (at most every 2 s, only while an Overview is open) instead of tapping the
// stores. nodeUse is the node usage snapshot (nil: none); it counts only when built from the same node store.
// buf is scratch for the node snapshot.
func ScanCapacity(pods, nodes *store.Store, nodeUse *Snap, buf *[]store.Item) Capacity {
	var c Capacity
	if nodes != nil {
		if nodeUse != nil && nodeUse.Store == nodes {
			*buf = nodes.Snapshot((*buf)[:0], nil)
			for i := range *buf {
				it := &(*buf)[i]
				cpu, mem := it.Row.Allocatable()
				c.Nodes++
				c.CPUAlloc += uint64(cpu)
				c.MemAlloc += mem
				if sl := nodeUse.Lookup(it.H, it.Gen); sl != nil && sl.Has {
					c.NodesReporting++
					c.CPUUse += uint64(sl.CPU)
					c.MemUse += uint64(sl.MemKi) << 10
				}
			}
			clear(*buf)
			*buf = (*buf)[:0]
			c.HasUse = c.NodesReporting > 0
		} else {
			nodes.Scan(func(r *ingest.Row) bool {
				cpu, mem := r.Allocatable()
				c.Nodes++
				c.CPUAlloc += uint64(cpu)
				c.MemAlloc += mem
				return true
			})
		}
	}
	if pods != nil {
		pods.Scan(func(r *ingest.Row) bool {
			if r.Terminal() || r.Node().Value() == "" {
				return true
			}
			rc, rm := r.Requests()
			lc, lm := r.Limits()
			c.Pods++
			c.CPUReq += uint64(rc)
			c.MemReq += rm
			if lc != 0 {
				c.CPULim += uint64(lc)
			} else {
				c.PodsNoCPU++
			}
			if lm != 0 {
				c.MemLim += lm
			} else {
				c.PodsNoMem++
			}
			return true
		})
	}
	return c
}

// Row builds the cluster's capacity row (uid capacity/<id>, name <id>). rv only makes the row's resource version
// distinct; a row with identical cells is not re-published by the store.
func (c Capacity) Row(id string, rv uint64) *ingest.Row {
	r := ingest.NewRow("capacity/"+id, id, strconv.FormatUint(rv, 10), "", 0, false, numCapCells)
	use := func(v uint64) int64 {
		if !c.HasUse {
			return -1
		}
		return int64(v)
	}
	for _, n := range []int64{
		int64(c.Nodes), int64(c.NodesReporting), int64(c.CPUAlloc), int64(c.MemAlloc >> 20),
		int64(c.CPUReq), int64(c.MemReq >> 20), int64(c.CPULim), int64(c.MemLim >> 20),
		use(c.CPUUse), use(c.MemUse >> 20),
		int64(c.Pods), int64(c.PodsNoCPU), int64(c.PodsNoMem),
	} {
		r.PutN(n)
	}
	return r
}
