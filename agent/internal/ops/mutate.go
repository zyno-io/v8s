package ops

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"

	"github.com/zyno-io/v8s/agent/internal/kube"
	"github.com/zyno-io/v8s/agent/pkg/protocol"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/yaml"
)

func ptr[T any](v T) *T { return &v }

var docSep = regexp.MustCompile(`(?m)^---[ \t]*(#.*)?\r?$`)

// parseDocs splits multi-document YAML into objects, skipping empty documents.
func parseDocs(src string) ([]*unstructured.Unstructured, error) {
	var out []*unstructured.Unstructured
	for i, chunk := range docSep.Split(src, -1) {
		if strings.TrimSpace(chunk) == "" {
			continue
		}
		j, err := yaml.YAMLToJSON([]byte(chunk))
		if err != nil {
			return nil, newErr("bad_request", "document %d: invalid YAML: %s", i+1, err.Error())
		}
		if t := strings.TrimSpace(string(j)); t == "null" || t == "{}" {
			continue
		}
		obj := &unstructured.Unstructured{}
		if err := obj.UnmarshalJSON(j); err != nil {
			return nil, newErr("bad_request", "document %d: %s", i+1, err.Error())
		}
		if obj.GetAPIVersion() == "" {
			return nil, newErr("bad_request", "document %d: apiVersion is required", i+1)
		}
		out = append(out, obj)
	}
	if len(out) == 0 {
		return nil, newErr("bad_request", "no YAML document provided")
	}
	return out, nil
}

type target struct {
	gvr    schema.GroupVersionResource
	ns     string
	name   string
	obj    *unstructured.Unstructured
	gvrStr string
}

func checkMode(mode string) error {
	if mode != "update" && mode != "create" {
		return newErr("bad_request", "mode must be \"update\" or \"create\"")
	}
	return nil
}

func (s *Service) prepUpdate(ref protocol.ObjectRef, src string) (*target, error) {
	gvr, err := parseGVR(ref.GVR)
	if err != nil {
		return nil, err
	}
	docs, err := parseDocs(src)
	if err != nil {
		return nil, err
	}
	if len(docs) != 1 {
		return nil, newErr("bad_request", "update mode requires exactly one document")
	}
	obj := docs[0]
	name := obj.GetName()
	if name == "" {
		name = ref.Name
	}
	if name == "" || (ref.Name != "" && obj.GetName() != "" && obj.GetName() != ref.Name) {
		return nil, newErr("bad_request", "metadata.name does not match the object being edited")
	}
	if ns := obj.GetNamespace(); ns != "" && ref.Namespace != "" && ns != ref.Namespace {
		return nil, newErr("bad_request", "metadata.namespace does not match the object being edited")
	}
	obj.SetName(name)
	ns := ref.Namespace
	if ns == "" {
		ns = obj.GetNamespace()
	}
	return &target{gvr: gvr, ns: ns, name: name, obj: obj, gvrStr: gvrString(gvr)}, nil
}

func (s *Service) prepCreate(cluster string, ref protocol.ObjectRef, src string) ([]*target, error) {
	docs, err := parseDocs(src)
	if err != nil {
		return nil, err
	}
	if s.cfg.Resolve == nil {
		return nil, newErr("internal", "no resource resolver")
	}
	var out []*target
	for i, d := range docs {
		g, namespaced, rerr := s.cfg.Resolve(cluster, d.GetAPIVersion(), d.GetKind())
		if rerr != nil {
			return nil, newErr("bad_request", "document %d: cannot resolve %s %s: %s", i+1, d.GetAPIVersion(), d.GetKind(), rerr.Error())
		}
		gvr, err := parseGVR(g)
		if err != nil {
			return nil, err
		}
		if d.GetName() == "" {
			return nil, newErr("bad_request", "document %d: metadata.name is required", i+1)
		}
		ns := ""
		if namespaced {
			ns = d.GetNamespace()
			if ns == "" {
				ns = ref.Namespace
			}
			if ns == "" {
				ns = "default"
			}
			d.SetNamespace(ns)
		}
		out = append(out, &target{gvr: gvr, ns: ns, name: d.GetName(), obj: d, gvrStr: gvrString(gvr)})
	}
	return out, nil
}

// routeCreate routes on the first document the read context is denied for.
func (s *Service) routeCreate(ctx context.Context, cluster string, items []*target) (string, kube.Conn, bool, error) {
	for _, it := range items {
		cid, c, viaWrite, err := s.route(ctx, cluster, "create", it.gvr, it.ns, "")
		if err != nil || viaWrite {
			return cid, c, viaWrite, err
		}
	}
	c, err := s.conn(cluster)
	return cluster, c, false, err
}

