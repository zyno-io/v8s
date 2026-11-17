package ops

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/zyno-io/v8s/agent/pkg/protocol"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ktesting "k8s.io/client-go/testing"
)

const (
	ksGVR  = "kustomize.toolkit.fluxcd.io/v1/kustomizations"
	hrGVR  = "helm.toolkit.fluxcd.io/v2/helmreleases"
	gitGVR = "source.toolkit.fluxcd.io/v1/gitrepositories"
)

func ks(name string) *unstructured.Unstructured {
	return mk("kustomize.toolkit.fluxcd.io/v1", "Kustomization", "ns", name, map[string]any{"spec": map[string]any{"sourceRef": map[string]any{"kind": "GitRepository", "name": "repo"}}})
}

func hr(name string) *unstructured.Unstructured {
	return mk("helm.toolkit.fluxcd.io/v2", "HelmRelease", "ns", name, map[string]any{"spec": map[string]any{"interval": "1m"}})
}

func repo() *unstructured.Unstructured {
	return mk("source.toolkit.fluxcd.io/v1", "GitRepository", "ns", "repo", nil)
}

func fluxEnv(read, write *fakeConn) *env {
	e := newEnv(read, write)
	prev := e.s.cfg.Resolve
	e.s.cfg.Resolve = func(c, av, kind string) (string, bool, error) {
		if kind == "GitRepository" && av == "source.toolkit.fluxcd.io/v1" {
			return gitGVR, true, nil
		}
		return prev(c, av, kind)
	}
	return e
}

func fluxAct(e *env, id, gvr, name, action string) (protocol.MutationResult, error) {
	return e.s.Action(context.Background(), protocol.ObjectActionParams{RequestID: id, Ref: protocol.ObjectRef{Cluster: "c1", GVR: gvr, Namespace: "ns", Name: name}, Action: action})
}

func TestFluxActions(t *testing.T) {
	e := fluxEnv(newConn(true, ks("k"), hr("h"), repo()), nil)
	res, err := fluxAct(e, "1", ksGVR, "k", "flux.suspend")
	if err != nil || res.UsedContext != "c1" || res.Token != "" {
		t.Fatalf("%v %+v", err, res)
	}
	if pt, body := patchOf(t, e.read); pt != types.MergePatchType || body != `{"spec":{"suspend":true}}` {
		t.Fatal(pt, body)
	}
	if a := lastAction(e.read, "patch").(ktesting.PatchAction); a.GetName() != "k" {
		t.Fatal("name")
	}
	if _, err := fluxAct(e, "2", ksGVR, "k", "flux.resume"); err != nil {
		t.Fatal(err)
	}
	if _, body := patchOf(t, e.read); body != `{"spec":{"suspend":false}}` {
		t.Fatal(body)
	}

	res, err = fluxAct(e, "3", ksGVR, "k", "flux.reconcile")
	if err != nil || res.Token != "2026-01-02T03:04:05Z" {
		t.Fatalf("%v %+v", err, res)
	}
	if _, body := patchOf(t, e.read); body != `{"metadata":{"annotations":{"reconcile.fluxcd.io/requestedAt":"`+res.Token+`"}}}` {
		t.Fatal(body)
	}

	for _, c := range []struct{ action, key string }{{"flux.force", "forceAt"}, {"flux.reset", "resetAt"}} {
		res, err = fluxAct(e, c.action, hrGVR, "h", c.action)
		if err != nil || res.Token == "" {
			t.Fatalf("%v %+v", err, res)
		}
		_, body := patchOf(t, e.read)
		var m struct {
			Metadata struct{ Annotations map[string]string }
		}
		if json.Unmarshal([]byte(body), &m) != nil || len(m.Metadata.Annotations) != 2 ||
			m.Metadata.Annotations["reconcile.fluxcd.io/"+c.key] != res.Token || m.Metadata.Annotations["reconcile.fluxcd.io/requestedAt"] != res.Token {
			t.Fatalf("%s: %s", c.action, body)
		}
	}
}

func TestFluxBadRequests(t *testing.T) {
	e := fluxEnv(newConn(true, ks("k"), hr("h"), deployment("ns", "web")), nil)
	cases := []struct{ id, gvr, name, action string }{
		{"a", ksGVR, "k", "flux.explode"},
		{"b", ksGVR, "k", "flux.force"},
		{"c", ksGVR, "k", "flux.reset"},
		{"d", "apps/v1/deployments", "web", "flux.reconcile"},
		{"e", gitGVR, "repo", "flux.reconcileWithSource"},
		{"f", "v1/pods", "p", "flux.suspend"},
	}
	for _, c := range cases {
		if _, err := fluxAct(e, c.id, c.gvr, c.name, c.action); code(t, err) != "bad_request" {
			t.Fatalf("%v: %v", c, err)
		}
	}
	if lastAction(e.read, "patch") != nil {
		t.Fatal("nothing should be patched")
	}
}

