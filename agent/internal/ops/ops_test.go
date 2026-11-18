package ops

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/zyno-io/v8s/agent/internal/kube"
	"github.com/zyno-io/v8s/agent/pkg/protocol"
	authorizationv1 "k8s.io/api/authorization/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/dynamic"
	dynfake "k8s.io/client-go/dynamic/fake"
	authv1 "k8s.io/client-go/kubernetes/typed/authorization/v1"
	ktesting "k8s.io/client-go/testing"
)

// ---- fixtures ---------------------------------------------------------------------------

type fakeAuthz struct {
	authv1.SelfSubjectAccessReviewInterface
	fn    func(ra *authorizationv1.ResourceAttributes) (bool, string, error)
	calls atomic.Int32
}

func (f *fakeAuthz) Create(_ context.Context, r *authorizationv1.SelfSubjectAccessReview, _ metav1.CreateOptions) (*authorizationv1.SelfSubjectAccessReview, error) {
	f.calls.Add(1)
	ok, reason, err := f.fn(r.Spec.ResourceAttributes)
	if err != nil {
		return nil, err
	}
	return &authorizationv1.SelfSubjectAccessReview{Status: authorizationv1.SubjectAccessReviewStatus{Allowed: ok, Reason: reason}}, nil
}

type fakeConn struct {
	dyn *dynfake.FakeDynamicClient
	az  *fakeAuthz
}

func (c *fakeConn) Dynamic() dynamic.Interface                     { return c.dyn }
func (c *fakeConn) Authz() authv1.SelfSubjectAccessReviewInterface { return c.az }

var listKinds = map[schema.GroupVersionResource]string{
	{Group: "apps", Version: "v1", Resource: "deployments"}:  "DeploymentList",
	{Group: "apps", Version: "v1", Resource: "statefulsets"}: "StatefulSetList",
	{Group: "apps", Version: "v1", Resource: "replicasets"}:  "ReplicaSetList",
	{Group: "batch", Version: "v1", Resource: "cronjobs"}:    "CronJobList",
	{Group: "batch", Version: "v1", Resource: "jobs"}:        "JobList",
	{Version: "v1", Resource: "nodes"}:                       "NodeList",
	{Version: "v1", Resource: "configmaps"}:                  "ConfigMapList",
}

func newConn(allow bool, objs ...runtime.Object) *fakeConn {
	return &fakeConn{
		dyn: dynfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), listKinds, objs...),
		az:  &fakeAuthz{fn: func(*authorizationv1.ResourceAttributes) (bool, string, error) { return allow, "no rbac", nil }},
	}
}

type env struct {
	s      *Service
	read   *fakeConn
	write  *fakeConn
	auto   bool
	linked bool
	now    time.Time
	mu     sync.Mutex
}

func (e *env) clock() time.Time { e.mu.Lock(); defer e.mu.Unlock(); return e.now }
func (e *env) advance(d time.Duration) {
	e.mu.Lock()
	e.now = e.now.Add(d)
	e.mu.Unlock()
}

func newEnv(read, write *fakeConn) *env {
	e := &env{read: read, write: write, now: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC), linked: write != nil, auto: true}
	e.s = New(Config{
		Conn: func(id string) (kube.Conn, error) {
			switch id {
			case "c1":
				return e.read, nil
			case "c1-write":
				if e.write != nil {
					return e.write, nil
				}
			}
			return nil, errors.New("unknown context")
		},
		Links: func(string) (string, bool) {
			if e.linked {
				return "c1-write", e.auto
			}
			return "", false
		},
		Resolve: func(_, apiVersion, kind string) (string, bool, error) {
			switch kind {
			case "ConfigMap":
				return "v1/configmaps", true, nil
			case "Namespace":
				return "v1/namespaces", false, nil
			}
			return "", false, errors.New("unknown kind")
		},
		Now: e.clock,
	})
	return e
}

func mk(apiVersion, kind, ns, name string, extra map[string]any) *unstructured.Unstructured {
	o := map[string]any{"apiVersion": apiVersion, "kind": kind, "metadata": map[string]any{"name": name}}
	if ns != "" {
		o["metadata"].(map[string]any)["namespace"] = ns
	}
	for k, v := range extra {
		o[k] = v
	}
	return &unstructured.Unstructured{Object: o}
}

func setMeta(o *unstructured.Unstructured, k string, v any) *unstructured.Unstructured {
	_ = unstructured.SetNestedField(o.Object, v, "metadata", k)
	return o
}

func deployment(ns, name string) *unstructured.Unstructured {
	o := mk("apps/v1", "Deployment", ns, name, map[string]any{"spec": map[string]any{
		"replicas": int64(2),
		"template": map[string]any{"spec": map[string]any{"containers": []any{
			map[string]any{"name": "app", "image": "app:1"},
		}}},
	}})
	setMeta(o, "uid", "dep-uid")
	return setMeta(o, "resourceVersion", "10")
}

func code(t *testing.T, err error) string {
	t.Helper()
	var e *Error
	if !errors.As(err, &e) {
		t.Fatalf("want *Error, got %v", err)
	}
	return e.Body.Code
}

func depRef() protocol.ObjectRef {
	return protocol.ObjectRef{Cluster: "c1", GVR: "apps/v1/deployments", Namespace: "ns", Name: "web"}
}

func lastAction(c *fakeConn, verb string) ktesting.Action {
	var out ktesting.Action
	for _, a := range c.dyn.Actions() {
		if a.GetVerb() == verb {
			out = a
		}
	}
	return out
}

// ---- Get --------------------------------------------------------------------------------

func fieldsV1(m map[string]any) map[string]any { return m }