func (s *Service) ssa(ctx context.Context, c kube.Conn, it *target, dry bool) (*unstructured.Unstructured, error) {
	body := it.obj.DeepCopy()
	unstructured.RemoveNestedField(body.Object, "metadata", "managedFields")
	data, err := json.Marshal(body.Object)
	if err != nil {
		return nil, newErr("bad_request", "encode: %s", err.Error())
	}
	opts := metav1.PatchOptions{FieldManager: fieldManager, Force: ptr(false)}
	if dry {
		opts.DryRun = []string{metav1.DryRunAll}
	}
	return resource(c.Dynamic(), it.gvr, it.ns).Patch(ctx, it.name, types.ApplyPatchType, data, opts)
}

func (s *Service) livePayload(ctx context.Context, cluster string, t *target) *protocol.ObjectPayload {
	c, err := s.conn(cluster)
	if err != nil {
		return nil
	}
	live, err := resource(c.Dynamic(), t.gvr, t.ns).Get(ctx, t.name, metav1.GetOptions{})
	if err != nil {
		return nil
	}
	p, err := buildPayload(live, false, false)
	if err != nil {
		return nil
	}
	return p
}

// conflictErr maps a failed write; 409s carry the live object when available.
func (s *Service) conflictErr(ctx context.Context, cluster string, t *target, err error, viaWrite bool, w wsite) error {
	e := s.mapW(ctx, err, viaWrite, w)
	if e.Body.Code == "conflict" && t != nil {
		e.Body.Live = s.livePayload(ctx, cluster, t)
	}
	return e
}

// DryRun previews an update or create without persisting.
func (s *Service) DryRun(ctx context.Context, p protocol.ObjectDryRunParams) (protocol.ObjectDryRunResult, error) {
	res := protocol.ObjectDryRunResult{Warnings: []string{}, Conflicts: []protocol.FieldConflict{}}
	if err := checkMode(p.Mode); err != nil {
		return res, err
	}
	ctx = WithUseWrite(ctx, p.UseWriteContext)
	wctx, sink := kube.WithWarningSink(ctx)
	var site wsite
	finish := func(err error, viaWrite bool) (protocol.ObjectDryRunResult, error) {
		res.Warnings = sink.Warnings()
		if err == nil {
			res.OK = true
			return res, nil
		}
		e := s.mapW(ctx, err, viaWrite, site)
		if _, isStatus := err.(apierrors.APIStatus); isStatus && !apierrors.IsUnauthorized(err) {
			// a server rejection (validation, admission, quota, forbidden) is a result, not a failed request
			res.Error = &e.Body
			return res, nil
		}
		return res, e
	}

	if p.Mode == "update" {
		t, err := s.prepUpdate(p.Ref, p.YAML)
		if err != nil {
			return res, err
		}
		rc, err := s.conn(p.Ref.Cluster)
		if err != nil {
			return res, err
		}
		live, gerr := resource(rc.Dynamic(), t.gvr, t.ns).Get(wctx, t.name, metav1.GetOptions{})
		if gerr != nil {
			return res, mapErr(gerr, false)
		}
		if k := t.obj.GetKind(); k != "" && live.GetKind() != "" && k != live.GetKind() {
			return res, newErr("bad_request", "kind %s does not match the object being edited (%s)", k, live.GetKind())
		}
		if merr := s.unmask(wctx, p.Ref.Cluster, t, live); merr != nil {
			return res, merr
		}
		res.Conflicts = conflictsFor(live, foldStringData(t.obj.Object))
		t.obj.SetResourceVersion(live.GetResourceVersion())
		wcid, c, viaWrite, err := s.route(ctx, p.Ref.Cluster, "update", t.gvr, t.ns, t.name)
		if err != nil {
			return res, err
		}
		site = wsite{cid: wcid, verb: "update", gvr: t.gvr, ns: t.ns, name: t.name}
		_, uerr := resource(c.Dynamic(), t.gvr, t.ns).Update(wctx, t.obj, metav1.UpdateOptions{FieldManager: fieldManager, DryRun: []string{metav1.DryRunAll}})
		return finish(uerr, viaWrite)
	}

	items, err := s.prepCreate(p.Ref.Cluster, p.Ref, p.YAML)
	if err != nil {
		return res, err
	}
	wcid, c, viaWrite, err := s.routeCreate(ctx, p.Ref.Cluster, items)
	if err != nil {
		return res, err
	}
	for _, it := range items {
		site = wsite{cid: wcid, verb: "create", gvr: it.gvr, ns: it.ns}
		if _, aerr := s.ssa(wctx, c, it, true); aerr != nil {
			return finish(aerr, viaWrite)
		}
	}
	return finish(nil, viaWrite)
}

