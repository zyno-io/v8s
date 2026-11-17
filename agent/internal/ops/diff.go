package ops

import (
	"encoding/json"
	"reflect"
	"sort"

	"github.com/zyno-io/v8s/agent/pkg/protocol"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
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

// diffPaths returns the changed paths between live and submitted.
func diffPaths(live, sub map[string]any) []string {
	var out []string
	diffMaps("", normalize(live).(map[string]any), normalize(sub).(map[string]any), &out)
	sort.Strings(out)
	return out
}

func join(path, k string) string {
	if path == "" {
		return k
	}
	return path + "." + k
}

func diffMaps(path string, a, b map[string]any, out *[]string) {
	keys := map[string]bool{}
	for k := range a {
		keys[k] = true
	}
	for k := range b {
		keys[k] = true
	}
	for k := range keys {
		if path == "" && k == "status" {
			continue
		}
		if path == "metadata" && ignoredMeta[k] {
			continue
		}
		cp := join(path, k)
		av, aok := a[k]
		bv, bok := b[k]
		if !aok || !bok {
			*out = append(*out, cp)
			continue
		}
		diffVal(cp, av, bv, out)
	}
}

func namedList(l []any) (map[string]any, bool) {
	if len(l) == 0 {
		return nil, false
	}
	m := map[string]any{}
	for _, e := range l {
		em, ok := e.(map[string]any)
		if !ok {
			return nil, false
		}
		n, ok := em["name"].(string)
		if !ok {
			return nil, false
		}
		if _, dup := m[n]; dup {
			return nil, false
		}
		m[n] = em
	}
	return m, true
}

func diffVal(path string, a, b any, out *[]string) {
	switch av := a.(type) {
	case map[string]any:
		if bv, ok := b.(map[string]any); ok {
			diffMaps(path, av, bv, out)
			return
		}
	case []any:
		if bv, ok := b.([]any); ok {
			am, aok := namedList(av)
			bm, bok := namedList(bv)
			if aok && bok {
				names := map[string]bool{}
				for n := range am {
					names[n] = true
				}
				for n := range bm {
					names[n] = true
				}
				for n := range names {
					ep := path + "[name=" + n + "]"
					ae, aok := am[n]
					be, bok := bm[n]
					if !aok || !bok {
						*out = append(*out, ep)
						continue
					}
					diffVal(ep, ae, be, out)
				}
				return
			}
		}
	}
	if !reflect.DeepEqual(a, b) {
		*out = append(*out, path)
	}
}

func related(a, b string) bool {
	if a == b {
		return true
	}
	return isPrefix(a, b) || isPrefix(b, a)
}

func isPrefix(p, full string) bool {
	if len(full) <= len(p) || full[:len(p)] != p {
		return false
	}
	c := full[len(p)]
	return c == '.' || c == '['
}

// conflictsFor reports, for each changed path, every other manager owning it or a parent/child.
func conflictsFor(live *unstructured.Unstructured, sub map[string]any) []protocol.FieldConflict {
	out := []protocol.FieldConflict{}
	changed := diffPaths(live.Object, sub)
	if len(changed) == 0 {
		return out
	}
	type own struct{ mgr, path string }
	var owned []own
	for _, mb := range managedByOf(live) {
		if mb.Manager == fieldManager {
			continue
		}
		for _, f := range mb.Fields {
			owned = append(owned, own{mb.Manager, f})
		}
	}
	seen := map[protocol.FieldConflict]bool{}
	for _, c := range changed {
		for _, o := range owned {
			if related(o.path, c) {
				fc := protocol.FieldConflict{Path: c, Manager: o.mgr}
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