func TestGetPayload(t *testing.T) {
	d := deployment("ns", "web")
	setMeta(d, "labels", map[string]any{"helm.toolkit.fluxcd.io/name": "h", "helm.toolkit.fluxcd.io/namespace": "flux", "app.kubernetes.io/managed-by": "Helm"})
	setMeta(d, "annotations", map[string]any{"kubectl.kubernetes.io/last-applied-configuration": "{}"})
	setMeta(d, "ownerReferences", []any{map[string]any{"apiVersion": "v1", "kind": "Foo", "name": "foo", "uid": "u"}})
	setMeta(d, "managedFields", []any{
		map[string]any{"manager": "helm", "operation": "Update", "apiVersion": "apps/v1", "fieldsV1": fieldsV1(map[string]any{
			"f:metadata": map[string]any{"f:labels": map[string]any{".": map[string]any{}, "f:app": map[string]any{}}},
			"f:spec": map[string]any{
				"f:replicas": map[string]any{},
				"f:template": map[string]any{"f:spec": map[string]any{"f:containers": map[string]any{
					`k:{"name":"app"}`: map[string]any{".": map[string]any{}, "f:image": map[string]any{}, "f:name": map[string]any{}},
				}}},
				"f:finalizers": map[string]any{`v:"x"`: map[string]any{}},
				"f:args":       map[string]any{"i:0": map[string]any{}},
			},
		})},
		map[string]any{"manager": "empty", "operation": "Apply", "apiVersion": "apps/v1"},
	})
	e := newEnv(newConn(true, d), nil)

	p, err := e.s.Get(context.Background(), protocol.ObjectGetParams{Cluster: "c1", GVR: "apps/v1/deployments", Namespace: "ns", Name: "web"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(p.YAML, "managedFields") {
		t.Fatal("managedFields not stripped")
	}
	if !strings.Contains(p.YAML, "last-applied-configuration") {
		t.Fatal("annotations must be kept")
	}
	if p.UID != "dep-uid" || p.ResourceVersion != "10" {
		t.Fatalf("uid/rv %q %q", p.UID, p.ResourceVersion)
	}
	if len(p.Owners) != 1 || p.Owners[0] != (protocol.ObjectOwner{Kind: "Foo", Name: "foo"}) {
		t.Fatalf("owners %+v", p.Owners)
	}
	if p.GitOps == nil || p.GitOps.Kind != "HelmRelease" || p.GitOps.Name != "h" || p.GitOps.Namespace != "flux" {
		t.Fatalf("gitops %+v", p.GitOps)
	}
	if len(p.ManagedBy) != 2 {
		t.Fatalf("managedBy %+v", p.ManagedBy)
	}
	want := []string{
		`metadata.labels.app`,
		`spec.args[0]`,
		`spec.finalizers[="x"]`,
		`spec.replicas`,
		`spec.template.spec.containers[name=app].image`,
		`spec.template.spec.containers[name=app].name`,
	}
	if got := p.ManagedBy[0].Fields; strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("fields\n got %q\nwant %q", got, want)
	}
	if p.ManagedBy[1].Manager != "empty" || p.ManagedBy[1].Fields == nil || len(p.ManagedBy[1].Fields) != 0 {
		t.Fatalf("empty manager %+v", p.ManagedBy[1])
	}

	p, err = e.s.Get(context.Background(), protocol.ObjectGetParams{Cluster: "c1", GVR: "apps/v1/deployments", Namespace: "ns", Name: "web", KeepManagedFields: true})
	if err != nil || !strings.Contains(p.YAML, "managedFields") {
		t.Fatalf("managedFields should be kept: %v", err)
	}

	_, err = e.s.Get(context.Background(), protocol.ObjectGetParams{Cluster: "c1", GVR: "apps/v1/deployments", Namespace: "ns", Name: "nope"})
	if code(t, err) != "not_found" {
		t.Fatal(err)
	}
	_, err = e.s.Get(context.Background(), protocol.ObjectGetParams{Cluster: "c1", GVR: "bad", Name: "x"})
	if code(t, err) != "bad_request" {
		t.Fatal(err)
	}
}

func TestGitOpsKinds(t *testing.T) {
	o := mk("v1", "ConfigMap", "ns", "a", nil)
	o.SetLabels(map[string]string{"kustomize.toolkit.fluxcd.io/name": "k", "kustomize.toolkit.fluxcd.io/namespace": "flux", "app.kubernetes.io/managed-by": "Helm"})
	if g := gitopsOf(o); g == nil || g.Kind != "Kustomization" || g.Name != "k" {
		t.Fatalf("%+v", g)
	}
	o.SetLabels(map[string]string{"app.kubernetes.io/managed-by": "Helm"})
	o.SetAnnotations(map[string]string{"meta.helm.sh/release-name": "rel", "meta.helm.sh/release-namespace": "rns"})
	if g := gitopsOf(o); g == nil || *g != (protocol.GitOps{Kind: "Helm", Name: "rel", Namespace: "rns"}) {
		t.Fatalf("%+v", g)
	}
	o.SetLabels(nil)
	if gitopsOf(o) != nil {
		t.Fatal("want nil")
	}
}

// ---- routing ----------------------------------------------------------------------------

func scaleParams(id string) protocol.ObjectActionParams {
	return protocol.ObjectActionParams{RequestID: id, Ref: depRef(), Action: "scale", Params: map[string]any{"replicas": float64(5)}}
}

func TestRouting(t *testing.T) {
	ctx := context.Background()
	t.Run("read allowed", func(t *testing.T) {
		e := newEnv(newConn(true, deployment("ns", "web")), newConn(true, deployment("ns", "web")))
		r, err := e.s.Action(ctx, scaleParams("1"))
		if err != nil || r.UsedContext != "c1" {
			t.Fatalf("%v %+v", err, r)
		}
		if lastAction(e.write, "patch") != nil {
			t.Fatal("write context used")
		}
	})
	t.Run("denied, write linked", func(t *testing.T) {
		e := newEnv(newConn(false, deployment("ns", "web")), newConn(true, deployment("ns", "web")))
		r, err := e.s.Action(ctx, scaleParams("1"))
		if err != nil || r.UsedContext != "c1-write" {
			t.Fatalf("%v %+v", err, r)
		}
		if lastAction(e.write, "patch") == nil || lastAction(e.read, "patch") != nil {
			t.Fatal("wrong context patched")
		}
		if e.write.az.calls.Load() != 0 {
			t.Fatal("no SSAR needed on write context")
		}
	})
	t.Run("denied no link", func(t *testing.T) {
		e := newEnv(newConn(false, deployment("ns", "web")), nil)
		_, err := e.s.Action(ctx, scaleParams("1"))
		if code(t, err) != "forbidden" || !strings.Contains(err.Error(), "no rbac") {
			t.Fatal(err)
		}
	})
	t.Run("denied autoswitch off", func(t *testing.T) {
		e := newEnv(newConn(false, deployment("ns", "web")), newConn(true, deployment("ns", "web")))
		e.auto = false
		_, err := e.s.Action(ctx, scaleParams("1"))
		if code(t, err) != "write_confirm" {
			t.Fatal(err)
		}
	})
	t.Run("write 403", func(t *testing.T) {
		w := newConn(true, deployment("ns", "web"))
		w.dyn.PrependReactor("patch", "deployments", func(ktesting.Action) (bool, runtime.Object, error) {
			return true, nil, apierrors.NewForbidden(schema.GroupResource{Resource: "deployments"}, "web", errors.New("no elevated session"))
		})
		w.az.fn = func(*authorizationv1.ResourceAttributes) (bool, string, error) {
			return false, "no elevated session", nil
		}
		e := newEnv(newConn(false, deployment("ns", "web")), w)
		_, err := e.s.Action(ctx, scaleParams("1"))
		if code(t, err) != "write_unavailable" || !strings.Contains(err.Error(), "no elevated session") {
			t.Fatal(err)
		}
	})
	t.Run("read 403 is forbidden", func(t *testing.T) {
		r := newConn(true, deployment("ns", "web"))
		r.dyn.PrependReactor("patch", "deployments", func(ktesting.Action) (bool, runtime.Object, error) {
			return true, nil, apierrors.NewForbidden(schema.GroupResource{Resource: "deployments"}, "web", errors.New("nope"))
		})
		e := newEnv(r, nil)
		_, err := e.s.Action(ctx, scaleParams("1"))
		if code(t, err) != "forbidden" {
			t.Fatal(err)
		}
	})
	t.Run("ssar error proceeds on read", func(t *testing.T) {
		r := newConn(true, deployment("ns", "web"))
		r.az.fn = func(*authorizationv1.ResourceAttributes) (bool, string, error) { return false, "", errors.New("boom") }
		e := newEnv(r, nil)
		res, err := e.s.Action(ctx, scaleParams("1"))
		if err != nil || res.UsedContext != "c1" {
			t.Fatalf("%v %+v", err, res)
		}
	})
}

func TestSSARCache(t *testing.T) {
	r := newConn(true, deployment("ns", "web"))
	e := newEnv(r, nil)
	for i := 0; i < 3; i++ {
		if _, err := e.s.Action(context.Background(), scaleParams(string(rune('a'+i)))); err != nil {
			t.Fatal(err)
		}
	}
	if n := r.az.calls.Load(); n != 1 {
		t.Fatalf("calls %d", n)
	}
	e.advance(59 * time.Second)
	_, _ = e.s.Action(context.Background(), scaleParams("x"))
	if n := r.az.calls.Load(); n != 1 {
		t.Fatalf("calls %d", n)
	}
	e.advance(2 * time.Second)
	_, _ = e.s.Action(context.Background(), scaleParams("y"))
	if n := r.az.calls.Load(); n != 2 {
		t.Fatalf("calls after expiry %d", n)
	}
}

// ---- dry run ----------------------------------------------------------------------------

func TestDryRunUpdateConflicts(t *testing.T) {
	live := deployment("ns", "web")
	setMeta(live, "managedFields", []any{
		map[string]any{"manager": "kustomize-controller", "operation": "Apply", "fieldsV1": map[string]any{
			"f:spec": map[string]any{"f:replicas": map[string]any{}}}},
		map[string]any{"manager": "helm", "operation": "Update", "fieldsV1": map[string]any{
			"f:spec": map[string]any{"f:template": map[string]any{"f:spec": map[string]any{"f:containers": map[string]any{
				`k:{"name":"app"}`: map[string]any{".": map[string]any{}, "f:image": map[string]any{}}}}}}}},
		map[string]any{"manager": "v8s", "operation": "Update", "fieldsV1": map[string]any{
			"f:spec": map[string]any{"f:replicas": map[string]any{}}}},
		map[string]any{"manager": "other", "operation": "Update", "fieldsV1": map[string]any{
			"f:spec": map[string]any{"f:paused": map[string]any{}}}},
	})
	e := newEnv(newConn(true, live), nil)
	var sent *unstructured.Unstructured
	e.read.dyn.PrependReactor("update", "deployments", func(a ktesting.Action) (bool, runtime.Object, error) {
		sent = a.(ktesting.UpdateAction).GetObject().(*unstructured.Unstructured)
		return true, sent, nil
	})
	y := `apiVersion: apps/v1
kind: Deployment
metadata: {name: web, namespace: ns, resourceVersion: "1"}
spec:
  replicas: 3
  template:
    spec:
      containers:
      - {name: app, image: "app:2"}
`
	res, err := e.s.DryRun(context.Background(), protocol.ObjectDryRunParams{Ref: depRef(), YAML: y, Mode: "update"})
	if err != nil || !res.OK || res.Error != nil {
		t.Fatalf("%v %+v", err, res)
	}
	if sent == nil || sent.GetResourceVersion() != "10" {
		t.Fatalf("rv must be the live one: %v", sent)
	}
	want := []protocol.FieldConflict{
		{Path: "spec.replicas", Manager: "kustomize-controller"},
		{Path: "spec.template.spec.containers[name=app].image", Manager: "helm"},
	}
	if len(res.Conflicts) != 2 || res.Conflicts[0] != want[0] || res.Conflicts[1] != want[1] {
		t.Fatalf("conflicts %+v", res.Conflicts)
	}
	if res.Warnings == nil {
		t.Fatal("warnings must be non-nil")
	}

	// name mismatch and multi-doc are bad requests
	if _, err = e.s.DryRun(context.Background(), protocol.ObjectDryRunParams{Ref: depRef(), YAML: strings.Replace(y, "name: web", "name: other", 1), Mode: "update"}); code(t, err) != "bad_request" {
		t.Fatal(err)
	}
	if _, err = e.s.DryRun(context.Background(), protocol.ObjectDryRunParams{Ref: depRef(), YAML: y + "---\n" + y, Mode: "update"}); code(t, err) != "bad_request" {
		t.Fatal(err)
	}
	if _, err = e.s.DryRun(context.Background(), protocol.ObjectDryRunParams{Ref: depRef(), YAML: "  \n", Mode: "update"}); code(t, err) != "bad_request" {
		t.Fatal(err)
	}

	// server rejection lands in the result
	e.read.dyn.PrependReactor("update", "deployments", func(ktesting.Action) (bool, runtime.Object, error) {
		return true, nil, apierrors.NewInvalid(schema.GroupKind{Kind: "Deployment"}, "web", nil)
	})
	res, err = e.s.DryRun(context.Background(), protocol.ObjectDryRunParams{Ref: depRef(), YAML: y, Mode: "update"})
	if err != nil || res.OK || res.Error == nil || res.Error.Code != "invalid" {
		t.Fatalf("%v %+v", err, res)
	}
}

func TestDiffPaths(t *testing.T) {
	a := map[string]any{"metadata": map[string]any{"resourceVersion": "1", "labels": map[string]any{"a": "1"}}, "spec": map[string]any{
		"list": []any{"a", "b"}, "cs": []any{map[string]any{"name": "x", "image": "1"}, map[string]any{"name": "y"}}}}
	b := map[string]any{"metadata": map[string]any{"resourceVersion": "2", "labels": map[string]any{"a": "1", "b": "2"}}, "spec": map[string]any{
		"list": []any{"a", "c"}, "cs": []any{map[string]any{"name": "x", "image": "2"}}}}
	var ps []string
	for _, p := range diffPaths(a, b, nil) {
		ps = append(ps, pathString(p))
	}
	got := strings.Join(ps, "|")
	want := "metadata.labels.b|spec.cs[name=x].image|spec.cs[name=y]|spec.list"
	if got != want {
		t.Fatalf("got %s want %s", got, want)
	}
}

func cmYAML(names ...string) string {
	var parts []string
	for _, n := range names {
		parts = append(parts, "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: "+n+"\ndata: {a: b}\n")
	}
	return strings.Join(parts, "---\n")
}

type applyRec struct {
	name, ns string
	pt       types.PatchType
	opts     metav1.PatchOptions
	body     map[string]any
}

func recordApply(c *fakeConn) *[]applyRec {
	var recs []applyRec
	var mu sync.Mutex
	c.dyn.PrependReactor("patch", "*", func(a ktesting.Action) (bool, runtime.Object, error) {
		pa := a.(ktesting.PatchAction)
		if pa.GetPatchType() != types.ApplyPatchType {
			return false, nil, nil
		}
		var body map[string]any
		_ = json.Unmarshal(pa.GetPatch(), &body)
		mu.Lock()
		recs = append(recs, applyRec{pa.GetName(), pa.GetNamespace(), pa.GetPatchType(), metav1.PatchOptions{}, body})
		n := len(recs)
		mu.Unlock()
		return true, setMeta(mk("v1", "ConfigMap", pa.GetNamespace(), pa.GetName(), nil), "resourceVersion", "rv"+string(rune('0'+n))), nil
	})
	return &recs
}

func TestDryRunCreate(t *testing.T) {
	e := newEnv(newConn(true), nil)
	recs := recordApply(e.read)
	res, err := e.s.DryRun(context.Background(), protocol.ObjectDryRunParams{Ref: protocol.ObjectRef{Cluster: "c1", Namespace: "team"}, YAML: cmYAML("a", "b"), Mode: "create"})
	if err != nil || !res.OK {
		t.Fatalf("%v %+v", err, res)
	}
	if len(*recs) != 2 || (*recs)[0].ns != "team" || (*recs)[1].name != "b" {
		t.Fatalf("%+v", *recs)
	}
	if res.Conflicts == nil || res.Warnings == nil {
		t.Fatal("slices must be non-nil")
	}
}

// ---- apply ------------------------------------------------------------------------------

const webYAML = `apiVersion: apps/v1
kind: Deployment
metadata: {name: web, namespace: ns}
spec: {replicas: 7}
`

func TestApplyUpdate(t *testing.T) {
	e := newEnv(newConn(true, deployment("ns", "web")), nil)
	var sent *unstructured.Unstructured
	e.read.dyn.PrependReactor("update", "deployments", func(a ktesting.Action) (bool, runtime.Object, error) {
		sent = a.(ktesting.UpdateAction).GetObject().(*unstructured.Unstructured)
		return true, setMeta(sent.DeepCopy(), "resourceVersion", "11"), nil
	})
	p := protocol.ObjectApplyParams{RequestID: "r1", Ref: depRef(), YAML: webYAML, Mode: "update", ResourceVersion: "10"}
	res, err := e.s.Apply(context.Background(), p)
	if err != nil || res.ResourceVersion != "11" || res.UsedContext != "c1" {
		t.Fatalf("%v %+v", err, res)
	}
	if sent.GetResourceVersion() != "10" {
		t.Fatalf("rv %q", sent.GetResourceVersion())
	}
	// missing rv
	p.RequestID, p.ResourceVersion = "r2", ""
	if _, err = e.s.Apply(context.Background(), p); code(t, err) != "bad_request" {
		t.Fatal(err)
	}
	// 409
	e.read.dyn.PrependReactor("update", "deployments", func(ktesting.Action) (bool, runtime.Object, error) {
		return true, nil, apierrors.NewConflict(schema.GroupResource{Group: "apps", Resource: "deployments"}, "web", errors.New("the object has been modified"))
	})
	p.RequestID, p.ResourceVersion = "r3", "9"
	_, err = e.s.Apply(context.Background(), p)
	if code(t, err) != "conflict" {
		t.Fatal(err)
	}
	live := err.(*Error).Body.Live
	if live == nil || live.ResourceVersion != "10" || !strings.Contains(live.YAML, "replicas: 2") {
		t.Fatalf("live %+v", live)
	}
}

func TestApplyCreateMultiDoc(t *testing.T) {
	e := newEnv(newConn(true), nil)
	recs := recordApply(e.read)
	var opts []metav1.PatchOptions
	e.read.dyn.PrependReactor("patch", "*", func(a ktesting.Action) (bool, runtime.Object, error) {
		if o, ok := a.(interface{ GetPatchOptions() metav1.PatchOptions }); ok {
			opts = append(opts, o.GetPatchOptions())
		}
		return false, nil, nil
	})
	y := cmYAML("a", "b") + "---\n---\n# nothing\n"
	res, err := e.s.Apply(context.Background(), protocol.ObjectApplyParams{RequestID: "c", Ref: protocol.ObjectRef{Cluster: "c1"}, YAML: y, Mode: "create"})
	if err != nil {
		t.Fatal(err)
	}
	if len(*recs) != 2 || res.ResourceVersion != "rv2" || res.UsedContext != "c1" {
		t.Fatalf("%+v %+v", res, *recs)
	}
	want := []protocol.ObjectRef{{Cluster: "c1", GVR: "v1/configmaps", Namespace: "default", Name: "a"}, {Cluster: "c1", GVR: "v1/configmaps", Namespace: "default", Name: "b"}}
	if len(res.Applied) != 2 || res.Applied[0] != want[0] || res.Applied[1] != want[1] {
		t.Fatalf("%+v", res.Applied)
	}
	for _, o := range opts {
		if o.FieldManager != "v8s" || o.Force == nil || *o.Force {
			t.Fatalf("opts %+v", o)
		}
	}
	// SSA conflict
	e.read.dyn.PrependReactor("patch", "*", func(a ktesting.Action) (bool, runtime.Object, error) {
		if a.(ktesting.PatchAction).GetPatchType() == types.ApplyPatchType {
			return true, nil, apierrors.NewConflict(schema.GroupResource{Resource: "configmaps"}, "a", errors.New("Apply failed with 1 conflict"))
		}
		return false, nil, nil
	})
	_, err = e.s.Apply(context.Background(), protocol.ObjectApplyParams{RequestID: "d", Ref: protocol.ObjectRef{Cluster: "c1"}, YAML: cmYAML("a"), Mode: "create"})
	if code(t, err) != "conflict" || err.(*Error).Body.Live != nil {
		t.Fatal(err)
	}
}

// ---- delete -----------------------------------------------------------------------------

func TestDelete(t *testing.T) {
	e := newEnv(newConn(true, deployment("ns", "web")), nil)
	var pol []metav1.DeletionPropagation
	e.read.dyn.PrependReactor("delete", "deployments", func(a ktesting.Action) (bool, runtime.Object, error) {
		if d, ok := a.(ktesting.DeleteActionImpl); ok && d.DeleteOptions.PropagationPolicy != nil {
			pol = append(pol, *d.DeleteOptions.PropagationPolicy)
		}
		return false, nil, nil
	})
	for i, tc := range []struct {
		in   string
		want metav1.DeletionPropagation
	}{{"", "Background"}, {"Foreground", "Foreground"}, {"Orphan", "Orphan"}} {
		_, err := e.s.Delete(context.Background(), protocol.ObjectDeleteParams{RequestID: string(rune('a' + i)), Ref: depRef(), Propagation: tc.in})
		if tc.in == "" && err != nil {
			t.Fatal(err)
		}
		if tc.in != "" && err != nil && code(t, err) != "not_found" {
			t.Fatal(err)
		}
		if len(pol) <= i || pol[i] != tc.want {
			t.Fatalf("policies %v", pol)
		}
		e.read.dyn.Tracker().Add(deployment("ns", "web"))
	}
	if _, err := e.s.Delete(context.Background(), protocol.ObjectDeleteParams{RequestID: "z", Ref: depRef(), Propagation: "Bogus"}); code(t, err) != "bad_request" {
		t.Fatal(err)
	}
	if _, err := e.s.Delete(context.Background(), protocol.ObjectDeleteParams{RequestID: "n", Ref: protocol.ObjectRef{Cluster: "c1", GVR: "apps/v1/deployments", Namespace: "ns", Name: "ghost"}}); code(t, err) != "not_found" {
		t.Fatal(err)
	}
}

// ---- actions ----------------------------------------------------------------------------

func patchOf(t *testing.T, c *fakeConn) (types.PatchType, string) {
	t.Helper()
	a := lastAction(c, "patch")
	if a == nil {
		t.Fatal("no patch sent")
	}
	pa := a.(ktesting.PatchAction)
	return pa.GetPatchType(), string(pa.GetPatch())
}

func act(e *env, id, gvr, name, action string, params map[string]any) (protocol.MutationResult, error) {
	ns := "ns"
	if gvr == "v1/nodes" {
		ns = ""
	}
	return e.s.Action(context.Background(), protocol.ObjectActionParams{RequestID: id, Ref: protocol.ObjectRef{Cluster: "c1", GVR: gvr, Namespace: ns, Name: name}, Action: action, Params: params})
}

func TestActionsSimple(t *testing.T) {
	node := mk("v1", "Node", "", "n1", nil)
	cj := mk("batch/v1", "CronJob", "ns", "cj", map[string]any{"spec": map[string]any{"suspend": false}})
	e := newEnv(newConn(true, deployment("ns", "web"), node, cj, mk("apps/v1", "StatefulSet", "ns", "db", nil)), nil)

	cases := []struct {
		gvr, name, action string
		params            map[string]any
		want              string
	}{
		{"apps/v1/deployments", "web", "scale", map[string]any{"replicas": uint64(4)}, `{"spec":{"replicas":4}}`},
		{"apps/v1/statefulsets", "db", "scale", map[string]any{"replicas": int64(0)}, `{"spec":{"replicas":0}}`},
		{"apps/v1/deployments", "web", "restart", nil, `{"spec":{"template":{"metadata":{"annotations":{"kubectl.kubernetes.io/restartedAt":"2026-01-02T03:04:05Z"}}}}}`},
		{"v1/nodes", "n1", "cordon", nil, `{"spec":{"unschedulable":true}}`},
		{"v1/nodes", "n1", "uncordon", nil, `{"spec":{"unschedulable":false}}`},
		{"batch/v1/cronjobs", "cj", "suspend", nil, `{"spec":{"suspend":true}}`},
		{"batch/v1/cronjobs", "cj", "resume", nil, `{"spec":{"suspend":false}}`},
	}
	for i, c := range cases {
		res, err := act(e, string(rune('a'+i)), c.gvr, c.name, c.action, c.params)
		if err != nil || res.UsedContext != "c1" {
			t.Fatalf("%s: %v %+v", c.action, err, res)
		}
		pt, body := patchOf(t, e.read)
		if pt != types.MergePatchType || body != c.want {
			t.Fatalf("%s: %s %s", c.action, pt, body)
		}
	}
	// wrong kind / unknown / bad params
	bad := []struct {
		gvr, action string
		p           map[string]any
	}{
		{"v1/nodes", "scale", map[string]any{"replicas": 1}},
		{"apps/v1/deployments", "cordon", nil},
		{"apps/v1/deployments", "explode", nil},
		{"apps/v1/deployments", "scale", map[string]any{"replicas": -1}},
		{"apps/v1/deployments", "scale", map[string]any{"replicas": 1.5}},
		{"apps/v1/deployments", "scale", nil},
		{"batch/v1/cronjobs", "restart", nil},
	}
	for i, b := range bad {
		if _, err := act(e, "bad"+string(rune('a'+i)), b.gvr, "x", b.action, b.p); code(t, err) != "bad_request" {
			t.Fatalf("%v: %v", b, err)
		}
	}
	if _, err := act(e, "", "apps/v1/deployments", "web", "restart", nil); code(t, err) != "bad_request" {
		t.Fatal("requestId required")
	}
}

func TestActionTrigger(t *testing.T) {
	long := strings.Repeat("x", 60)
	cj := mk("batch/v1", "CronJob", "ns", long, map[string]any{"spec": map[string]any{"jobTemplate": map[string]any{
		"metadata": map[string]any{"labels": map[string]any{"l": "1"}, "annotations": map[string]any{"a": "1"}},
		"spec":     map[string]any{"template": map[string]any{"spec": map[string]any{"restartPolicy": "Never"}}},
	}}})
	setMeta(cj, "uid", "cj-uid")
	e := newEnv(newConn(true, cj), nil)
	res, err := act(e, "t", "batch/v1/cronjobs", long, "trigger", nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Name) > 63 || !regexp.MustCompile(`^x+-manual-[a-z0-9]{5}$`).MatchString(res.Name) {
		t.Fatalf("name %q (%d)", res.Name, len(res.Name))
	}
	job := lastAction(e.read, "create").(ktesting.CreateAction).GetObject().(*unstructured.Unstructured)
	if job.GetKind() != "Job" || job.GetNamespace() != "ns" || job.GetName() != res.Name {
		t.Fatalf("%+v", job.Object)
	}
	if job.GetLabels()["l"] != "1" || job.GetAnnotations()["a"] != "1" || job.GetAnnotations()["cronjob.kubernetes.io/instantiate"] != "manual" {
		t.Fatalf("meta %v %v", job.GetLabels(), job.GetAnnotations())
	}
	owners := job.GetOwnerReferences()
	if len(owners) != 1 || owners[0].Kind != "CronJob" || owners[0].Name != long || string(owners[0].UID) != "cj-uid" ||
		owners[0].Controller == nil || !*owners[0].Controller || owners[0].BlockOwnerDeletion == nil || !*owners[0].BlockOwnerDeletion {
		t.Fatalf("owners %+v", owners)
	}
	if rp, _, _ := unstructured.NestedString(job.Object, "spec", "template", "spec", "restartPolicy"); rp != "Never" {
		t.Fatal("spec not copied")
	}
}

func rsFor(name, uid, rev, hash string) *unstructured.Unstructured {
	o := mk("apps/v1", "ReplicaSet", "ns", name, map[string]any{"spec": map[string]any{"template": map[string]any{
		"metadata": map[string]any{"labels": map[string]any{"app": "web", "pod-template-hash": hash}},
		"spec":     map[string]any{"containers": []any{map[string]any{"name": "app", "image": "app:" + rev}}},
	}}})
	o.SetOwnerReferences([]metav1.OwnerReference{{APIVersion: "apps/v1", Kind: "Deployment", Name: "web", UID: types.UID(uid)}})
	o.SetAnnotations(map[string]string{"deployment.kubernetes.io/revision": rev})
	return o
}

func TestActionRollback(t *testing.T) {
	e := newEnv(newConn(true, deployment("ns", "web"), rsFor("rs1", "dep-uid", "1", "aaa"), rsFor("rs2", "dep-uid", "2", "bbb"), rsFor("other", "zzz", "1", "ccc")), nil)
	res, err := act(e, "r", "apps/v1/deployments", "web", "rollback", map[string]any{"revision": int64(1)})
	if err != nil || res.UsedContext != "c1" {
		t.Fatalf("%v %+v", err, res)
	}
	pt, body := patchOf(t, e.read)
	if pt != types.JSONPatchType {
		t.Fatal(pt)
	}
	var ops []struct {
		Op, Path string
		Value    map[string]any
	}
	if err := json.Unmarshal([]byte(body), &ops); err != nil || len(ops) != 1 || ops[0].Op != "replace" || ops[0].Path != "/spec/template" {
		t.Fatalf("%s", body)
	}
	if strings.Contains(body, "pod-template-hash") || !strings.Contains(body, `"image":"app:1"`) {
		t.Fatalf("template %s", body)
	}
	if _, err = act(e, "r2", "apps/v1/deployments", "web", "rollback", map[string]any{"revision": 9}); code(t, err) != "not_found" {
		t.Fatal(err)
	}
}

// ---- dedupe -----------------------------------------------------------------------------

func TestDedupeReplayAndExpiry(t *testing.T) {
	e := newEnv(newConn(true, deployment("ns", "web")), nil)
	r1, err := e.s.Action(context.Background(), scaleParams("same"))
	if err != nil {
		t.Fatal(err)
	}
	patches := func() int {
		n := 0
		for _, a := range e.read.dyn.Actions() {
			if a.GetVerb() == "patch" {
				n++
			}
		}
		return n
	}
	r2, err := e.s.Action(context.Background(), scaleParams("same"))
	if err != nil || r2 != r1 || patches() != 1 {
		t.Fatalf("replay: %v %+v patches=%d", err, r2, patches())
	}
	// method namespaces are separate
	if _, err := e.s.Delete(context.Background(), protocol.ObjectDeleteParams{RequestID: "same", Ref: depRef()}); err != nil {
		t.Fatal(err)
	}
	e.advance(9 * time.Minute)
	_, _ = e.s.Action(context.Background(), scaleParams("same"))
	if patches() != 1 {
		t.Fatal("should still dedupe")
	}
	e.advance(2 * time.Minute)
	e.read.dyn.Tracker().Add(deployment("ns", "web"))
	_, _ = e.s.Action(context.Background(), scaleParams("same"))
	if patches() != 2 {
		t.Fatalf("should re-execute after expiry, patches=%d", patches())
	}
}

func TestDedupeStoresErrors(t *testing.T) {
	e := newEnv(newConn(true), nil)
	_, err1 := e.s.Delete(context.Background(), protocol.ObjectDeleteParams{RequestID: "e", Ref: depRef()})
	e.read.dyn.Tracker().Add(deployment("ns", "web"))
	_, err2 := e.s.Delete(context.Background(), protocol.ObjectDeleteParams{RequestID: "e", Ref: depRef()})
	if code(t, err1) != "not_found" || err1 != err2 {
		t.Fatal(err1, err2)
	}
}

func TestDedupeConcurrent(t *testing.T) {
	e := newEnv(newConn(true, deployment("ns", "web")), nil)
	release := make(chan struct{})
	var calls atomic.Int32
	e.read.dyn.PrependReactor("patch", "deployments", func(ktesting.Action) (bool, runtime.Object, error) {
		calls.Add(1)
		<-release
		return false, nil, nil
	})
	var wg sync.WaitGroup
	results := make([]protocol.MutationResult, 4)
	for i := range results {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results[i], _ = e.s.Action(context.Background(), scaleParams("conc"))
		}()
	}
	time.Sleep(100 * time.Millisecond)
	close(release)
	wg.Wait()
	if calls.Load() != 1 {
		t.Fatalf("executed %d times", calls.Load())
	}
	for _, r := range results {
		if r != results[0] || r.UsedContext != "c1" {
			t.Fatalf("%+v", results)
		}
	}
}