// Apply persists an update (PUT) or creates objects with server-side apply.
func (s *Service) Apply(ctx context.Context, p protocol.ObjectApplyParams) (protocol.ObjectApplyResult, error) {
	ctx = WithUseWrite(ctx, p.UseWriteContext)
	return dedupe(ctx, s, "apply", p.RequestID, func() (protocol.ObjectApplyResult, error) {
		dctx, cancel := detach(ctx)
		defer cancel()
		return s.apply(dctx, p)
	})
}

func (s *Service) apply(ctx context.Context, p protocol.ObjectApplyParams) (protocol.ObjectApplyResult, error) {
	var res protocol.ObjectApplyResult
	if err := checkMode(p.Mode); err != nil {
		return res, err
	}
	if p.Mode == "update" {
		if p.ResourceVersion == "" {
			return res, newErr("bad_request", "resourceVersion is required for update")
		}
		t, err := s.prepUpdate(p.Ref, p.YAML)
		if err != nil {
			return res, err
		}
		cid, c, viaWrite, err := s.route(ctx, p.Ref.Cluster, "update", t.gvr, t.ns, t.name)
		if err != nil {
			return res, err
		}
		t.obj.SetResourceVersion(p.ResourceVersion)
		if p.Ref.UID != "" {
			if verr := VerifyUID(ctx, c, t.gvr, t.ns, t.name, p.Ref.UID); verr != nil {
				return res, verr
			}
			t.obj.SetUID(types.UID(p.Ref.UID)) // the server rejects a PUT whose uid differs from the live object's
		}
		if merr := s.unmask(ctx, p.Ref.Cluster, t, nil); merr != nil {
			return res, merr
		}
		out, uerr := resource(c.Dynamic(), t.gvr, t.ns).Update(ctx, t.obj, metav1.UpdateOptions{FieldManager: fieldManager})
		if uerr != nil {
			return res, s.conflictErr(ctx, p.Ref.Cluster, t, uerr, viaWrite, wsite{cid: cid, verb: "update", gvr: t.gvr, ns: t.ns, name: t.name})
		}
		res.ResourceVersion = out.GetResourceVersion()
		res.UsedContext = cid
		return res, nil
	}

	items, err := s.prepCreate(p.Ref.Cluster, p.Ref, p.YAML)
	if err != nil {
		return res, err
	}
	cid, c, viaWrite, err := s.routeCreate(ctx, p.Ref.Cluster, items)
	if err != nil {
		return res, err
	}
	for _, it := range items {
		out, aerr := s.ssa(ctx, c, it, false)
		if aerr != nil {
			err := s.conflictErr(ctx, p.Ref.Cluster, nil, aerr, viaWrite, wsite{cid: cid, verb: "create", gvr: it.gvr, ns: it.ns})
			if e, ok := err.(*Error); ok {
				e.Body.Applied = append([]protocol.ObjectRef(nil), res.Applied...) // what succeeded before this document
			}
			return res, err
		}
		res.ResourceVersion = out.GetResourceVersion()
		res.Applied = append(res.Applied, protocol.ObjectRef{Cluster: p.Ref.Cluster, GVR: it.gvrStr, Namespace: it.ns, Name: it.name})
	}
	res.UsedContext = cid
	return res, nil
}

// Delete removes one object.
func (s *Service) Delete(ctx context.Context, p protocol.ObjectDeleteParams) (protocol.MutationResult, error) {
	ctx = WithUseWrite(ctx, p.UseWriteContext)
	return dedupe(ctx, s, "delete", p.RequestID, func() (protocol.MutationResult, error) {
		dctx, cancel := detach(ctx)
		defer cancel()
		return s.del(dctx, p)
	})
}

