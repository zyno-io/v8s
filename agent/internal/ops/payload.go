package ops

import (
	"context"
	"sort"

	"github.com/zyno-io/v8s/agent/pkg/protocol"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/structured-merge-diff/v6/fieldpath"
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
	return buildPayload(obj, p.KeepManagedFields, p.Reveal)
}

// maskSecret replaces the values of a Secret's data (keys kept) and strips the last-applied annotation, which
// embeds them.
func maskSecret(o *unstructured.Unstructured) {
	if o.GetKind() != "Secret" || o.GetAPIVersion() != "v1" {
		return
	}
	for _, f := range []string{"data", "stringData"} {
		if m, ok, _ := unstructured.NestedMap(o.Object, f); ok {
			for k := range m {
				m[k] = protocol.MaskedValue
			}
			_ = unstructured.SetNestedMap(o.Object, m, f)
		}
	}
	if a := o.GetAnnotations(); a != nil {
		if _, ok := a[lastApplied]; ok {
			delete(a, lastApplied)
			o.SetAnnotations(a)
		}
	}
}

func buildPayload(obj *unstructured.Unstructured, keep, reveal bool) (*protocol.ObjectPayload, error) {
	managedBy := managedByOf(obj)
	cp := obj.DeepCopy()
	if !reveal {
		maskSecret(cp)
	}
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

// fieldPaths converts a fieldsV1 JSON document into sorted leaf paths (display form).
func fieldPaths(raw []byte) []string {
	set := parseSet(raw)
	out := []string{}
	set.Leaves().Iterate(func(p fieldpath.Path) {
		if s := pathString(p); s != "" {
			out = append(out, s)
		}
	})
	sort.Strings(out)
	return out
}