func TestDedupeCap(t *testing.T) {
	s := New(Config{})
	for i := 0; i < dedupeMax+50; i++ {
		_, err := dedupe(context.Background(), s, "m", "id"+strings.Repeat("x", i%7)+string(rune(i)), func() (int, error) { return 1, nil })
		if err != nil {
			t.Fatal(err)
		}
	}
	if len(s.dd) > dedupeMax {
		t.Fatalf("size %d", len(s.dd))
	}
}

// ---- access -----------------------------------------------------------------------------

func TestAccess(t *testing.T) {
	r := newConn(false)
	r.az.fn = func(ra *authorizationv1.ResourceAttributes) (bool, string, error) {
		switch ra.Resource {
		case "pods":
			return true, "", nil
		case "broken":
			return false, "", errors.New("connection refused")
		}
		return false, "RBAC: no", nil
	}
	w := newConn(false)
	w.az.fn = func(ra *authorizationv1.ResourceAttributes) (bool, string, error) {
		return ra.Resource == "deployments", "", nil
	}
	e := newEnv(r, w)
	got := e.s.Access(context.Background(), []protocol.AccessCheck{
		{Cluster: "c1", Verb: "get", GVR: "v1/pods", Namespace: "ns"},
		{Cluster: "c1", Verb: "patch", GVR: "apps/v1/deployments", Namespace: "ns", Name: "web"},
		{Cluster: "c1", Verb: "delete", GVR: "apps/v1/statefulsets"},
		{Cluster: "c1", Verb: "get", GVR: "v1/broken"},
	})
	if len(got) != 4 || !got[0].Allowed || got[0].ViaWrite {
		t.Fatalf("%+v", got)
	}
	if !got[1].Allowed || !got[1].ViaWrite {
		t.Fatalf("%+v", got[1])
	}
	if got[2].Allowed || got[2].Reason != "RBAC: no" {
		t.Fatalf("%+v", got[2])
	}
	// A read whose review fails is never escalated to the write context: unable to check.
	if got[3].Allowed || got[3].NeedsConfirm || got[3].Reason != "unable to check: connection refused" {
		t.Fatalf("%+v", got[3])
	}
	// no link, empty reason
	e2 := newEnv(newConn(false), nil)
	e2.read.az.fn = func(*authorizationv1.ResourceAttributes) (bool, string, error) { return false, "", nil }
	if g := e2.s.Access(context.Background(), []protocol.AccessCheck{{Cluster: "c1", Verb: "get", GVR: "v1/pods"}}); g[0].Reason != "denied" {
		t.Fatalf("%+v", g)
	}
}

