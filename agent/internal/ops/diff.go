package ops

import (
	"encoding/json"
	"fmt"
	"math"
	"reflect"
	"sort"
	"strings"

	"github.com/zyno-io/v8s/agent/pkg/protocol"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/structured-merge-diff/v6/fieldpath"
	"sigs.k8s.io/structured-merge-diff/v6/value"
)

var ignoredMeta = map[string]bool{
	"managedFields": true, "resourceVersion": true, "generation": true,
	"creationTimestamp": true, "uid": true, "selfLink": true,
}

func normalize(v any) any {
	b, _ := json.Marshal(v)
	var out any
	_ = json.Unmarshal(b, &out)
	return out
}

// listShape says how the managedFields of the live object address the items of a list, learned from the
// owned sets themselves (their real list keys), keyed by the list's path.
type listShape struct {
	keys   map[string][]string // list path -> key field names (associative list of maps)
	values map[string]bool     // list path -> items are addressed by value (set of scalars)
}

func newShape() *listShape { return &listShape{keys: map[string][]string{}, values: map[string]bool{}} }

func (ls *listShape) learn(s *fieldpath.Set) {
	s.Iterate(func(p fieldpath.Path) {
		for i, pe := range p {
			switch {
			case pe.Key != nil:
				names := make([]string, 0, len(*pe.Key))
				for _, f := range *pe.Key {
					names = append(names, f.Name)
				}
				ls.keys[pathKey(p[:i])] = names
			case pe.Value != nil:
				ls.values[pathKey(p[:i])] = true
			}
		}
	})
}

func pathKey(p fieldpath.Path) string { return pathString(p) }

// diffPaths returns the changed paths between live and submitted, with list items addressed as the
// managedFields do (shape), falling back to "name" for lists of named maps.
func diffPaths(live, sub map[string]any, shape *listShape) []fieldpath.Path {
	if shape == nil {
		shape = newShape()
	}
	var out []fieldpath.Path
	diffMaps(nil, normalize(live).(map[string]any), normalize(sub).(map[string]any), shape, &out)
	sort.Slice(out, func(i, j int) bool { return pathString(out[i]) < pathString(out[j]) })
	return out
}

func extend(p fieldpath.Path, pe fieldpath.PathElement) fieldpath.Path {
	return append(append(fieldpath.Path(nil), p...), pe)
}

func diffMaps(path fieldpath.Path, a, b map[string]any, sh *listShape, out *[]fieldpath.Path) {
	keys := map[string]bool{}
	for k := range a {
		keys[k] = true
	}
	for k := range b {
		keys[k] = true
	}
	top := len(path) == 0
	inMeta := len(path) == 1 && path[0].FieldName != nil && *path[0].FieldName == "metadata"
	for k := range keys {
		if top && k == "status" {
			continue
		}
		if inMeta && ignoredMeta[k] {
			continue
		}
		cp := extend(path, fieldpath.PathElement{FieldName: strptr(k)})
		av, aok := a[k]
		bv, bok := b[k]
		if !aok || !bok {
			*out = append(*out, cp)
			continue
		}
		diffVal(cp, av, bv, sh, out)
	}
}

func strptr(s string) *string { return &s }

func scalarVal(v any) (value.Value, bool) {
	switch x := v.(type) {
	case string, bool, nil:
		return value.NewValueInterface(x), true
	case float64:
		if x == math.Trunc(x) && math.Abs(x) < 1<<53 {
			return value.NewValueInterface(int64(x)), true
		}
		return value.NewValueInterface(x), true
	}
	return nil, false
}

// itemElem builds the PathElement addressing item e of a list under the given shape.
func itemElem(path fieldpath.Path, e any, sh *listShape) (fieldpath.PathElement, bool) {
	pk := pathKey(path)
	if names, ok := sh.keys[pk]; ok {
		m, ok := e.(map[string]any)
		if !ok {
			return fieldpath.PathElement{}, false
		}
		var fl value.FieldList
		for _, n := range names {
			fv, ok := m[n]
			if !ok {
				return fieldpath.PathElement{}, false
			}
			sv, ok := scalarVal(fv)
			if !ok {
				return fieldpath.PathElement{}, false
			}
			fl = append(fl, value.Field{Name: n, Value: sv})
		}
		fl.Sort()
		return fieldpath.PathElement{Key: &fl}, true
	}
	if m, ok := e.(map[string]any); ok && !sh.values[pk] {
		if n, ok := m["name"].(string); ok {
			fl := value.FieldList{{Name: "name", Value: value.NewValueInterface(n)}}
			return fieldpath.PathElement{Key: &fl}, true
		}
		return fieldpath.PathElement{}, false
	}
	if sv, ok := scalarVal(e); ok && sh.values[pk] {
		return fieldpath.PathElement{Value: &sv}, true
	}
	return fieldpath.PathElement{}, false
}

