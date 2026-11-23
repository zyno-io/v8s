package fake

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	jsonpatch "gopkg.in/evanphx/json-patch.v4"

	"github.com/zyno-io/v8s/agent/internal/disco"
	"github.com/zyno-io/v8s/agent/internal/helm"
	"github.com/zyno-io/v8s/agent/internal/ingest"
	"github.com/zyno-io/v8s/agent/internal/kube"
	"github.com/zyno-io/v8s/agent/pkg/protocol"
	authzv1 "k8s.io/api/authorization/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes/scheme"
	authv1 "k8s.io/client-go/kubernetes/typed/authorization/v1"
)

// ---- world operations ---------------------------------------------------------------------

type kwatch struct {
	gvr  string
	ns   string
	sel  fields.Selector // nil: everything
	conv func(typ watch.EventType, o *unstructured.Unstructured) []watch.Event
	ch   chan watch.Event
	done bool
}

func (k *kwatch) ResultChan() <-chan watch.Event { return k.ch }

type stopWatch struct {
	w *World
	k *kwatch
}

func (s *stopWatch) ResultChan() <-chan watch.Event { return s.k.ch }
func (s *stopWatch) Stop() {
	s.w.mu.Lock()
	delete(s.w.watchers, s.k)
	s.w.closeWatchLocked(s.k)
	s.w.mu.Unlock()
}

func (w *World) closeWatchLocked(k *kwatch) {
	if !k.done {
		k.done = true
		close(k.ch)
	}
}

func (w *World) emitLocked(typ watch.EventType, gvr string, o *unstructured.Unstructured) {
	rv, _ := strconv.ParseUint(o.GetResourceVersion(), 10, 64)
	if len(w.log) == worldLogSize {
		w.logStart = w.log[0].rv + 1
		copy(w.log, w.log[1:])
		w.log = w.log[:worldLogSize-1]
	}
	w.log = append(w.log, event{rv: rv, typ: typ, gvr: gvr, obj: o})
	for k := range w.watchers {
		if k.gvr == gvr && k.matches(o) {
			for _, ev := range k.conv(typ, o) {
				select {
				case k.ch <- ev:
				default:
					delete(w.watchers, k)
					w.closeWatchLocked(k)
				}
			}
		}
	}
}

func (k *kwatch) matches(o *unstructured.Unstructured) bool {
	return (k.ns == "" || o.GetNamespace() == k.ns) && (k.sel == nil || k.sel.Matches(fieldSet(k.gvr, o)))
}

// fieldSet is the field-selector view of an object: metadata.name and metadata.namespace for every kind, and
// for events the fields a real API server supports (type, reason, source, reportingComponent, involvedObject.*).
func fieldSet(gvr string, o *unstructured.Unstructured) fields.Set {
	s := fields.Set{"metadata.name": o.GetName(), "metadata.namespace": o.GetNamespace()}
	if gvr != "v1/events" {
		return s
	}
	str := func(path ...string) string { v, _, _ := unstructured.NestedString(o.Object, path...); return v }
	s["type"], s["reason"] = str("type"), str("reason")
	s["source"], s["reportingComponent"] = str("source", "component"), str("reportingComponent")
	for _, f := range []string{"kind", "name", "namespace", "uid", "apiVersion"} {
		s["involvedObject."+f] = str("involvedObject", f)
	}
	return s
}

// parseSelector parses a list/watch field selector (empty: no selector).
func parseSelector(sel string) (fields.Selector, error) {
	if sel == "" {
		return nil, nil
	}
	s, err := fields.ParseSelector(sel)
	if err != nil {
		return nil, apierrors.NewBadRequest("bad fieldSelector: " + err.Error())
	}
	return s, nil
}

func (w *World) getLocked(gvr, ns, name string) *unstructured.Unstructured {
	return w.objs[gvr][key(ns, name)]
}

// Get returns a deep copy of an object.
func (w *World) Get(gvr, ns, name string) (*unstructured.Unstructured, bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	o := w.getLocked(gvr, ns, name)
	if o == nil {
		return nil, false
	}
	return o.DeepCopy(), true
}