// ---- watch ------------------------------------------------------------------------------

func TestWatch(t *testing.T) {
	e := newEnv(newConn(true, deployment("ns", "web")), nil)
	fw := watch.NewFakeWithChanSize(10, false)
	var gotSel string
	e.read.dyn.PrependWatchReactor("deployments", func(a ktesting.Action) (bool, watch.Interface, error) {
		gotSel = a.(ktesting.WatchActionImpl).ListOptions.FieldSelector
		return true, fw, nil
	})
	frames := make(chan protocol.ObjectFrame, 10)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- e.s.Watch(ctx, depRef(), false, false, func(f protocol.ObjectFrame) { frames <- f }) }()

	next := func() protocol.ObjectFrame {
		select {
		case f := <-frames:
			return f
		case <-time.After(3 * time.Second):
			t.Fatal("timeout waiting for frame")
			return protocol.ObjectFrame{}
		}
	}
	f := next()
	if f.Deleted || f.ResourceVersion != "10" || !strings.Contains(f.YAML, "replicas: 2") {
		t.Fatalf("initial %+v", f)
	}
	mod := deployment("ns", "web")
	_ = unstructured.SetNestedField(mod.Object, int64(9), "spec", "replicas")
	setMeta(mod, "resourceVersion", "11")
	fw.Modify(mod)
	f = next()
	if f.Deleted || f.ResourceVersion != "11" || !strings.Contains(f.YAML, "replicas: 9") {
		t.Fatalf("modified %+v", f)
	}
	fw.Delete(mod)
	f = next()
	if !f.Deleted || f.ResourceVersion != "11" || !strings.Contains(f.YAML, "replicas: 9") {
		t.Fatalf("deleted %+v", f)
	}
	if gotSel != "metadata.name=web" {
		t.Fatalf("selector %q", gotSel)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("Watch did not return")
	}
	if _, err := e.s.Get(context.Background(), protocol.ObjectGetParams{Cluster: "c1", GVR: "apps/v1/deployments", Namespace: "ns", Name: "web"}); err != nil {
		t.Fatal(err)
	}
}