func (s *Service) del(ctx context.Context, p protocol.ObjectDeleteParams) (protocol.MutationResult, error) {
	var res protocol.MutationResult
	gvr, err := parseGVR(p.Ref.GVR)
	if err != nil {
		return res, err
	}
	if p.Ref.Name == "" {
		return res, newErr("bad_request", "name is required")
	}
	pol := metav1.DeletePropagationBackground
	switch p.Propagation {
	case "", "Background":
	case "Foreground":
		pol = metav1.DeletePropagationForeground
	case "Orphan":
		pol = metav1.DeletePropagationOrphan
	default:
		return res, newErr("bad_request", "unknown propagation %q", p.Propagation)
	}
	cid, c, viaWrite, err := s.route(ctx, p.Ref.Cluster, "delete", gvr, p.Ref.Namespace, p.Ref.Name)
	if err != nil {
		return res, err
	}
	if derr := resource(c.Dynamic(), gvr, p.Ref.Namespace).Delete(ctx, p.Ref.Name, metav1.DeleteOptions{PropagationPolicy: &pol, Preconditions: delPrecondition(p.Ref.UID)}); derr != nil {
		if re, ok := replacedErr(derr, p.Ref.UID); ok {
			return res, re
		}
		return res, s.mapW(ctx, derr, viaWrite, wsite{cid: cid, verb: "delete", gvr: gvr, ns: p.Ref.Namespace, name: p.Ref.Name})
	}
	res.UsedContext = cid
	return res, nil
}

// ---- actions ---------------------------------------------------------------------------

func toInt64(v any) (int64, bool) {
	switch n := v.(type) {
	case int:
		return int64(n), true
	case int8:
		return int64(n), true
	case int16:
		return int64(n), true
	case int32:
		return int64(n), true
	case int64:
		return n, true
	case uint:
		return int64(n), n <= math.MaxInt64
	case uint8:
		return int64(n), true
	case uint16:
		return int64(n), true
	case uint32:
		return int64(n), true
	case uint64:
		return int64(n), n <= math.MaxInt64
	case float32:
		return int64(n), float32(int64(n)) == n
	case float64:
		return int64(n), n == math.Trunc(n) && math.Abs(n) < 1<<53
	case json.Number:
		i, err := n.Int64()
		return i, err == nil
	}
	return 0, false
}

func resIn(r string, set ...string) bool {
	for _, s := range set {
		if r == s {
			return true
		}
	}
	return false
}

// Action runs a targeted one-click action.
func (s *Service) Action(ctx context.Context, p protocol.ObjectActionParams) (protocol.MutationResult, error) {
	ctx = WithUseWrite(ctx, p.UseWriteContext)
	return dedupe(ctx, s, "action", p.RequestID, func() (protocol.MutationResult, error) {
		dctx, cancel := detach(ctx)
		defer cancel()
		return s.action(dctx, p)
	})
}