func (w *World) listLocked(gvr, ns string) []*unstructured.Unstructured {
	m := w.objs[gvr]
	out := make([]*unstructured.Unstructured, 0, len(m))
	for _, o := range m {
		if ns == "" || o.GetNamespace() == ns {
			out = append(out, o)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].GetNamespace() != out[j].GetNamespace() {
			return out[i].GetNamespace() < out[j].GetNamespace()
		}
		return out[i].GetName() < out[j].GetName()
	})
	return out
}

// store writes o (already validated) and emits the event. Caller holds w.mu.
func (w *World) storeLocked(gvr string, o *unstructured.Unstructured, create bool) *unstructured.Unstructured {
	d := kindByGVR[gvr]
	w.rv++
	o.SetAPIVersion(d.apiVersion)
	o.SetKind(d.kind)
	o.SetResourceVersion(strconv.FormatUint(w.rv, 10))
	if o.GetUID() == "" {
		o.SetUID(types.UID(w.uid(d.kind)))
	}
	if ct := o.GetCreationTimestamp(); ct.IsZero() {
		o.SetCreationTimestamp(metav1.Now())
	}
	m := w.objs[gvr]
	if m == nil {
		m = map[string]*unstructured.Unstructured{}
		w.objs[gvr] = m
	}
	m[key(o.GetNamespace(), o.GetName())] = o
	typ := watch.Modified
	if create {
		typ = watch.Added
	}
	w.emitLocked(typ, gvr, o.DeepCopy())
	w.scheduleReconcileLocked(gvr, o)
	return o.DeepCopy()
}

func (w *World) deleteLocked(gvr, ns, name string) error {
	o := w.getLocked(gvr, ns, name)
	if o == nil {
		return apierrors.NewNotFound(schemaGR(gvr), name)
	}
	delete(w.objs[gvr], key(ns, name))
	w.rv++
	dead := o.DeepCopy()
	dead.SetResourceVersion(strconv.FormatUint(w.rv, 10))
	w.emitLocked(watch.Deleted, gvr, dead)
	// cascade to owned objects
	uid := o.GetUID()
	for g, m := range w.objs {
		for k, c := range m {
			for _, or := range c.GetOwnerReferences() {
				if or.UID == uid {
					delete(m, k)
					w.rv++
					cd := c.DeepCopy()
					cd.SetResourceVersion(strconv.FormatUint(w.rv, 10))
					w.emitLocked(watch.Deleted, g, cd)
				}
			}
		}
	}
	return nil
}

func schemaGR(gvr string) schema.GroupResource {
	g, _, res, _ := ingest.ParseGVR(gvr)
	return schema.GroupResource{Group: g, Resource: res}
}

// ---- typed / table API for the ingest path -----------------------------------------------------

type kindAPI struct {
	w   *World
	d   *kindDef
	mod *ingest.Module
	ns  string // narrows list and watch to one namespace
	// delay holds each Table-mode LIST (Config.ListDelay).
	delay time.Duration
}

// InNamespace implements ingest.Namespacer.
func (a *kindAPI) InNamespace(ns string) ingest.API { c := *a; c.ns = ns; return &c }

func typedObject(o *unstructured.Unstructured) (runtime.Object, error) {
	obj, err := scheme.Scheme.New(o.GroupVersionKind())
	if err != nil {
		return nil, err
	}
	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(o.Object, obj); err != nil {
		return nil, err
	}
	return obj, nil
}

func (a *kindAPI) table() bool { return a.d.cols != nil }

// partial: a mode B kind (Flux) decoded by its module from the object's JSON.
func (a *kindAPI) partial() bool { return a.mod != nil && a.mod.DecodeJSON != nil }

func (a *kindAPI) decode(o *unstructured.Unstructured) (runtime.Object, error) {
	b, err := json.Marshal(o.Object)
	if err != nil {
		return nil, err
	}
	return a.mod.DecodeJSON(b)
}

