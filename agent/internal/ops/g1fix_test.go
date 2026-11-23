package ops

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/zyno-io/v8s/agent/pkg/protocol"
	authorizationv1 "k8s.io/api/authorization/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	ktesting "k8s.io/client-go/testing"
)

// Regression: a cancelled caller must neither abort the mutation nor make a retry repeat it (dedupe).
func TestDedupeSurvivesCancelledCaller(t *testing.T) {
	e := newEnv(newConn(true, deployment("ns", "web")), nil)
	var patches atomic.Int32
	release := make(chan struct{})
	started := make(chan struct{}, 1)
	e.read.dyn.PrependReactor("patch", "deployments", func(ktesting.Action) (bool, runtime.Object, error) {
		patches.Add(1)
		select {
		case started <- struct{}{}:
		default:
		}
		<-release
		return false, nil, nil
	})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { _, err := e.s.Action(ctx, scaleParams("same")); done <- err }()
	<-started
	cancel() // the connection drops while the patch is in flight
	close(release)
	<-done
	// the retry after a reconnect replays the recorded outcome instead of patching again
	res, err := e.s.Action(context.Background(), scaleParams("same"))
	if err != nil || res.UsedContext != "c1" {
		t.Fatalf("%v %+v", err, res)
	}
	if n := patches.Load(); n != 1 {
		t.Fatalf("patched %d times", n)
	}
}

// Regression: label "app" must not collide with Helm-owned "app.kubernetes.io/name"; list items are addressed
// by the real list keys of managedFields (ports by containerPort+protocol, mounts by mountPath).
func TestConflictsUseFieldPathsAndRealListKeys(t *testing.T) {
	live := mk("apps/v1", "Deployment", "ns", "web", map[string]any{"spec": map[string]any{
		"ports": []any{
			map[string]any{"containerPort": int64(80), "protocol": "TCP", "name": "http"},
			map[string]any{"containerPort": int64(80), "protocol": "UDP", "name": "dns"},
		},
		"mounts": []any{
			map[string]any{"mountPath": "/a", "name": "x"},
			map[string]any{"mountPath": "/b", "name": "x"},
		},
	}})
	setMeta(live, "labels", map[string]any{"app": "web", "app.kubernetes.io/name": "web"})
	setMeta(live, "managedFields", []any{map[string]any{"manager": "helm", "operation": "Update", "fieldsV1": map[string]any{
		"f:metadata": map[string]any{"f:labels": map[string]any{"f:app.kubernetes.io/name": map[string]any{}}},
		"f:spec": map[string]any{
			"f:ports":  map[string]any{`k:{"containerPort":80,"protocol":"UDP"}`: map[string]any{".": map[string]any{}, "f:name": map[string]any{}}},
			"f:mounts": map[string]any{`k:{"mountPath":"/b"}`: map[string]any{".": map[string]any{}, "f:name": map[string]any{}}},
		},
	}}})
	sub := func(f func(m map[string]any)) map[string]any {
		o := live.DeepCopy().Object
		f(o)
		return o
	}
	paths := func(o map[string]any) []string {
		var out []string
		for _, c := range conflictsFor(live, o) {
			out = append(out, c.Path)
		}
		return out
	}
	// changing the "app" label only: not owned by helm (only app.kubernetes.io/name is)
	if got := paths(sub(func(m map[string]any) {
		_ = unstructured.SetNestedField(m, "other", "metadata", "labels", "app")
	})); len(got) != 0 {
		t.Fatalf("label app collided: %v", got)
	}
	if got := paths(sub(func(m map[string]any) {
		_ = unstructured.SetNestedField(m, "other", "metadata", "labels", "app.kubernetes.io/name")
	})); len(got) != 1 || got[0] != "metadata.labels.app.kubernetes.io/name" {
		t.Fatalf("owned label: %v", got)
	}
	ports := func(tcp, udp string) map[string]any {
		return sub(func(m map[string]any) {
			_ = unstructured.SetNestedSlice(m, []any{
				map[string]any{"containerPort": int64(80), "protocol": "TCP", "name": tcp},
				map[string]any{"containerPort": int64(80), "protocol": "UDP", "name": udp},
			}, "spec", "ports")
		})
	}
	if got := paths(ports("web", "dns")); len(got) != 0 { // TCP item renamed: helm owns only the UDP one
		t.Fatalf("tcp port: %v", got)
	}
	if got := paths(ports("http", "dns2")); len(got) != 1 || !strings.Contains(got[0], "protocol=UDP") {
		t.Fatalf("udp port: %v", got)
	}
	mounts := func(a, b string) map[string]any {
		return sub(func(m map[string]any) {
			_ = unstructured.SetNestedSlice(m, []any{map[string]any{"mountPath": "/a", "name": a}, map[string]any{"mountPath": "/b", "name": b}}, "spec", "mounts")
		})
	}
	if got := paths(mounts("y", "x")); len(got) != 0 {
		t.Fatalf("mount /a: %v", got)
	}
	if got := paths(mounts("x", "y")); len(got) != 1 || !strings.Contains(got[0], "mountPath=/b") {
		t.Fatalf("mount /b: %v", got)
	}
}