func TestWatchNotFound(t *testing.T) {
	e := newEnv(newConn(true), nil)
	err := e.s.Watch(context.Background(), depRef(), false, false, func(protocol.ObjectFrame) {})
	if code(t, err) != "not_found" {
		t.Fatal(err)
	}
}

// A gateway's read-only target may refuse even the access review (the POST): with a linked write context, that
// read context is treated like a refusal, for the action menu (access.check) and for the mutation's routing.
func TestReadContextCannotCheck(t *testing.T) {
	ctx := context.Background()
	refused := func(*authorizationv1.ResourceAttributes) (bool, string, error) {
		return false, "", &url.Error{Op: "Post", URL: "https://gw.example/k8s-ro/apis/authorization.k8s.io/v1/selfsubjectaccessreviews", Err: errors.New("EOF")}
	}
	check := []protocol.AccessCheck{{Cluster: "c1", Verb: "delete", GVR: "apps/v1/deployments", Namespace: "ns", Name: "web"}}

	t.Run("no write context: unable to check, without the URL", func(t *testing.T) {
		e := newEnv(newConn(false), nil)
		e.read.az.fn = refused
		g := e.s.Access(ctx, check)[0]
		if g.Allowed || g.Reason != "unable to check: EOF" {
			t.Fatalf("%+v", g)
		}
	})
	t.Run("write context allows: via write, or after confirmation", func(t *testing.T) {
		e := newEnv(newConn(false), newConn(true))
		e.read.az.fn = refused
		if g := e.s.Access(ctx, check)[0]; !g.Allowed || !g.ViaWrite {
			t.Fatalf("auto: %+v", g)
		}
		e2 := newEnv(newConn(false), newConn(true))
		e2.read.az.fn = refused
		e2.auto = false
		if g := e2.s.Access(ctx, check)[0]; g.Allowed || !g.NeedsConfirm {
			t.Fatalf("confirm: %+v", g)
		}
	})
	t.Run("write context cannot check either: offered through it, with a note", func(t *testing.T) {
		w := newConn(true)
		w.az.fn = refused
		e := newEnv(newConn(false), w)
		e.read.az.fn = refused
		g := e.s.Access(ctx, check)[0]
		if !g.Allowed || !g.ViaWrite || !strings.Contains(g.Reason, "elevated session") {
			t.Fatalf("%+v", g)
		}
	})
	t.Run("confirm mode, write context cannot check either: needs confirmation, with the note", func(t *testing.T) {
		w := newConn(true)
		w.az.fn = refused
		e := newEnv(newConn(false), w)
		e.read.az.fn = refused
		e.auto = false
		g := e.s.Access(ctx, check)[0]
		if g.Allowed || !g.NeedsConfirm || !strings.Contains(g.Reason, "elevated session") {
			t.Fatalf("%+v", g)
		}
	})
	t.Run("reads stay on the read context", func(t *testing.T) {
		e := newEnv(newConn(false), newConn(true))
		e.read.az.fn = refused
		r, err := e.s.Route(ctx, "c1", "get", "v1/pods", "log", "ns", "web-0")
		if err != nil || r.ViaWrite || r.ContextID != "c1" {
			t.Fatalf("%+v %v", r, err)
		}
	})
	t.Run("a transient review failure does not escalate", func(t *testing.T) {
		for name, terr := range map[string]error{
			"deadline":    context.DeadlineExceeded,
			"unavailable": apierrors.NewServiceUnavailable("restarting"),
			"throttled":   apierrors.NewTooManyRequests("slow down", 1),
		} {
			e := newEnv(newConn(false), newConn(true))
			e.read.az.fn = func(*authorizationv1.ResourceAttributes) (bool, string, error) { return false, "", terr }
			r, err := e.s.Route(ctx, "c1", "patch", "apps/v1/deployments", "", "ns", "web")
			if err != nil || r.ViaWrite || r.ContextID != "c1" {
				t.Fatalf("%s: %+v %v", name, r, err)
			}
			if g := e.s.Access(ctx, check)[0]; g.Allowed || g.NeedsConfirm || !strings.HasPrefix(g.Reason, "unable to check: ") {
				t.Fatalf("%s access: %+v", name, g)
			}
		}
	})
	t.Run("mutation goes to the write context", func(t *testing.T) {
		e := newEnv(newConn(false, deployment("ns", "web")), newConn(true, deployment("ns", "web")))
		e.read.az.fn = refused
		if _, err := e.s.Action(ctx, scaleParams("1")); err != nil {
			t.Fatal(err)
		}
		if lastAction(e.write, "patch") == nil || lastAction(e.read, "patch") != nil {
			t.Fatal("wrong context patched")
		}
		e2 := newEnv(newConn(false, deployment("ns", "web")), newConn(true, deployment("ns", "web")))
		e2.read.az.fn = refused
		e2.auto = false
		if _, err := e2.s.Action(ctx, scaleParams("1")); code(t, err) != "write_confirm" {
			t.Fatal(err)
		}
	})
}
