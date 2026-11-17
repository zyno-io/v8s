package flux

import (
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// SourceOf reads the source an object reconciles from: spec.sourceRef, else (HelmRelease)
// spec.chart.spec.sourceRef or spec.chartRef. apiVersion defaults to the Flux source group v1; namespace to
// the object's.
func SourceOf(o *unstructured.Unstructured) (apiVersion, kind, ns, name string, ok bool) {
	var m map[string]any
	for _, path := range [][]string{{"spec", "sourceRef"}, {"spec", "chart", "spec", "sourceRef"}, {"spec", "chartRef"}} {
		if v, found, _ := unstructured.NestedMap(o.Object, path...); found {
			m = v
			break
		}
	}
	if m == nil {
		return "", "", "", "", false
	}
	str := func(k string) string { s, _ := m[k].(string); return s }
	kind, name, ns, apiVersion = str("kind"), str("name"), str("namespace"), str("apiVersion")
	if kind == "" || name == "" {
		return "", "", "", "", false
	}
	if ns == "" {
		ns = o.GetNamespace()
	}
	if apiVersion == "" {
		apiVersion = GroupSource + "/v1"
	}
	return apiVersion, kind, ns, name, true
}