func (a *kindAPI) row(o *unstructured.Unstructured) ingest.TableRow {
	r := ingest.TableRow{Cells: a.d.cells(o.Object), UID: string(o.GetUID()), Name: o.GetName(), Namespace: o.GetNamespace(), RV: o.GetResourceVersion(),
		Deleting: o.GetDeletionTimestamp() != nil}
	if t := o.GetCreationTimestamp(); !t.IsZero() {
		r.Created = t.UnixMilli()
	}
	if l := o.GetLabels(); len(l) > 0 { // curated Table modules read a few labels (Trivy: the workload)
		r.LabelsRaw, _ = json.Marshal(l)
	}
	if refs := o.GetOwnerReferences(); len(refs) > 0 {
		r.OwnerKind, r.OwnerName = refs[0].Kind, refs[0].Name
	}
	return r
}

func (a *kindAPI) List(ctx context.Context, opts metav1.ListOptions) (runtime.Object, error) {
	start, rv := 0, ""
	if opts.Continue != "" {
		s, r, ok := strings.Cut(opts.Continue, ":")
		n, err := strconv.Atoi(s)
		if !ok || err != nil {
			return nil, apierrors.NewBadRequest("bad continue token")
		}
		start, rv = n, r
	}
	sel, err := parseSelector(opts.FieldSelector)
	if err != nil {
		return nil, err
	}
	if a.table() && a.delay > 0 && opts.Limit != 1 { // the schema probe (Limit 1) is one row: it stays fast
		t := time.NewTimer(a.delay)
		select {
		case <-t.C:
		case <-ctx.Done():
			t.Stop()
			return nil, ctx.Err()
		}
	}
	a.w.mu.Lock()
	defer a.w.mu.Unlock()
	if opts.Continue == "" {
		rv = strconv.FormatUint(a.w.rv, 10)
	}
	all := a.w.listLocked(a.d.gvr, a.ns)
	if sel != nil {
		kept := all[:0:0]
		for _, o := range all {
			if sel.Matches(fieldSet(a.d.gvr, o)) {
				kept = append(kept, o)
			}
		}
		all = kept
	}
	limit := int(opts.Limit)
	if limit <= 0 {
		limit = len(all) + 1
	}
	end := min(len(all), start+limit)
	page := all[min(start, len(all)):end]
	cont := ""
	if end < len(all) {
		cont = fmt.Sprintf("%d:%s", end, rv)
	}
	if a.table() {
		t := &ingest.TableResult{Cols: a.d.cols, RV: rv, Continue: cont}
		for _, o := range page {
			t.Rows = append(t.Rows, a.row(o))
		}
		return t, nil
	}
	if a.partial() {
		pl := &ingest.PartialList{ListMeta: metav1.ListMeta{ResourceVersion: rv, Continue: cont}}
		for _, o := range page {
			d, err := a.decode(o)
			if err != nil {
				return nil, err
			}
			pl.Items = append(pl.Items, d)
		}
		return pl, nil
	}
	list := a.mod.NewList()
	objs := make([]runtime.Object, 0, len(page))
	for _, o := range page {
		to, err := typedObject(o)
		if err != nil {
			return nil, err
		}
		objs = append(objs, to)
	}
	if err := meta.SetList(list, objs); err != nil {
		return nil, err
	}
	la, _ := meta.ListAccessor(list)
	la.SetResourceVersion(rv)
	la.SetContinue(cont)
	return list, nil
}

func (a *kindAPI) conv() func(typ watch.EventType, o *unstructured.Unstructured) []watch.Event {
	first := true
	if a.table() {
		return func(typ watch.EventType, o *unstructured.Unstructured) []watch.Event {
			t := &ingest.TableResult{Rows: []ingest.TableRow{a.row(o)}}
			if first {
				t.Cols, first = a.d.cols, false
			}
			return []watch.Event{{Type: typ, Object: t}}
		}
	}
	if a.partial() {
		return func(typ watch.EventType, o *unstructured.Unstructured) []watch.Event {
			d, err := a.decode(o)
			if err != nil {
				return nil
			}
			return []watch.Event{{Type: typ, Object: d}}
		}
	}
	return func(typ watch.EventType, o *unstructured.Unstructured) []watch.Event {
		to, err := typedObject(o)
		if err != nil {
			return nil
		}
		return []watch.Event{{Type: typ, Object: to}}
	}
}

