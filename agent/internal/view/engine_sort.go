package view

import (
	"cmp"
	"runtime"
	"slices"
	"strings"
	"sync"

	"github.com/zyno-io/v8s/agent/internal/ingest"
)

// entry is one row of a materialized view. row is immutable, so an entry is a consistent snapshot.
type entry struct {
	row  *ingest.PodRow
	pfx  uint64 // first 8 bytes of the name, big-endian, zero padded: decides most name compares without touching the row
	cl   uint16
	rank uint8 // severity rank (default sort)
	h    uint32
	gen  uint32
}

func namePrefix(s string) uint64 {
	var v uint64
	for i := 0; i < 8; i++ {
		v <<= 8
		if i < len(s) {
			v |= uint64(s[i])
		}
	}
	return v
}

func mkEntry(row *ingest.PodRow, cl uint16, h, gen uint32) entry {
	return entry{row: row, pfx: namePrefix(row.Name()), cl: cl, rank: row.SeverityRank(), h: h, gen: gen}
}

// cmpName compares names in byte order using the cached prefix first.
func cmpName(a, b *entry) int {
	if a.pfx != b.pfx {
		if a.pfx < b.pfx {
			return -1
		}
		return 1
	}
	return strings.Compare(a.row.Name(), b.row.Name())
}

type cmpFn func(a, b *entry) int

// buildMatch compiles the query's membership predicate.
func (e *Engine) buildMatch(n *Norm) func(r *ingest.PodRow, cl uint16) bool {
	var preds []func(r *ingest.PodRow, cl uint16) bool
	if len(n.Clusters) > 0 {
		set := n.Clusters
		preds = append(preds, func(_ *ingest.PodRow, cl uint16) bool {
			_, ok := slices.BinarySearch(set, e.names[cl])
			return ok
		})
	}
	if n.nsSet != nil {
		set := n.nsSet
		preds = append(preds, func(r *ingest.PodRow, _ uint16) bool {
			_, ok := set[r.NS().Value()]
			return ok
		})
	}
	if n.Text != "" {
		t := n.Text
		preds = append(preds, func(r *ingest.PodRow, _ uint16) bool { return containsFold(r.Name(), t) })
	}
	for _, f := range n.filters {
		f := f
		switch f.col {
		case "statusTone":
			preds = append(preds, func(r *ingest.PodRow, _ uint16) bool { return f.toneMask&(1<<r.Tone()) != 0 })
		case "status":
			preds = append(preds, func(r *ingest.PodRow, _ uint16) bool { return slices.Contains(f.vals, r.Status().Value()) })
		case "node":
			preds = append(preds, func(r *ingest.PodRow, _ uint16) bool { return slices.Contains(f.vals, r.Node().Value()) })
		case "namespace":
			preds = append(preds, func(r *ingest.PodRow, _ uint16) bool { return slices.Contains(f.vals, r.NS().Value()) })
		case "cluster":
			preds = append(preds, func(_ *ingest.PodRow, cl uint16) bool { return slices.Contains(f.vals, e.names[cl]) })
		case "name":
			if f.op == "contains" {
				preds = append(preds, func(r *ingest.PodRow, _ uint16) bool {
					for _, v := range f.vals {
						if containsFold(r.Name(), v) {
							return true
						}
					}
					return false
				})
			} else {
				preds = append(preds, func(r *ingest.PodRow, _ uint16) bool { return slices.Contains(f.vals, r.Name()) })
			}
		}
	}
	switch len(preds) {
	case 0:
		return func(*ingest.PodRow, uint16) bool { return true }
	case 1:
		return preds[0]
	}
	return func(r *ingest.PodRow, cl uint16) bool {
		for _, p := range preds {
			if !p(r, cl) {
				return false
			}
		}
		return true
	}
}

func cmpStr(a, b string) int { return strings.Compare(a, b) }

func cmpHandle(a, b ingest.Str) int {
	if a == b {
		return 0
	}
	return strings.Compare(a.Value(), b.Value())
}