func (s *Service) action(ctx context.Context, p protocol.ObjectActionParams) (protocol.MutationResult, error) {
	var res protocol.MutationResult
	gvr, err := parseGVR(p.Ref.GVR)
	if err != nil {
		return res, err
	}
	ref := p.Ref
	if ref.Name == "" {
		return res, newErr("bad_request", "name is required")
	}
	if ref.UID != "" { // pinned to one incarnation of the object: a recreated object must not be patched
		rc, cerr := s.conn(ref.Cluster)
		if cerr != nil {
			return res, cerr
		}
		if verr := VerifyUID(ctx, rc, gvr, ref.Namespace, ref.Name, ref.UID); verr != nil {
			return res, verr
		}
	}
	if strings.HasPrefix(p.Action, "flux.") {
		return s.fluxAction(ctx, p, gvr)
	}
	r := gvr.Resource
	wrong := func() error {
		return newErr("bad_request", "action %q is not supported for %s", p.Action, gvrString(gvr))
	}

	var patchType types.PatchType = types.MergePatchType
	var patch []byte
	var pre func() error // read-context lookups needed before routing

	switch p.Action {
	case "scale":
		if !resIn(r, "deployments", "statefulsets", "replicasets") {
			return res, wrong()
		}
		n, ok := toInt64(p.Params["replicas"])
		if !ok || n < 0 {
			return res, newErr("bad_request", "replicas must be a non-negative integer")
		}
		patch = []byte(fmt.Sprintf(`{"spec":{"replicas":%d}}`, n))
	case "restart":
		if !resIn(r, "deployments", "statefulsets", "daemonsets") {
			return res, wrong()
		}
		ts, _ := json.Marshal(s.cfg.Now().UTC().Format("2006-01-02T15:04:05Z07:00"))
		patch = []byte(`{"spec":{"template":{"metadata":{"annotations":{"kubectl.kubernetes.io/restartedAt":` + string(ts) + `}}}}}`)
	case "cordon", "uncordon":
		if r != "nodes" {
			return res, wrong()
		}
		patch = []byte(fmt.Sprintf(`{"spec":{"unschedulable":%t}}`, p.Action == "cordon"))
	case "suspend", "resume":
		if r != "cronjobs" {
			return res, wrong()
		}
		patch = []byte(fmt.Sprintf(`{"spec":{"suspend":%t}}`, p.Action == "suspend"))
	case "trigger":
		if r != "cronjobs" || gvr.Group != "batch" {
			return res, wrong()
		}
		return s.trigger(ctx, ref, gvr)
	case "rollback":
		if r != "deployments" || gvr.Group != "apps" {
			return res, wrong()
		}
		rev, ok := toInt64(p.Params["revision"])
		if !ok || rev < 0 {
			return res, newErr("bad_request", "revision must be a non-negative integer")
		}
		pre = func() error {
			tpl, err := s.rollbackTemplate(ctx, ref, gvr, rev)
			if err != nil {
				return err
			}
			patchType = types.JSONPatchType
			patch, _ = json.Marshal([]map[string]any{{"op": "replace", "path": "/spec/template", "value": tpl}})
			return nil
		}
	default:
		return res, newErr("bad_request", "unknown action %q", p.Action)
	}
	if pre != nil {
		if err := pre(); err != nil {
			return res, err
		}
	}
	cid, c, viaWrite, err := s.route(ctx, ref.Cluster, "patch", gvr, ref.Namespace, ref.Name)
	if err != nil {
		return res, err
	}
	out, perr := resource(c.Dynamic(), gvr, ref.Namespace).Patch(ctx, ref.Name, patchType, patch, metav1.PatchOptions{FieldManager: fieldManager})
	if perr != nil {
		return res, s.mapW(ctx, perr, viaWrite, wsite{cid: cid, verb: "patch", gvr: gvr, ns: ref.Namespace, name: ref.Name})
	}
	res.UsedContext = cid
	res.ResourceVersion = out.GetResourceVersion()
	return res, nil
}

const alnum = "abcdefghijklmnopqrstuvwxyz0123456789"

func randSuffix(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	for i := range b {
		b[i] = alnum[int(b[i])%len(alnum)]
	}
	return string(b)
}

func (s *Service) trigger(ctx context.Context, ref protocol.ObjectRef, gvr schema.GroupVersionResource) (protocol.MutationResult, error) {
	var res protocol.MutationResult
	rc, err := s.conn(ref.Cluster)
	if err != nil {
		return res, err
	}
	cj, gerr := resource(rc.Dynamic(), gvr, ref.Namespace).Get(ctx, ref.Name, metav1.GetOptions{})
	if gerr != nil {
		return res, mapErr(gerr, false)
	}
	spec, found, _ := unstructured.NestedMap(cj.Object, "spec", "jobTemplate", "spec")
	if !found {
		return res, newErr("bad_request", "cronjob has no spec.jobTemplate.spec")
	}
	suffix := "-manual-" + randSuffix(5)
	base := cj.GetName()
	if len(base) > 63-len(suffix) {
		base = base[:63-len(suffix)]
	}
	labels, _, _ := unstructured.NestedStringMap(cj.Object, "spec", "jobTemplate", "metadata", "labels")
	ann, _, _ := unstructured.NestedStringMap(cj.Object, "spec", "jobTemplate", "metadata", "annotations")
	if ann == nil {
		ann = map[string]string{}
	}
	ann["cronjob.kubernetes.io/instantiate"] = "manual"
	job := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "batch/v1",
		"kind":       "Job",
		"metadata": map[string]any{
			"name":      base + suffix,
			"namespace": ref.Namespace,
			"ownerReferences": []any{map[string]any{
				"apiVersion": "batch/v1", "kind": "CronJob", "name": cj.GetName(), "uid": string(cj.GetUID()),
				"controller": true, "blockOwnerDeletion": true,
			}},
		},
		"spec": spec,
	}}
	job.SetAnnotations(ann)
	if len(labels) > 0 {
		job.SetLabels(labels)
	}
	jobs := schema.GroupVersionResource{Group: "batch", Version: "v1", Resource: "jobs"}
	cid, c, viaWrite, err := s.route(ctx, ref.Cluster, "create", jobs, ref.Namespace, "")
	if err != nil {
		return res, err
	}
	out, cerr := resource(c.Dynamic(), jobs, ref.Namespace).Create(ctx, job, metav1.CreateOptions{FieldManager: fieldManager})
	if cerr != nil {
		return res, s.mapW(ctx, cerr, viaWrite, wsite{cid: cid, verb: "create", gvr: jobs, ns: ref.Namespace})
	}
	res.UsedContext = cid
	res.ResourceVersion = out.GetResourceVersion()
	res.Name = out.GetName()
	return res, nil
}