func (a *kindAPI) bookmark(rv uint64, initialEnd bool) watch.Event {
	if a.table() {
		t := &ingest.TableResult{Bookmark: true, RV: strconv.FormatUint(rv, 10)}
		if initialEnd {
			t.Annotations = map[string]string{metav1.InitialEventsAnnotationKey: "true"}
		}
		return watch.Event{Type: watch.Bookmark, Object: t}
	}
	if a.partial() {
		m := &metav1.PartialObjectMetadata{ObjectMeta: metav1.ObjectMeta{ResourceVersion: strconv.FormatUint(rv, 10)}}
		if initialEnd {
			m.Annotations = map[string]string{metav1.InitialEventsAnnotationKey: "true"}
		}
		return watch.Event{Type: watch.Bookmark, Object: m}
	}
	obj, _ := scheme.Scheme.New(schema.GroupVersionKind{Group: a.d.group(), Version: a.d.version(), Kind: a.d.kind})
	acc, _ := meta.Accessor(obj)
	acc.SetResourceVersion(strconv.FormatUint(rv, 10))
	if initialEnd {
		acc.SetAnnotations(map[string]string{metav1.InitialEventsAnnotationKey: "true"})
	}
	return watch.Event{Type: watch.Bookmark, Object: obj}
}

func (a *kindAPI) Watch(ctx context.Context, opts metav1.ListOptions) (watch.Interface, error) {
	sel, err := parseSelector(opts.FieldSelector)
	if err != nil {
		return nil, err
	}
	return a.w.watch(a.d.gvr, a.ns, sel, opts, a.conv(), a.bookmark)
}

// watch opens a watch with the semantics the reflector expects: a streaming list when
// SendInitialEvents is set, a replay from the log when a resourceVersion is given.
func (w *World) watch(gvr, ns string, sel fields.Selector, opts metav1.ListOptions, conv func(watch.EventType, *unstructured.Unstructured) []watch.Event,
	bookmark func(uint64, bool) watch.Event) (watch.Interface, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	k := &kwatch{gvr: gvr, ns: ns, sel: sel, conv: conv, ch: make(chan watch.Event, 8192)}
	initial := opts.SendInitialEvents != nil && *opts.SendInitialEvents
	if initial {
		for _, o := range w.listLocked(gvr, ns) {
			if sel != nil && !sel.Matches(fieldSet(gvr, o)) {
				continue
			}
			for _, ev := range conv(watch.Added, o) {
				select {
				case k.ch <- ev:
				default:
					return nil, apierrors.NewTooManyRequests("fake watch buffer", 1)
				}
			}
		}
		k.ch <- bookmark(w.rv, true)
	} else if rv, _ := strconv.ParseUint(opts.ResourceVersion, 10, 64); rv != 0 {
		if rv+1 < w.logStart {
			k.ch <- watch.Event{Type: watch.Error, Object: &apierrors.NewResourceExpired("too old resource version").ErrStatus}
			return &stopWatch{w, k}, nil
		}
		for _, e := range w.log {
			if e.rv > rv && e.gvr == gvr && k.matches(e.obj) {
				for _, ev := range conv(e.typ, e.obj) {
					select {
					case k.ch <- ev:
					default:
					}
				}
			}
		}
	}
	w.watchers[k] = struct{}{}
	return &stopWatch{w, k}, nil
}

// ---- dynamic client ---------------------------------------------------------------------------

type dynClient struct {
	w *World
	c *Cluster
}

func (d *dynClient) Resource(gvr schema.GroupVersionResource) dynamic.NamespaceableResourceInterface {
	g := gvr.Resource
	if gvr.Group != "" {
		g = gvr.Group + "/" + gvr.Version + "/" + gvr.Resource
	} else {
		g = gvr.Version + "/" + gvr.Resource
	}
	return &dynRes{d: d, gvr: g}
}

type dynRes struct {
	d   *dynClient
	gvr string
	ns  string
}

func (r *dynRes) Namespace(ns string) dynamic.ResourceInterface {
	c := *r
	c.ns = ns
	return &c
}

func (r *dynRes) def() (*kindDef, error) {
	d := kindByGVR[r.gvr]
	if d == nil {
		return nil, apierrors.NewNotFound(schemaGR(r.gvr), r.gvr)
	}
	return d, nil
}