func TestPrefixAndChildOwnership(t *testing.T) {
	live := deployment("ns", "web")
	setMeta(live, "managedFields", []any{
		map[string]any{"manager": "parent", "operation": "Update", "fieldsV1": map[string]any{"f:spec": map[string]any{"f:template": map[string]any{}}}},
		map[string]any{"manager": "child", "operation": "Update", "fieldsV1": map[string]any{"f:spec": map[string]any{"f:template": map[string]any{"f:spec": map[string]any{"f:containers": map[string]any{
			`k:{"name":"app"}`: map[string]any{"f:image": map[string]any{}}}}}}}},
	})
	o := live.DeepCopy().Object
	_ = unstructured.SetNestedField(o, "b", "spec", "template", "spec", "containers") // replaces the whole list: a prefix of the child's field
	_ = unstructured.SetNestedSlice(o, []any{map[string]any{"name": "app", "image": "app:2"}}, "spec", "template", "spec", "containers")
	got := map[string]bool{}
	for _, c := range conflictsFor(live, o) {
		got[c.Manager] = true
	}
	if !got["parent"] || !got["child"] {
		t.Fatalf("%v", got)
	}
}

// Regression: admission/quota/PodSecurity 403s through the write context keep their own code; write_unavailable
// only when the write context's own access review also denies.
func TestWriteForbiddenClassification(t *testing.T) {
	ctx := context.Background()
	mk403 := func(w *fakeConn) {
		w.dyn.PrependReactor("patch", "deployments", func(ktesting.Action) (bool, runtime.Object, error) {
			return true, nil, apierrors.NewForbidden(schema.GroupResource{Resource: "deployments"}, "web", errors.New("violates PodSecurity \"restricted\""))
		})
	}
	w := newConn(true, deployment("ns", "web")) // SSAR on the write context allows
	mk403(w)
	e := newEnv(newConn(false, deployment("ns", "web")), w)
	_, err := e.s.Action(ctx, scaleParams("a"))
	if code(t, err) != "forbidden" || !strings.Contains(err.Error(), "PodSecurity") {
		t.Fatalf("admission 403: %v", err)
	}
	w2 := newConn(true, deployment("ns", "web"))
	mk403(w2)
	w2.az.fn = func(*authorizationv1.ResourceAttributes) (bool, string, error) {
		return false, "", errors.New("gateway: no session")
	}
	e = newEnv(newConn(false, deployment("ns", "web")), w2)
	if _, err = e.s.Action(ctx, scaleParams("b")); code(t, err) != "write_unavailable" {
		t.Fatalf("review error: %v", err)
	}
	w3 := newConn(true, deployment("ns", "web"))
	w3.dyn.PrependReactor("patch", "deployments", func(ktesting.Action) (bool, runtime.Object, error) {
		return true, nil, apierrors.NewUnauthorized("token expired")
	})
	e = newEnv(newConn(false, deployment("ns", "web")), w3)
	if _, err = e.s.Action(ctx, scaleParams("c")); code(t, err) != "write_unavailable" {
		t.Fatalf("401: %v", err)
	}
}