func keyedItems(path fieldpath.Path, l []any, sh *listShape) (map[string]any, map[string]fieldpath.PathElement, bool) {
	if len(l) == 0 {
		return nil, nil, false
	}
	items := map[string]any{}
	elems := map[string]fieldpath.PathElement{}
	for _, e := range l {
		pe, ok := itemElem(path, e, sh)
		if !ok {
			return nil, nil, false
		}
		k := pe.String()
		if _, dup := items[k]; dup {
			return nil, nil, false
		}
		items[k] = e
		elems[k] = pe
	}
	return items, elems, true
}

func diffVal(path fieldpath.Path, a, b any, sh *listShape, out *[]fieldpath.Path) {
	switch av := a.(type) {
	case map[string]any:
		if bv, ok := b.(map[string]any); ok {
			diffMaps(path, av, bv, sh, out)
			return
		}
	case []any:
		if bv, ok := b.([]any); ok {
			am, ae, aok := keyedItems(path, av, sh)
			bm, be, bok := keyedItems(path, bv, sh)
			if aok && bok {
				ids := map[string]fieldpath.PathElement{}
				for k, e := range ae {
					ids[k] = e
				}
				for k, e := range be {
					ids[k] = e
				}
				for k, pe := range ids {
					ep := extend(path, pe)
					x, xok := am[k]
					y, yok := bm[k]
					if !xok || !yok {
						*out = append(*out, ep)
						continue
					}
					diffVal(ep, x, y, sh, out)
				}
				return
			}
		}
	}
	if !reflect.DeepEqual(a, b) {
		*out = append(*out, path)
	}
}

// pathString renders a path for display: fields joined by dots, list keys as [k=v,...], set values as [=v],
// indexes as [i] (the same form as ManagedBy.Fields).
func pathString(p fieldpath.Path) string {
	var b strings.Builder
	for _, pe := range p {
		switch {
		case pe.FieldName != nil:
			if b.Len() > 0 {
				b.WriteByte('.')
			}
			b.WriteString(*pe.FieldName)
		case pe.Key != nil:
			parts := make([]string, 0, len(*pe.Key))
			for _, f := range *pe.Key {
				parts = append(parts, f.Name+"="+valueString(f.Value))
			}
			b.WriteString("[" + strings.Join(parts, ",") + "]")
		case pe.Value != nil:
			jb, _ := json.Marshal((*pe.Value).Unstructured())
			b.WriteString("[=" + string(jb) + "]")
		case pe.Index != nil:
			fmt.Fprintf(&b, "[%d]", *pe.Index)
		}
	}
	return b.String()
}

func valueString(v value.Value) string {
	if v == nil {
		return "null"
	}
	if v.IsString() {
		return v.AsString()
	}
	return fmt.Sprint(v.Unstructured())
}

// owns reports whether the owned set contains path, a prefix of it, or a child of it.
func owns(s *fieldpath.Set, p fieldpath.Path) bool {
	node := s
	for _, pe := range p {
		if node.Members.Has(pe) {
			return true
		}
		c, ok := node.Children.Get(pe)
		if !ok {
			return false
		}
		node = c
	}
	return !node.Empty()
}

func parseSet(raw []byte) *fieldpath.Set {
	s := fieldpath.NewSet()
	if len(raw) == 0 {
		return s
	}
	if err := s.FromJSON(strings.NewReader(string(raw))); err != nil {
		return fieldpath.NewSet()
	}
	return s
}

// conflictsFor reports, for each changed path, every other manager owning it, a prefix of it or a child of it.
func conflictsFor(live *unstructured.Unstructured, sub map[string]any) []protocol.FieldConflict {
	out := []protocol.FieldConflict{}
	type own struct {
		mgr string
		set *fieldpath.Set
	}
	var owned []own
	shape := newShape()
	for _, mf := range live.GetManagedFields() {
		if mf.Manager == fieldManager || mf.FieldsV1 == nil {
			continue
		}
		s := parseSet(mf.FieldsV1.Raw)
		shape.learn(s)
		owned = append(owned, own{mf.Manager, s})
	}
	if len(owned) == 0 {
		return out
	}
	changed := diffPaths(live.Object, sub, shape)
	seen := map[protocol.FieldConflict]bool{}
	for _, c := range changed {
		for _, o := range owned {
			if owns(o.set, c) {
				fc := protocol.FieldConflict{Path: pathString(c), Manager: o.mgr}
				if !seen[fc] {
					seen[fc] = true
					out = append(out, fc)
				}
			}
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Path != out[j].Path {
			return out[i].Path < out[j].Path
		}
		return out[i].Manager < out[j].Manager
	})
	return out
}