func (r *dynRes) Get(ctx context.Context, name string, _ metav1.GetOptions, sub ...string) (*unstructured.Unstructured, error) {
	if _, err := r.def(); err != nil {
		return nil, err
	}
	if r.gvr == "v1/pods" {
		return r.d.c.podObject(r.ns, name)
	}
	o, ok := r.d.w.Get(r.gvr, r.ns, name)
	if !ok {
		return nil, apierrors.NewNotFound(schemaGR(r.gvr), name)
	}
	return o, nil
}

func (r *dynRes) List(ctx context.Context, opts metav1.ListOptions) (*unstructured.UnstructuredList, error) {
	if _, err := r.def(); err != nil {
		return nil, err
	}
	r.d.w.mu.Lock()
	defer r.d.w.mu.Unlock()
	out := &unstructured.UnstructuredList{Object: map[string]any{}}
	out.SetResourceVersion(strconv.FormatUint(r.d.w.rv, 10))
	for _, o := range r.d.w.listLocked(r.gvr, r.ns) {
		out.Items = append(out.Items, *o.DeepCopy())
	}
	return out, nil
}

func (r *dynRes) Watch(ctx context.Context, opts metav1.ListOptions) (watch.Interface, error) {
	if _, err := r.def(); err != nil {
		return nil, err
	}
	conv := func(typ watch.EventType, o *unstructured.Unstructured) []watch.Event {
		return []watch.Event{{Type: typ, Object: o.DeepCopy()}}
	}
	bm := func(rv uint64, _ bool) watch.Event {
		o := &unstructured.Unstructured{Object: map[string]any{}}
		o.SetResourceVersion(strconv.FormatUint(rv, 10))
		return watch.Event{Type: watch.Bookmark, Object: o}
	}
	sel, err := parseSelector(opts.FieldSelector)
	if err != nil {
		return nil, err
	}
	return r.d.w.watch(r.gvr, r.ns, sel, opts, conv, bm)
}

func dry(o []string) bool { return len(o) > 0 }

func (r *dynRes) Create(ctx context.Context, obj *unstructured.Unstructured, opts metav1.CreateOptions, sub ...string) (*unstructured.Unstructured, error) {
	if _, err := r.def(); err != nil {
		return nil, err
	}
	o := obj.DeepCopy()
	if r.ns != "" {
		o.SetNamespace(r.ns)
	}
	if o.GetName() == "" {
		return nil, apierrors.NewBadRequest("name is required")
	}
	w := r.d.w
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.getLocked(r.gvr, o.GetNamespace(), o.GetName()) != nil {
		return nil, apierrors.NewAlreadyExists(schemaGR(r.gvr), o.GetName())
	}
	if dry(opts.DryRun) {
		o.SetResourceVersion(strconv.FormatUint(w.rv+1, 10))
		return o, nil
	}
	return w.storeLocked(r.gvr, o, true), nil
}

func (r *dynRes) Update(ctx context.Context, obj *unstructured.Unstructured, opts metav1.UpdateOptions, sub ...string) (*unstructured.Unstructured, error) {
	if _, err := r.def(); err != nil {
		return nil, err
	}
	o := obj.DeepCopy()
	if r.ns != "" {
		o.SetNamespace(r.ns)
	}
	if r.gvr == "v1/pods" {
		return r.d.c.touchPod(o.GetNamespace(), o.GetName(), dry(opts.DryRun))
	}
	w := r.d.w
	w.mu.Lock()
	defer w.mu.Unlock()
	cur := w.getLocked(r.gvr, o.GetNamespace(), o.GetName())
	if cur == nil {
		return nil, apierrors.NewNotFound(schemaGR(r.gvr), o.GetName())
	}
	if rv := o.GetResourceVersion(); rv != "" && rv != cur.GetResourceVersion() {
		return nil, apierrors.NewConflict(schemaGR(r.gvr), o.GetName(), fmt.Errorf("the object has been modified; please apply your changes to the latest version and try again"))
	}
	o.SetUID(cur.GetUID())
	o.SetCreationTimestamp(cur.GetCreationTimestamp())
	recordManager(o, opts.FieldManager, "Update")
	if dry(opts.DryRun) {
		o.SetResourceVersion(strconv.FormatUint(w.rv+1, 10))
		return o, nil
	}
	return w.storeLocked(r.gvr, o, false), nil
}