func TestDryRunServerRejectionsAreResults(t *testing.T) {
	e := newEnv(newConn(true, deployment("ns", "web")), nil)
	e.read.dyn.PrependReactor("update", "deployments", func(ktesting.Action) (bool, runtime.Object, error) {
		return true, nil, apierrors.NewForbidden(schema.GroupResource{Resource: "deployments"}, "web", errors.New("denied by policy"))
	})
	res, err := e.s.DryRun(context.Background(), protocol.ObjectDryRunParams{Ref: depRef(), YAML: webYAML, Mode: "update"})
	if err != nil || res.OK || res.Error == nil || res.Error.Code != "forbidden" || !strings.Contains(res.Error.Message, "denied by policy") {
		t.Fatalf("%v %+v", err, res)
	}
	e.read.dyn.PrependReactor("update", "deployments", func(ktesting.Action) (bool, runtime.Object, error) {
		return true, nil, apierrors.NewUnauthorized("expired")
	})
	if _, err = e.s.DryRun(context.Background(), protocol.ObjectDryRunParams{Ref: depRef(), YAML: webYAML, Mode: "update"}); err == nil {
		t.Fatal("a 401 is a failed request")
	}
}

// Regression: with autoSwitchWrite off the UI gets write_confirm and retries with useWriteContext;
// access.check reflects the setting.
func TestUseWriteContextAndWriteConfirm(t *testing.T) {
	ctx := context.Background()
	e := newEnv(newConn(false, deployment("ns", "web")), newConn(true, deployment("ns", "web")))
	e.auto = false
	if _, err := e.s.Action(ctx, scaleParams("1")); code(t, err) != "write_confirm" {
		t.Fatal(err)
	}
	p := scaleParams("2")
	p.UseWriteContext = true
	res, err := e.s.Action(ctx, p)
	if err != nil || res.UsedContext != "c1-write" {
		t.Fatalf("%v %+v", err, res)
	}
	e.write.az.fn = func(*authorizationv1.ResourceAttributes) (bool, string, error) { return true, "", nil }
	got := e.s.Access(ctx, []protocol.AccessCheck{{Cluster: "c1", Verb: "patch", GVR: "apps/v1/deployments", Namespace: "ns", Name: "web"}})
	if got[0].Allowed || got[0].ViaWrite || !got[0].NeedsConfirm {
		t.Fatalf("%+v", got[0])
	}
	e.auto = true
	e.s.ssar = map[string]ssarEntry{}
	got = e.s.Access(ctx, []protocol.AccessCheck{{Cluster: "c1", Verb: "patch", GVR: "apps/v1/deployments", Namespace: "ns", Name: "web"}})
	if !got[0].Allowed || !got[0].ViaWrite || got[0].NeedsConfirm {
		t.Fatalf("%+v", got[0])
	}
	// no link: the flag is ignored
	e2 := newEnv(newConn(false, deployment("ns", "web")), nil)
	p.RequestID = "3"
	if _, err := e2.s.Action(ctx, p); code(t, err) != "forbidden" {
		t.Fatal(err)
	}
}

func ns(uid string) *unstructured.Unstructured {
	o := mk("v1", "Namespace", "", "kube-system", nil)
	return setMeta(o, "uid", uid)
}

