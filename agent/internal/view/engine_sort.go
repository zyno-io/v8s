package view

import (
	"cmp"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"

	"github.com/zyno-io/v8s/agent/internal/ingest"
	"github.com/zyno-io/v8s/agent/internal/usage"
	"github.com/zyno-io/v8s/agent/pkg/protocol"
)

// entry is one row of a materialized view. row is immutable, so an entry is a consistent snapshot.
type entry struct {
	row  *ingest.Row
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

func mkEntry(row *ingest.Row, cl uint16, h, gen uint32) entry {
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

// colString returns an accessor for the string form of a column (used by filters).
func (e *Engine) colString(c *ingest.Col) func(r *ingest.Row, cl uint16) string {
	switch c.Src {
	case ingest.SrcCluster:
		return func(_ *ingest.Row, cl uint16) string { return e.names[cl] }
	case ingest.SrcName:
		return func(r *ingest.Row, _ uint16) string { return r.Name() }
	case ingest.SrcNS:
		return func(r *ingest.Row, _ uint16) string { return r.NS().Value() }
	case ingest.SrcOwner:
		return func(r *ingest.Row, _ uint16) string { return r.OwnerName().Value() }
	case ingest.SrcIP:
		f := c.IPField
		return func(r *ingest.Row, _ uint16) string { return r.IPText(f) }
	case ingest.SrcCell:
		idx := c.Idx
		if c.Type == protocol.ColInt || c.Type == protocol.ColTime {
			return func(r *ingest.Row, _ uint16) string { return strconv.FormatInt(r.Cell(idx).N, 10) }
		}
		return func(r *ingest.Row, _ uint16) string { return r.StrCell(idx) }
	}
	return func(*ingest.Row, uint16) string { return "" }
}

// buildMatch compiles the query's membership predicate.
func (e *Engine) buildMatch(n *Norm) func(r *ingest.Row, cl uint16) bool {
	var preds []func(r *ingest.Row, cl uint16) bool
	if len(n.Clusters) > 0 {
		set := n.Clusters
		preds = append(preds, func(_ *ingest.Row, cl uint16) bool {
			_, ok := slices.BinarySearch(set, e.names[cl])
			return ok
		})
	}
	if n.nsSet != nil {
		set := n.nsSet
		preds = append(preds, func(r *ingest.Row, _ uint16) bool {
			_, ok := set[r.NS().Value()]
			return ok
		})
	}
	if n.Text != "" {
		t := n.Text
		if q := n.ip; q != nil { // IP-like text on a kind with IPs: name contains it, or an address matches
			preds = append(preds, func(r *ingest.Row, _ uint16) bool { return containsFold(r.Name(), t) || q.Any(r.IPs()) })
		} else {
			preds = append(preds, func(r *ingest.Row, _ uint16) bool { return containsFold(r.Name(), t) })
		}
	}
	for _, f := range n.filters {
		f := f
		if f.c.Src == ingest.SrcTone {
			preds = append(preds, func(r *ingest.Row, _ uint16) bool { return f.toneMask&(1<<r.Tone()) != 0 })
			continue
		}
		get := e.colString(f.c)
		if f.op == "contains" {
			preds = append(preds, func(r *ingest.Row, cl uint16) bool {
				s := get(r, cl)
				for _, v := range f.vals {
					if containsFold(s, v) {
						return true
					}
				}
				return false
			})
			continue
		}
		if f.c.Src == ingest.SrcName { // hot path: no closure indirection
			preds = append(preds, func(r *ingest.Row, _ uint16) bool { return slices.Contains(f.vals, r.Name()) })
			continue
		}
		preds = append(preds, func(r *ingest.Row, cl uint16) bool { return slices.Contains(f.vals, get(r, cl)) })
	}
	switch len(preds) {
	case 0:
		return func(*ingest.Row, uint16) bool { return true }
	case 1:
		return preds[0]
	}
	return func(r *ingest.Row, cl uint16) bool {
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
func (e *Engine) buildCmp(n *Norm, v *View) cmpFn {
	var keys []cmpFn
	for _, k := range n.sort {
		desc := k.desc
		if k.col == "statusTone" {
			keys = append(keys, func(a, b *entry) int { return dir(cmp.Compare(a.rank, b.rank), desc) })
			continue
		}
		c := n.Mod.Col(k.col)
		switch c.Src {
		case ingest.SrcCluster:
			keys = append(keys, func(a, b *entry) int { return dir(cmpStr(e.names[a.cl], e.names[b.cl]), desc) })
		case ingest.SrcName:
			keys = append(keys, func(a, b *entry) int { return dir(cmpName(a, b), desc) })
		case ingest.SrcNS:
			keys = append(keys, func(a, b *entry) int { return dir(cmpHandle(a.row.NS(), b.row.NS()), desc) })
		case ingest.SrcOwner:
			keys = append(keys, func(a, b *entry) int { return dir(cmpHandle(a.row.OwnerName(), b.row.OwnerName()), desc) })
		case ingest.SrcCreated:
			keys = append(keys, func(a, b *entry) int { return dir(cmp.Compare(a.row.CreatedMs(), b.row.CreatedMs()), desc) })
		case ingest.SrcTone:
			keys = append(keys, func(a, b *entry) int { return dir(cmp.Compare(a.rank, b.rank), desc) })
		case ingest.SrcIP:
			f := c.IPField
			keys = append(keys, func(a, b *entry) int {
				x, okx := a.row.FirstIP(f)
				y, oky := b.row.FirstIP(f)
				switch {
				case okx && oky:
					return dir(x.Cmp(y), desc)
				case okx:
					return -1 // rows without an address sort last either way
				case oky:
					return 1
				}
				return 0
			})
		case ingest.SrcUsage, ingest.SrcRes:
			// Missing values sort last in both directions: rank (value, n/a, none) ascending, then the value by direction.
			uk, res := c.Use, c.Src == ingest.SrcRes
			keys = append(keys, func(a, b *entry) int {
				var sa, sb *usage.Slot
				if !res {
					sa, sb = v.slot(a), v.slot(b)
				}
				va, ra := usageOrd(uk, a.row, sa)
				vb, rb := usageOrd(uk, b.row, sb)
				if ra != rb {
					return cmp.Compare(ra, rb)
				}
				return dir(cmp.Compare(va, vb), desc)
			})
		case ingest.SrcCell:
			idx := c.Idx
			if c.Unit != "" { // a unit cell below zero is missing (encoded nil): last in both directions, as usage cells
				keys = append(keys, func(a, b *entry) int {
					na, nb := a.row.Cell(idx).N, b.row.Cell(idx).N
					if ma, mb := na < 0, nb < 0; ma != mb {
						if ma {
							return 1
						}
						return -1
					}
					return dir(cmp.Compare(na, nb), desc)
				})
			} else if c.Type == protocol.ColInt || c.Type == protocol.ColTime {
				keys = append(keys, func(a, b *entry) int { return dir(cmp.Compare(a.row.Cell(idx).N, b.row.Cell(idx).N), desc) })
			} else {
				keys = append(keys, func(a, b *entry) int { return dir(cmpHandle(a.row.Cell(idx).S, b.row.Cell(idx).S), desc) })
			}
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

func (e *Engine) buildKey(n *Norm, v *View) keyFn {
	def := len(n.sort) == 2 && n.sort[0] == sortKey{col: "statusTone"} && n.sort[1] == sortKey{col: "name"}
	if def {
		return func(e *entry) uint64 { return uint64(e.rank)<<56 | e.pfx>>8 }
	}
	if len(n.sort) == 1 && n.sort[0] == (sortKey{col: "name"}) {
		return func(e *entry) uint64 { return e.pfx }
	}
	if len(n.sort) >= 1 {
		if c := n.Mod.Col(n.sort[0].col); c != nil && (c.Src == ingest.SrcUsage || c.Src == ingest.SrcRes) {
			// key = rank<<62 | value (inverted when descending): ranks ascend either way, so missing values stay last.
			uk, res, desc := c.Use, c.Src == ingest.SrcRes, n.sort[0].desc
			const lim = 1<<62 - 1
			return func(en *entry) uint64 {
				var sl *usage.Slot
				if !res {
					sl = v.slot(en)
				}
				val, rank := usageOrd(uk, en.row, sl)
				val = min(val, lim)
				if desc {
					val = lim - val
				}
				return uint64(rank)<<62 | val
			}
		}
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