func recordManager(o *unstructured.Unstructured, manager, op string) {
	if manager == "" {
		return
	}
	mf := o.GetManagedFields()
	for _, e := range mf {
		if e.Manager == manager && string(e.Operation) == op {
			return
		}
	}
	mf = append(mf, metav1.ManagedFieldsEntry{Manager: manager, Operation: metav1.ManagedFieldsOperationType(op), APIVersion: o.GetAPIVersion(),
		FieldsType: "FieldsV1", FieldsV1: &metav1.FieldsV1{Raw: []byte(`{"f:metadata":{"f:labels":{}}}`)}})
	o.SetManagedFields(mf)
}

func (r *dynRes) UpdateStatus(ctx context.Context, obj *unstructured.Unstructured, opts metav1.UpdateOptions) (*unstructured.Unstructured, error) {
	return r.Update(ctx, obj, opts)
}

func (r *dynRes) Delete(ctx context.Context, name string, opts metav1.DeleteOptions, sub ...string) error {
	if _, err := r.def(); err != nil {
		return err
	}
	if r.gvr == "v1/pods" {
		if opts.Preconditions != nil && opts.Preconditions.UID != nil {
			if o, err := r.d.c.podObject(r.ns, name); err != nil {
				return err
			} else if o.GetUID() != *opts.Preconditions.UID {
				return apierrors.NewConflict(schemaGR(r.gvr), name, fmt.Errorf("Precondition failed: UID in precondition: %s, UID in object meta: %s", *opts.Preconditions.UID, o.GetUID()))
			}
		}
		return r.d.c.killPod(r.ns, name, dry(opts.DryRun))
	}
	w := r.d.w
	w.mu.Lock()
	defer w.mu.Unlock()
	if opts.Preconditions != nil && opts.Preconditions.UID != nil {
		if cur := w.getLocked(r.gvr, r.ns, name); cur != nil && cur.GetUID() != *opts.Preconditions.UID {
			return apierrors.NewConflict(schemaGR(r.gvr), name, fmt.Errorf("Precondition failed: UID in precondition: %s, UID in object meta: %s", *opts.Preconditions.UID, cur.GetUID()))
		}
	}
	if dry(opts.DryRun) {
		if w.getLocked(r.gvr, r.ns, name) == nil {
			return apierrors.NewNotFound(schemaGR(r.gvr), name)
		}
		return nil
	}
	return w.deleteLocked(r.gvr, r.ns, name)
}

func (r *dynRes) DeleteCollection(context.Context, metav1.DeleteOptions, metav1.ListOptions) error {
	return apierrors.NewMethodNotSupported(schemaGR(r.gvr), "deletecollection")
}

func (r *dynRes) Patch(ctx context.Context, name string, pt types.PatchType, data []byte, opts metav1.PatchOptions, sub ...string) (*unstructured.Unstructured, error) {
	if _, err := r.def(); err != nil {
		return nil, err
	}
	if r.gvr == "v1/pods" {
		return r.d.c.touchPod(r.ns, name, dry(opts.DryRun))
	}
	w := r.d.w
	w.mu.Lock()
	defer w.mu.Unlock()
	cur := w.getLocked(r.gvr, r.ns, name)
	if pt == types.ApplyPatchType {
		var patch map[string]any
		if err := json.Unmarshal(data, &patch); err != nil {
			return nil, apierrors.NewBadRequest(err.Error())
		}
		if cur == nil {
			o := &unstructured.Unstructured{Object: patch}
			o.SetName(name)
			o.SetNamespace(r.ns)
			recordManager(o, opts.FieldManager, "Apply")
			if dry(opts.DryRun) {
				return o, nil
			}
			return w.storeLocked(r.gvr, o, true), nil
		}
		merged := cur.DeepCopy()
		mergeInto(merged.Object, patch)
		recordManager(merged, opts.FieldManager, "Apply")
		if dry(opts.DryRun) {
			return merged, nil
		}
		return w.storeLocked(r.gvr, merged, false), nil
	}
	if cur == nil {
		return nil, apierrors.NewNotFound(schemaGR(r.gvr), name)
	}
	doc, _ := json.Marshal(cur.Object)
	var out []byte
	var err error
	switch pt {
	case types.MergePatchType, types.StrategicMergePatchType:
		out, err = jsonpatch.MergePatch(doc, data)
	case types.JSONPatchType:
		var p jsonpatch.Patch
		if p, err = jsonpatch.DecodePatch(data); err == nil {
			out, err = p.Apply(doc)
		}
	default:
		err = fmt.Errorf("unsupported patch type %q", pt)
	}
	if err != nil {
		return nil, apierrors.NewBadRequest(err.Error())
	}
	var m map[string]any
	if err := json.Unmarshal(out, &m); err != nil {
		return nil, apierrors.NewBadRequest(err.Error())
	}
	o := &unstructured.Unstructured{Object: m}
	o.SetManagedFields(cur.GetManagedFields())
	recordManager(o, opts.FieldManager, "Update")
	if dry(opts.DryRun) {
		return o, nil
	}
	r.d.w.applyEffects(r.gvr, cur, o)
	return w.storeLocked(r.gvr, o, false), nil
}