func TestLinkMismatch(t *testing.T) {
	ctx := context.Background()
	e := newEnv(newConn(false, deployment("ns", "web"), ns("aaa")), newConn(true, deployment("ns", "web"), ns("bbb")))
	_, err := e.s.Action(ctx, scaleParams("1"))
	if code(t, err) != "link_mismatch" {
		t.Fatal(err)
	}
	if lastAction(e.write, "patch") != nil {
		t.Fatal("wrote through a mislinked context")
	}
	e = newEnv(newConn(false, deployment("ns", "web"), ns("same")), newConn(true, deployment("ns", "web"), ns("same")))
	if _, err = e.s.Action(ctx, scaleParams("1")); err != nil {
		t.Fatal(err)
	}
	// verified once, then cached
	gets := 0
	e.write.dyn.PrependReactor("get", "namespaces", func(ktesting.Action) (bool, runtime.Object, error) { gets++; return false, nil, nil })
	_, _ = e.s.Action(ctx, scaleParams("2"))
	if gets != 0 {
		t.Fatalf("link re-checked %d times", gets)
	}
}

func secretObj(ns, name string) *unstructured.Unstructured {
	o := mk("v1", "Secret", ns, name, map[string]any{"data": map[string]any{"pw": "c2VjcmV0", "user": "YWRtaW4="}})
	setMeta(o, "annotations", map[string]any{lastApplied: `{"data":{"pw":"c2VjcmV0"}}`, "keep": "me"})
	setMeta(o, "uid", "sec-uid")
	return setMeta(o, "resourceVersion", "5")
}

func TestSecretRevealGate(t *testing.T) {
	ctx := context.Background()
	e := newEnv(newConn(true, secretObj("ns", "s")), nil)
	get := func(reveal bool) *protocol.ObjectPayload {
		p, err := e.s.Get(ctx, protocol.ObjectGetParams{Cluster: "c1", GVR: "v1/secrets", Namespace: "ns", Name: "s", Reveal: reveal})
		if err != nil {
			t.Fatal(err)
		}
		return p
	}
	m := get(false)
	if strings.Contains(m.YAML, "c2VjcmV0") || strings.Contains(m.YAML, "YWRtaW4=") || strings.Contains(m.YAML, "last-applied") ||
		!strings.Contains(m.YAML, "pw: "+protocol.MaskedValue) || !strings.Contains(m.YAML, "keep: me") {
		t.Fatalf("masked payload:\n%s", m.YAML)
	}
	r := get(true)
	if !strings.Contains(r.YAML, "c2VjcmV0") || !strings.Contains(r.YAML, "last-applied") {
		t.Fatalf("revealed payload:\n%s", r.YAML)
	}
	// the conflict `live` payload is masked as well
	var sent *unstructured.Unstructured
	e.read.dyn.PrependReactor("update", "secrets", func(a ktesting.Action) (bool, runtime.Object, error) {
		sent = a.(ktesting.UpdateAction).GetObject().(*unstructured.Unstructured)
		if sent.GetResourceVersion() == "9" {
			return true, nil, apierrors.NewConflict(schema.GroupResource{Resource: "secrets"}, "s", errors.New("modified"))
		}
		return true, sent, nil
	})
	ref := protocol.ObjectRef{Cluster: "c1", GVR: "v1/secrets", Namespace: "ns", Name: "s"}
	y := "apiVersion: v1\nkind: Secret\nmetadata: {name: s, namespace: ns}\ndata:\n  pw: " + protocol.MaskedValue + "\n  user: bmV3\n"
	_, err := e.s.Apply(ctx, protocol.ObjectApplyParams{RequestID: "x", Ref: ref, YAML: y, Mode: "update", ResourceVersion: "9"})
	if code(t, err) != "conflict" || strings.Contains(err.(*Error).Body.Live.YAML, "c2VjcmV0") {
		t.Fatalf("live must be masked: %v", err)
	}
	// update with a masked sentinel keeps the live value of that key and the last-applied annotation
	if _, err = e.s.Apply(ctx, protocol.ObjectApplyParams{RequestID: "y", Ref: ref, YAML: y, Mode: "update", ResourceVersion: "5"}); err != nil {
		t.Fatal(err)
	}
	d, _, _ := unstructured.NestedStringMap(sent.Object, "data")
	if d["pw"] != "c2VjcmV0" || d["user"] != "bmV3" {
		t.Fatalf("data %v", d)
	}
	if sent.GetAnnotations()[lastApplied] == "" {
		t.Fatal("last-applied annotation was dropped")
	}
	// a sentinel for a key that does not exist is rejected
	bad := strings.Replace(y, "pw:", "nokey:", 1)
	if _, err = e.s.Apply(ctx, protocol.ObjectApplyParams{RequestID: "z", Ref: ref, YAML: bad, Mode: "update", ResourceVersion: "5"}); code(t, err) != "bad_request" {
		t.Fatal(err)
	}
}

