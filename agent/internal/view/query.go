// Package view is the view engine: materialized, sorted, filtered handle lists per normalized
// query, maintained incrementally from the stores' change feeds, plus per-subscription windowing,
// wire ids and patch diffing.
package view

import (
	"cmp"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/zyno-io/v8s/agent/internal/ingest"
	"github.com/zyno-io/v8s/agent/pkg/protocol"
)

const PodsGVR = ingest.PodsGVR

// BadQueryError is returned for queries the engine cannot serve (maps to bad_request).
type BadQueryError struct{ Msg string }

func (e *BadQueryError) Error() string { return e.Msg }

func bad(format string, a ...any) error { return &BadQueryError{fmt.Sprintf(format, a...)} }

type sortKey struct {
	col  string
	desc bool
}

type filterSpec struct {
	col      string
	c        *ingest.Col
	op       string
	vals     []string // sorted
	toneMask uint8    // for statusTone
}

// Norm is a normalized, canonical form of a ViewQuery.
type Norm struct {
	Key        string
	GVR        string
	Mod        *ingest.Module
	Clusters   []string
	Namespaces []string
	Text       string // lower-cased
	filters    []filterSpec
	sort       []sortKey
	nsSet      map[string]struct{}
}

func filterValues(v any) ([]string, error) {
	switch v := v.(type) {
	case string:
		return []string{v}, nil
	case []string:
		return slices.Clone(v), nil
	case []any:
		var vals []string
		for _, x := range v {
			s, err := filterValues(x)
			if err != nil || len(s) != 1 {
				return nil, bad("filter value must be strings")
			}
			vals = append(vals, s[0])
		}
		return vals, nil
	case int8, int16, int32, int64, uint8, uint16, uint32, uint64, int:
		return []string{fmt.Sprint(v)}, nil
	}
	return nil, bad("filter value must be a string or string[]")
}

// Normalize validates q and produces its canonical form; equal queries yield equal Keys.
func Normalize(q protocol.ViewQuery) (*Norm, error) {
	gvr := q.GVR
	if gvr == "" {
		gvr = PodsGVR
	}
	mod := ingest.Lookup(gvr)
	if mod == nil {
		return nil, bad("unsupported gvr %q", q.GVR)
	}
	n := &Norm{GVR: gvr, Mod: mod, Text: strings.ToLower(strings.TrimSpace(q.Text))}
	n.Clusters = sortedUnique(q.Clusters)
	n.Namespaces = sortedUnique(q.Namespaces)
	if len(n.Namespaces) > 0 {
		n.nsSet = make(map[string]struct{}, len(n.Namespaces))
		for _, ns := range n.Namespaces {
			n.nsSet[ns] = struct{}{}
		}
	}
	for _, f := range q.Filters {
		col := mod.Col(f.Col)
		if col == nil || !col.Filterable {
			return nil, bad("cannot filter on %q", f.Col)
		}
		vals, err := filterValues(f.Value)
		if err != nil {
			return nil, err
		}
		switch f.Op {
		case "eq", "in", "contains":
		default:
			return nil, bad("unknown filter op %q", f.Op)
		}
		if f.Op == "contains" && col.Src == ingest.SrcTone {
			return nil, bad("contains not supported on %q", f.Col)
		}
		fs := filterSpec{col: f.Col, c: col, op: f.Op, vals: sortedUnique(vals)}
		if col.Src == ingest.SrcTone {
			for _, v := range fs.vals {
				t, err := strconv.Atoi(v)
				if err != nil || t < 0 || t > 7 {
					return nil, bad("invalid tone %q", v)
				}
				fs.toneMask |= 1 << uint(t)
			}
		}
		if f.Op == "contains" {
			for i := range fs.vals {
				fs.vals[i] = strings.ToLower(fs.vals[i])
			}
		}
		n.filters = append(n.filters, fs)
	}
	slices.SortFunc(n.filters, func(a, b filterSpec) int {
		return cmp.Or(cmp.Compare(a.col, b.col), cmp.Compare(a.op, b.op), slices.Compare(a.vals, b.vals))
	})
	for _, s := range q.Sort {
		if c := mod.Col(s.Col); (c == nil || !c.Sortable) && s.Col != "statusTone" {
			return nil, bad("cannot sort on %q", s.Col)
		}
		n.sort = append(n.sort, sortKey{s.Col, s.Desc})
	}
	if len(n.sort) == 0 {
		for _, s := range mod.DefaultSort {
			n.sort = append(n.sort, sortKey{s.Col, s.Desc})
		}
	}
	if len(n.sort) == 0 {
		n.sort = []sortKey{{col: "statusTone"}, {col: "name"}}
	}
	var b strings.Builder
	fmt.Fprintf(&b, "g=%s|c=%q|n=%q|t=%q|f=", n.GVR, n.Clusters, n.Namespaces, n.Text)
	for _, f := range n.filters {
		fmt.Fprintf(&b, "%s:%s:%q;", f.col, f.op, f.vals)
	}
	b.WriteString("|s=")
	for _, s := range n.sort {
		fmt.Fprintf(&b, "%s:%t;", s.col, s.desc)
	}
	n.Key = b.String()
	return n, nil
}

func sortedUnique(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	out := slices.Clone(in)
	slices.Sort(out)
	return slices.Compact(out)
}

// containsFold reports whether s contains lowerSub (already lower-case), ASCII case-insensitively
// without allocating.
func containsFold(s, lowerSub string) bool {
	n := len(lowerSub)
	if n == 0 {
		return true
	}
	for i := 0; i+n <= len(s); i++ {
		j := 0
		for ; j < n; j++ {
			c := s[i+j]
			if 'A' <= c && c <= 'Z' {
				c += 'a' - 'A'
			}
			if c != lowerSub[j] {
				break
			}
		}
		if j == n {
			return true
		}
	}
	return false
}
