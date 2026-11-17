package ops

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/zyno-io/v8s/agent/pkg/protocol"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/yaml"
)

// Get fetches one object on the read context.
func (s *Service) Get(ctx context.Context, p protocol.ObjectGetParams) (*protocol.ObjectPayload, error) {
	gvr, err := parseGVR(p.GVR)
	if err != nil {
		return nil, err
	}
	if p.Name == "" {
		return nil, newErr("bad_request", "name is required")
	}
	c, err := s.conn(p.Cluster)
	if err != nil {
		return nil, err
	}
	obj, gerr := resource(c.Dynamic(), gvr, p.Namespace).Get(ctx, p.Name, metav1.GetOptions{})
	if gerr != nil {
		return nil, mapErr(gerr, false)
	}
	return buildPayload(obj, p.KeepManagedFields)
}

func buildPayload(obj *unstructured.Unstructured, keep bool) (*protocol.ObjectPayload, error) {
	managedBy := managedByOf(obj)
	cp := obj.DeepCopy()
	if !keep {
		unstructured.RemoveNestedField(cp.Object, "metadata", "managedFields")
	}
	y, err := yaml.Marshal(cp.Object)
	if err != nil {
		return nil, newErr("internal", "encode yaml: %s", err.Error())
	}
	owners := []protocol.ObjectOwner{}
	for _, o := range obj.GetOwnerReferences() {
		owners = append(owners, protocol.ObjectOwner{Kind: o.Kind, Name: o.Name})
	}
	return &protocol.ObjectPayload{
		YAML:            string(y),
		UID:             string(obj.GetUID()),
		ResourceVersion: obj.GetResourceVersion(),
		ManagedBy:       managedBy,
		Owners:          owners,
		GitOps:          gitopsOf(obj),
	}, nil
}

func managedByOf(obj *unstructured.Unstructured) []protocol.ManagedBy {
	out := []protocol.ManagedBy{}
	for _, mf := range obj.GetManagedFields() {
		fields := []string{}
		if mf.FieldsV1 != nil && len(mf.FieldsV1.Raw) > 0 {
			fields = fieldPaths(mf.FieldsV1.Raw)
		}
		out = append(out, protocol.ManagedBy{Manager: mf.Manager, Operation: string(mf.Operation), Fields: fields})
	}
	return out
}

func gitopsOf(obj *unstructured.Unstructured) *protocol.GitOps {
	l := obj.GetLabels()
	a := obj.GetAnnotations()
	if n, ns := l["kustomize.toolkit.fluxcd.io/name"], l["kustomize.toolkit.fluxcd.io/namespace"]; n != "" && ns != "" {
		return &protocol.GitOps{Kind: "Kustomization", Name: n, Namespace: ns}
	}
	if n, ns := l["helm.toolkit.fluxcd.io/name"], l["helm.toolkit.fluxcd.io/namespace"]; n != "" && ns != "" {
		return &protocol.GitOps{Kind: "HelmRelease", Name: n, Namespace: ns}
	}
	if l["app.kubernetes.io/managed-by"] == "Helm" {
		return &protocol.GitOps{Kind: "Helm", Name: a["meta.helm.sh/release-name"], Namespace: a["meta.helm.sh/release-namespace"]}
	}
	return nil
}

// fieldPaths converts a fieldsV1 JSON document into sorted dotted leaf paths.
func fieldPaths(raw []byte) []string {
	var root map[string]any
	if err := json.Unmarshal(raw, &root); err != nil {
		return []string{}
	}
	out := []string{}
	walkFields(root, "", &out)
	sort.Strings(out)
	return out
}

func walkFields(node map[string]any, prefix string, out *[]string) {
	n := 0
	for k := range node {
		if k != "." {
			n++
		}
	}
	if n == 0 {
		if prefix != "" {
			*out = append(*out, prefix)
		}
		return
	}
	for k, v := range node {
		if k == "." {
			continue
		}
		child, _ := v.(map[string]any)
		walkFields(child, joinSeg(prefix, k), out)
	}
}

func joinSeg(prefix, k string) string {
	switch {
	case strings.HasPrefix(k, "f:"):
		if prefix == "" {
			return k[2:]
		}
		return prefix + "." + k[2:]
	case strings.HasPrefix(k, "k:"):
		var m map[string]any
		if err := json.Unmarshal([]byte(k[2:]), &m); err != nil {
			return prefix + "[" + k[2:] + "]"
		}
		keys := make([]string, 0, len(m))
		for kk := range m {
			keys = append(keys, kk)
		}
		sort.Strings(keys)
		parts := make([]string, 0, len(keys))
		for _, kk := range keys {
			parts = append(parts, kk+"="+scalarString(m[kk]))
		}
		return prefix + "[" + strings.Join(parts, ",") + "]"
	case strings.HasPrefix(k, "v:"):
		return prefix + "[=" + k[2:] + "]"
	case strings.HasPrefix(k, "i:"):
		return prefix + "[" + k[2:] + "]"
	}
	if prefix == "" {
		return k
	}
	return prefix + "." + k
}

func scalarString(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	if f, ok := v.(float64); ok && f == float64(int64(f)) {
		return fmt.Sprintf("%d", int64(f))
	}
	b, _ := json.Marshal(v)
	return string(b)
}