func (s *Service) rollbackTemplate(ctx context.Context, ref protocol.ObjectRef, gvr schema.GroupVersionResource, rev int64) (map[string]any, error) {
	rc, err := s.conn(ref.Cluster)
	if err != nil {
		return nil, err
	}
	dep, gerr := resource(rc.Dynamic(), gvr, ref.Namespace).Get(ctx, ref.Name, metav1.GetOptions{})
	if gerr != nil {
		return nil, mapErr(gerr, false)
	}
	rsGVR := schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "replicasets"}
	list, lerr := resource(rc.Dynamic(), rsGVR, ref.Namespace).List(ctx, metav1.ListOptions{})
	if lerr != nil {
		return nil, mapErr(lerr, false)
	}
	want := strconv.FormatInt(rev, 10)
	for i := range list.Items {
		rs := &list.Items[i]
		owned := false
		for _, o := range rs.GetOwnerReferences() {
			if o.UID == dep.GetUID() && dep.GetUID() != "" {
				owned = true
			}
		}
		if !owned || rs.GetAnnotations()["deployment.kubernetes.io/revision"] != want {
			continue
		}
		tpl, found, _ := unstructured.NestedMap(rs.Object, "spec", "template")
		if !found {
			continue
		}
		unstructured.RemoveNestedField(tpl, "metadata", "labels", "pod-template-hash")
		return tpl, nil
	}
	return nil, newErr("not_found", "revision %d of deployment %s not found", rev, ref.Name)
}

// unmask puts the live values back into an update of a Secret whose payload was fetched without reveal:
// keys still holding protocol.MaskedValue keep their live value, and a stripped last-applied annotation is kept.
// live may be passed when the caller already fetched it (read context).
func (s *Service) unmask(ctx context.Context, cluster string, t *target, live *unstructured.Unstructured) error {
	if t.obj.GetKind() != "Secret" || t.obj.GetAPIVersion() != "v1" {
		return nil
	}
	data, _, _ := unstructured.NestedMap(t.obj.Object, "data")
	sdata, _, _ := unstructured.NestedMap(t.obj.Object, "stringData")
	masked := false
	for _, m := range []map[string]any{data, sdata} {
		for _, v := range m {
			if v == protocol.MaskedValue {
				masked = true
			}
		}
	}
	ann := t.obj.GetAnnotations()
	_, hasLast := ann[lastApplied]
	if !masked && hasLast {
		return nil
	}
	if live == nil {
		rc, err := s.conn(cluster)
		if err != nil {
			return err
		}
		var gerr error
		if live, gerr = resource(rc.Dynamic(), t.gvr, t.ns).Get(ctx, t.name, metav1.GetOptions{}); gerr != nil {
			return mapErr(gerr, false)
		}
	}
	liveData, _, _ := unstructured.NestedMap(live.Object, "data")
	for k, v := range data {
		if v != protocol.MaskedValue {
			continue
		}
		lv, ok := liveData[k]
		if !ok {
			return newErr("bad_request", "data key %q is masked but does not exist on the live Secret", k)
		}
		data[k] = lv
	}
	for k, v := range sdata { // a masked stringData key means "unchanged": the live data entry stays
		if v == protocol.MaskedValue {
			delete(sdata, k)
		}
	}
	if len(data) > 0 {
		_ = unstructured.SetNestedMap(t.obj.Object, data, "data")
	}
	if len(sdata) > 0 {
		_ = unstructured.SetNestedMap(t.obj.Object, sdata, "stringData")
	} else {
		unstructured.RemoveNestedField(t.obj.Object, "stringData")
	}
	if !hasLast {
		if lv, ok := live.GetAnnotations()[lastApplied]; ok {
			if ann == nil {
				ann = map[string]string{}
			}
			ann[lastApplied] = lv
			t.obj.SetAnnotations(ann)
		}
	}
	return nil
}

const lastApplied = "kubectl.kubernetes.io/last-applied-configuration"

func delPrecondition(uid string) *metav1.Preconditions {
	if uid == "" {
		return nil
	}
	u := types.UID(uid)
	return &metav1.Preconditions{UID: &u}
}