// applyEffects mimics controllers for the actions the UI offers: a scaled Deployment/ReplicaSet/StatefulSet
// reports its new replica counts, a rolled-back Deployment gets the template's image, a cordoned node
// shows it. Caller holds w.mu.
func (w *World) applyEffects(gvr string, old, o *unstructured.Unstructured) {
	switch gvr {
	case "apps/v1/deployments", "apps/v1/statefulsets", "apps/v1/replicasets":
		n, ok, _ := unstructured.NestedInt64(o.Object, "spec", "replicas")
		if gvr == "apps/v1/deployments" { // patched objects come from JSON, so replicas may be a float64 here
			prev, pok := specReplicas(old)
			if cur, cok := specReplicas(o); pok && cok && prev != cur {
				w.scalingEvent(o, prev, cur)
			}
		}
		if ok {
			_ = unstructured.SetNestedField(o.Object, n, "status", "replicas")
			_ = unstructured.SetNestedField(o.Object, n, "status", "readyReplicas")
			_ = unstructured.SetNestedField(o.Object, n, "status", "availableReplicas")
			_ = unstructured.SetNestedField(o.Object, n, "status", "updatedReplicas")
		}
	}
}

func specReplicas(o *unstructured.Unstructured) (int64, bool) {
	v, found, _ := unstructured.NestedFieldNoCopy(o.Object, "spec", "replicas")
	switch n := v.(type) {
	case int64:
		return n, found
	case float64:
		return int64(n), found
	case int:
		return int64(n), found
	}
	return 0, false
}

// scalingEvent records the deployment controller's ScalingReplicaSet event for a replica change.
func (w *World) scalingEvent(o *unstructured.Unstructured, prev, n int64) {
	dir := "up"
	if n < prev {
		dir = "down"
	}
	w.eventLocked(o.GetNamespace(), map[string]any{"kind": "Deployment", "name": o.GetName(), "namespace": o.GetNamespace(), "uid": string(o.GetUID())},
		"Normal", "ScalingReplicaSet", "Scaled "+dir+" replica set "+w.ownedRS(o)+" to "+strconv.FormatInt(n, 10), "deployment-controller", time.Now(), true)
}

// ownedRS names the ReplicaSet a Deployment owns (the newest with replicas, else the first), or the Deployment's name.
func (w *World) ownedRS(d *unstructured.Unstructured) string {
	best := ""
	for _, rs := range w.objs["apps/v1/replicasets"] {
		if rs.GetNamespace() != d.GetNamespace() {
			continue
		}
		for _, or := range rs.GetOwnerReferences() {
			if or.UID != d.GetUID() {
				continue
			}
			n, _, _ := unstructured.NestedInt64(rs.Object, "spec", "replicas")
			if best == "" || n > 0 {
				best = rs.GetName()
			}
		}
	}
	if best == "" {
		return d.GetName()
	}
	return best
}

func (r *dynRes) Apply(ctx context.Context, name string, obj *unstructured.Unstructured, opts metav1.ApplyOptions, sub ...string) (*unstructured.Unstructured, error) {
	data, _ := json.Marshal(obj.Object)
	f := opts.Force
	return r.Patch(ctx, name, types.ApplyPatchType, data, metav1.PatchOptions{FieldManager: opts.FieldManager, Force: &f})
}