func TestMultiDocApplyReportsAppliedWithError(t *testing.T) {
	e := newEnv(newConn(true), nil)
	n := 0
	e.read.dyn.PrependReactor("patch", "*", func(a ktesting.Action) (bool, runtime.Object, error) {
		n++
		if n == 2 {
			return true, nil, apierrors.NewInvalid(schema.GroupKind{Kind: "ConfigMap"}, "b", nil)
		}
		return true, setMeta(mk("v1", "ConfigMap", "default", a.(ktesting.PatchAction).GetName(), nil), "resourceVersion", "1"), nil
	})
	res, err := e.s.Apply(context.Background(), protocol.ObjectApplyParams{RequestID: "m", Ref: protocol.ObjectRef{Cluster: "c1"}, YAML: cmYAML("a", "b", "c"), Mode: "create"})
	if code(t, err) != "invalid" {
		t.Fatal(err)
	}
	body := err.(*Error).Body
	if len(body.Applied) != 1 || body.Applied[0].Name != "a" || len(res.Applied) != 1 {
		t.Fatalf("%+v / %+v", body.Applied, res.Applied)
	}
	_ = time.Second
}

// A Secret edited in decoded form (values as stringData) must not report the moved keys as changed fields.
func TestConflictsFoldStringData(t *testing.T) {
	live := mk("v1", "Secret", "ns", "s", map[string]any{"data": map[string]any{"a": "eA==", "b": "eQ=="}})
	setMeta(live, "labels", map[string]any{"app": "web"})
	setMeta(live, "managedFields", []any{map[string]any{"manager": "kubectl-create", "operation": "Update", "fieldsV1": map[string]any{
		"f:data": map[string]any{"f:a": map[string]any{}, "f:b": map[string]any{}},
	}}})
	draft := func(f func(m map[string]any)) map[string]any {
		o := live.DeepCopy().Object
		delete(o, "data")
		o["stringData"] = map[string]any{"a": "x", "b": "y"} // eA== is "x", eQ== is "y"
		f(o)
		return o
	}
	paths := func(o map[string]any) []string {
		var out []string
		for _, c := range conflictsFor(live, foldStringData(o)) {
			out = append(out, c.Path)
		}
		return out
	}
	if got := paths(draft(func(m map[string]any) { _ = unstructured.SetNestedField(m, "other", "metadata", "labels", "app") })); len(got) != 0 {
		t.Fatalf("decoded draft that only changes a label conflicts: %v", got)
	}
	if got := paths(draft(func(m map[string]any) { m["stringData"].(map[string]any)["a"] = "changed" })); len(got) != 1 || got[0] != "data.a" {
		t.Fatalf("a changed value must conflict with its owner: %v", got)
	}
	// stringData wins over data on a collision, and the input is left alone
	in := map[string]any{"apiVersion": "v1", "kind": "Secret", "data": map[string]any{"a": "b2xk"}, "stringData": map[string]any{"a": "x"}}
	out := foldStringData(in)
	if out["data"].(map[string]any)["a"] != "eA==" || in["data"].(map[string]any)["a"] != "b2xk" || out["stringData"] != nil {
		t.Fatalf("fold: %v / %v", out, in)
	}
	// other kinds are never folded
	cm := map[string]any{"apiVersion": "v1", "kind": "ConfigMap", "stringData": map[string]any{"a": "x"}}
	if got := foldStringData(cm); got["stringData"] == nil || got["data"] != nil {
		t.Fatalf("folded a non-Secret: %v", got)
	}
}