func dir(c int, desc bool) int {
	if desc {
		return -c
	}
	return c
}

// buildCmp compiles the query's total order: sort keys, then name, namespace, cluster, handle.
func (e *Engine) buildCmp(n *Norm) cmpFn {
	var keys []cmpFn
	for _, k := range n.sort {
		desc := k.desc
		switch k.col {
		case "cluster":
			keys = append(keys, func(a, b *entry) int { return dir(cmpStr(e.names[a.cl], e.names[b.cl]), desc) })
		case "name":
			keys = append(keys, func(a, b *entry) int { return dir(cmpName(a, b), desc) })
		case "namespace":
			keys = append(keys, func(a, b *entry) int { return dir(cmpHandle(a.row.NS(), b.row.NS()), desc) })
		case "status":
			keys = append(keys, func(a, b *entry) int { return dir(cmpHandle(a.row.Status(), b.row.Status()), desc) })
		case "node":
			keys = append(keys, func(a, b *entry) int { return dir(cmpHandle(a.row.Node(), b.row.Node()), desc) })
		case "statusTone":
			keys = append(keys, func(a, b *entry) int {
				return dir(cmp.Compare(a.rank, b.rank), desc)
			})
		case "restarts":
			keys = append(keys, func(a, b *entry) int { return dir(cmp.Compare(a.row.Restarts(), b.row.Restarts()), desc) })
		case "createdAt":
			keys = append(keys, func(a, b *entry) int { return dir(cmp.Compare(a.row.CreatedMs(), b.row.CreatedMs()), desc) })
		}
	}
	// Fast path for the default order.
	def := len(n.sort) == 2 && n.sort[0] == sortKey{col: "statusTone"} && n.sort[1] == sortKey{col: "name"}
	tail := func(a, b *entry) int {
		if c := cmpName(a, b); c != 0 {
			return c
		}
		if c := cmpHandle(a.row.NS(), b.row.NS()); c != 0 {
			return c
		}
		if c := cmp.Compare(a.cl, b.cl); c != 0 {
			return c
		}
		return cmp.Compare(a.h, b.h)
	}
	if def {
		return func(a, b *entry) int {
			if a.rank != b.rank {
				return int(a.rank) - int(b.rank)
			}
			return tail(a, b)
		}
	}
	return func(a, b *entry) int {
		for _, k := range keys {
			if c := k(a, b); c != 0 {
				return c
			}
		}
		return tail(a, b)
	}
}

// sortEntries sorts in place; large inputs are sorted as parallel runs and merged.
func sortEntries(es []entry, c cmpFn) {
	n := len(es)
	par := min(runtime.GOMAXPROCS(0), 8)
	if n < 16384 || par < 2 {
		slices.SortFunc(es, func(a, b entry) int { return c(&a, &b) })
		return
	}
	chunk := (n + par - 1) / par
	var runs [][2]int
	var wg sync.WaitGroup
	for lo := 0; lo < n; lo += chunk {
		hi := min(lo+chunk, n)
		runs = append(runs, [2]int{lo, hi})
		wg.Add(1)
		go func() {
			defer wg.Done()
			slices.SortFunc(es[lo:hi], func(a, b entry) int { return c(&a, &b) })
		}()
	}
	wg.Wait()
	buf := make([]entry, n)
	src, dst := es, buf
	for len(runs) > 1 {
		var next [][2]int
		for i := 0; i < len(runs); i += 2 {
			if i+1 == len(runs) {
				copy(dst[runs[i][0]:runs[i][1]], src[runs[i][0]:runs[i][1]])
				next = append(next, runs[i])
				continue
			}
			a, b := runs[i], runs[i+1]
			wg.Add(1)
			go func() {
				defer wg.Done()
				mergeInto(dst[a[0]:b[1]], src[a[0]:a[1]], src[b[0]:b[1]], c)
			}()
			next = append(next, [2]int{a[0], b[1]})
		}
		wg.Wait()
		src, dst = dst, src
		runs = next
	}
	if &src[0] != &es[0] {
		copy(es, src)
	}
}