func (r *dynRes) ApplyStatus(ctx context.Context, name string, obj *unstructured.Unstructured, opts metav1.ApplyOptions) (*unstructured.Unstructured, error) {
	return r.Apply(ctx, name, obj, opts)
}

func mergeInto(dst, src map[string]any) {
	for k, v := range src {
		if sm, ok := v.(map[string]any); ok {
			if dm, ok := dst[k].(map[string]any); ok {
				mergeInto(dm, sm)
				continue
			}
		}
		dst[k] = v
	}
}

// ---- backend ---------------------------------------------------------------------------------

// Backend is the fake cluster's kube.Backend.
type Backend struct {
	c     *Cluster
	w     *World
	admin bool // the linked write context ("<cluster>-admin")
	deny  bool // read context of a read-only cluster (rbac=1): mutations are denied
	dyn   *dynClient
	rate  int // synthetic log lines per second
	// noMetrics: metrics.k8s.io is not installed (not in discovery; the lists answer NotFound).
	noMetrics bool
	// listDelay and discDelay slow Table-mode LISTs and discovery (fake options listdelay, discdelay).
	listDelay, discDelay time.Duration
}

var _ kube.Backend = (*Backend)(nil)

func (b *Backend) Dynamic() dynamic.Interface { return b.dyn }

func (b *Backend) Authz() authv1.SelfSubjectAccessReviewInterface { return kubeAuthz{b} }

func (b *Backend) API(gvr string, m *ingest.Module) (ingest.API, error) {
	if gvr == ingest.PodsGVR {
		return ingest.PodAdapter{PodAPI: b.c}, nil
	}
	if gvr == protocol.HelmGVR {
		return helm.NewReleaseAPI(helm.NewTypedMetaSource(b.Clientset())), nil
	}
	d := kindByGVR[gvr]
	if d == nil {
		return nil, apierrors.NewNotFound(schemaGR(gvr), gvr)
	}
	if b.deny && gvr == "v1/secrets" { // the rbac=1 read context cannot read Secrets; its admin context can
		return deniedAPI{err: apierrors.NewForbidden(schemaGR(gvr), "", errors.New("RBAC: this identity cannot list secrets on "+b.c.name))}, nil
	}
	return &kindAPI{w: b.w, d: d, mod: m, delay: b.listDelay}, nil
}

func (b *Backend) Discover(ctx context.Context) ([]disco.Resource, error) {
	if b.discDelay > 0 {
		t := time.NewTimer(b.discDelay)
		select {
		case <-t.C:
		case <-ctx.Done():
			t.Stop()
			return nil, ctx.Err()
		}
	}
	d := Discovery()
	if b.noMetrics {
		d = slices.DeleteFunc(d, func(r disco.Resource) bool { return r.Group == "metrics.k8s.io" })
	}
	return d, nil
}

// Admin reports whether this is the linked write context ("<cluster>-admin").
func (b *Backend) Admin() bool { return b.admin }

// deniedAPI answers every list and watch with err.
type deniedAPI struct{ err error }

func (d deniedAPI) List(context.Context, metav1.ListOptions) (runtime.Object, error) {
	return nil, d.err
}
func (d deniedAPI) Watch(context.Context, metav1.ListOptions) (watch.Interface, error) {
	return nil, d.err
}

// World returns the cluster's object store (tests).
func (b *Backend) World() *World { return b.w }

type kubeAuthz struct{ b *Backend }

// Create answers SelfSubjectAccessReviews: everything is allowed, except mutating verbs on the read
// context of a read-only fake cluster.
func (a kubeAuthz) Create(ctx context.Context, r *authzv1.SelfSubjectAccessReview, _ metav1.CreateOptions) (*authzv1.SelfSubjectAccessReview, error) {
	out := r.DeepCopy()
	out.Status.Allowed = true
	if a.b.deny {
		switch r.Spec.ResourceAttributes.Verb {
		case "create", "update", "patch", "delete", "deletecollection":
			out.Status.Allowed = false
			out.Status.Reason = "RBAC: this identity is read-only on " + a.b.c.name
		}
	}
	return out, nil
}