func TestFluxRoutingWriteContext(t *testing.T) {
	e := fluxEnv(newConn(false, ks("k")), newConn(true, ks("k")))
	res, err := fluxAct(e, "1", ksGVR, "k", "flux.suspend")
	if err != nil || res.UsedContext != "c1-write" {
		t.Fatalf("%v %+v", err, res)
	}
	if lastAction(e.write, "patch") == nil || lastAction(e.read, "patch") != nil {
		t.Fatal("wrong context")
	}
	e2 := fluxEnv(newConn(false, ks("k")), nil)
	if _, err := fluxAct(e2, "1", ksGVR, "k", "flux.reconcile"); code(t, err) != "forbidden" {
		t.Fatal(err)
	}
}

func TestFluxDedupe(t *testing.T) {
	e := fluxEnv(newConn(true, ks("k")), nil)
	a, _ := fluxAct(e, "same", ksGVR, "k", "flux.reconcile")
	e.advance(time.Second)
	b, _ := fluxAct(e, "same", ksGVR, "k", "flux.reconcile")
	if a != b || countVerb(e.read, "patch") != 1 {
		t.Fatalf("%+v %+v", a, b)
	}
}

func countVerb(c *fakeConn, verb string) int {
	n := 0
	for _, a := range c.dyn.Actions() {
		if a.GetVerb() == verb {
			n++
		}
	}
	return n
}

func TestFluxReconcileWithSourceOrdering(t *testing.T) {
	old1, old2 := fluxPollInterval, fluxPollTimeout
	fluxPollInterval, fluxPollTimeout = 5*time.Millisecond, 5*time.Second
	defer func() { fluxPollInterval, fluxPollTimeout = old1, old2 }()

	c := newConn(true, ks("k"), repo())
	var mu sync.Mutex
	var order []string
	var token string
	gets := 0
	c.dyn.PrependReactor("patch", "*", func(a ktesting.Action) (bool, runtime.Object, error) {
		mu.Lock()
		defer mu.Unlock()
		pa := a.(ktesting.PatchAction)
		order = append(order, "patch:"+pa.GetResource().Resource)
		if pa.GetResource().Resource == "gitrepositories" {
			var m struct {
				Metadata struct{ Annotations map[string]string }
			}
			_ = json.Unmarshal(pa.GetPatch(), &m)
			token = m.Metadata.Annotations["reconcile.fluxcd.io/requestedAt"]
		}
		return false, nil, nil
	})
	c.dyn.PrependReactor("get", "gitrepositories", func(a ktesting.Action) (bool, runtime.Object, error) {
		mu.Lock()
		defer mu.Unlock()
		order = append(order, "get:gitrepositories")
		gets++
		h := "old"
		if gets >= 3 {
			h = token
		}
		o := repo()
		_ = unstructured.SetNestedField(o.Object, h, "status", "lastHandledReconcileAt")
		return true, o, nil
	})
	e := fluxEnv(c, nil)
	res, err := fluxAct(e, "1", ksGVR, "k", "flux.reconcileWithSource")
	if err != nil || res.Token == "" {
		t.Fatalf("%v %+v", err, res)
	}
	mu.Lock()
	defer mu.Unlock()
	// ks Get (read of the object) is not recorded by the gitrepositories reactors; ordering of the rest:
	want := "patch:gitrepositories,get:gitrepositories,get:gitrepositories,get:gitrepositories,patch:kustomizations"
	if got := strings.Join(order, ","); got != want {
		t.Fatalf("order %s", got)
	}
}

func TestFluxReconcileWithSourceTimeout(t *testing.T) {
	old1, old2 := fluxPollInterval, fluxPollTimeout
	fluxPollInterval, fluxPollTimeout = 5*time.Millisecond, 60*time.Millisecond
	defer func() { fluxPollInterval, fluxPollTimeout = old1, old2 }()

	e := fluxEnv(newConn(true, ks("k"), repo()), nil) // source never handles the token
	_, err := fluxAct(e, "1", ksGVR, "k", "flux.reconcileWithSource")
	if code(t, err) != "unavailable" || !strings.Contains(err.Error(), "did not finish") {
		t.Fatal(err)
	}
	if countVerb(e.read, "patch") != 1 {
		t.Fatal("object must not be reconciled after a source timeout")
	}
}

func TestFluxReconcileWithoutSourceRef(t *testing.T) {
	e := fluxEnv(newConn(true, mk("kustomize.toolkit.fluxcd.io/v1", "Kustomization", "ns", "k", nil)), nil)
	if _, err := fluxAct(e, "1", ksGVR, "k", "flux.reconcileWithSource"); code(t, err) != "bad_request" {
		t.Fatal(err)
	}
}