func mergeInto(dst, a, b []entry, c cmpFn) {
	i, j, k := 0, 0, 0
	for i < len(a) && j < len(b) {
		if c(&b[j], &a[i]) < 0 {
			dst[k] = b[j]
			j++
		} else {
			dst[k] = a[i]
			i++
		}
		k++
	}
	k += copy(dst[k:], a[i:])
	copy(dst[k:], b[j:])
}

func sortSerial(es []entry, c cmpFn) {
	slices.SortFunc(es, func(a, b entry) int { return c(&a, &b) })
}

// keyFn derives a 64-bit prefix key such that key(a) < key(b) implies a sorts before b; equal keys
// fall through to the full comparison. Only the default order and plain name order have one.
type keyFn func(e *entry) uint64

func (e *Engine) buildKey(n *Norm) keyFn {
	def := len(n.sort) == 2 && n.sort[0] == sortKey{col: "statusTone"} && n.sort[1] == sortKey{col: "name"}
	if def {
		return func(e *entry) uint64 { return uint64(e.rank)<<56 | e.pfx>>8 }
	}
	if len(n.sort) == 1 && n.sort[0] == (sortKey{col: "name"}) {
		return func(e *entry) uint64 { return e.pfx }
	}
	return nil
}

type kitem struct {
	k uint64
	i uint32
}

// sortKeyed sorts es by (key, full comparison) using compact 16-byte items so most comparisons
// are a single integer compare.
func sortKeyed(es []entry, c cmpFn, key keyFn) {
	n := len(es)
	items := make([]kitem, n)
	for i := range es {
		items[i] = kitem{key(&es[i]), uint32(i)}
	}
	cmpItem := func(a, b kitem) int {
		if a.k != b.k {
			if a.k < b.k {
				return -1
			}
			return 1
		}
		return c(&es[a.i], &es[b.i])
	}
	par := min(runtime.GOMAXPROCS(0), 8)
	if n < 16384 || par < 2 {
		slices.SortFunc(items, cmpItem)
	} else {
		chunk := (n + par - 1) / par
		var runs [][2]int
		var wg sync.WaitGroup
		for lo := 0; lo < n; lo += chunk {
			hi := min(lo+chunk, n)
			runs = append(runs, [2]int{lo, hi})
			wg.Add(1)
			go func() {
				defer wg.Done()
				slices.SortFunc(items[lo:hi], cmpItem)
			}()
		}
		wg.Wait()
		buf := make([]kitem, n)
		src, dst := items, buf
		for len(runs) > 1 {
			var next [][2]int
			for i := 0; i < len(runs); i += 2 {
				if i+1 == len(runs) {
					copy(dst[runs[i][0]:runs[i][1]], src[runs[i][0]:runs[i][1]])
					next = append(next, runs[i])
					continue
				}
				a, b := runs[i], runs[i+1]
				wg.Add(1)
				go func() {
					defer wg.Done()
					mergeItems(dst[a[0]:b[1]], src[a[0]:a[1]], src[b[0]:b[1]], cmpItem)
				}()
				next = append(next, [2]int{a[0], b[1]})
			}
			wg.Wait()
			src, dst = dst, src
			runs = next
		}
		items = src
	}
	out := make([]entry, n)
	for j, it := range items {
		out[j] = es[it.i]
	}
	copy(es, out)
}

func mergeItems(dst, a, b []kitem, c func(x, y kitem) int) {
	i, j, k := 0, 0, 0
	for i < len(a) && j < len(b) {
		if c(b[j], a[i]) < 0 {
			dst[k] = b[j]
			j++
		} else {
			dst[k] = a[i]
			i++
		}
		k++
	}
	k += copy(dst[k:], a[i:])
	copy(dst[k:], b[j:])
}
