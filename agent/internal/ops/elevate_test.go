package ops

import (
	"context"
	"testing"

	"github.com/zyno-io/v8s/agent/internal/kube"
	"github.com/zyno-io/v8s/agent/pkg/protocol"
	authorizationv1 "k8s.io/api/authorization/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ktesting "k8s.io/client-go/testing"
)

// elevated makes c1 run as c1-write, as the engine does: Conn("c1") is the write connection.
func elevated(e *env) *env {
	e.s.cfg.Elevated = func(id string) string {
		if id == "c1" {
			return "c1-write"
		}
		return ""
	}
	inner := e.s.cfg.Conn
	e.s.cfg.RawConn = inner
	e.s.cfg.Conn = func(id string) (kube.Conn, error) {
		if id == "c1" {
			return inner("c1-write")
		}
		return inner(id)
	}
	return e
}

func TestElevatedRouting(t *testing.T) {
	ctx := context.Background()
	t.Run("action runs on the write context without a read review", func(t *testing.T) {
		e := elevated(newEnv(newConn(false, deployment("ns", "web")), newConn(true, deployment("ns", "web"))))
		e.auto = false
		r, err := e.s.Action(ctx, scaleParams("1"))
		if err != nil || r.UsedContext != "c1-write" {
			t.Fatalf("%v %+v", err, r)
		}
		if e.read.az.calls.Load() != 0 || lastAction(e.read, "patch") != nil || lastAction(e.write, "patch") == nil {
			t.Fatal("read context touched")
		}
	})
	t.Run("access", func(t *testing.T) {
		w := newConn(true)
		e := elevated(newEnv(newConn(false), w))
		e.auto = false
		got := e.s.access(ctx, protocol.AccessCheck{Cluster: "c1", Verb: "patch", GVR: "apps/v1/deployments", Namespace: "ns"})
		if !got.Allowed || got.ViaWrite || got.NeedsConfirm {
			t.Fatalf("%+v", got)
		}
		if e.read.az.calls.Load() != 0 {
			t.Fatal("read review ran")
		}
		w.az.fn = func(*authorizationv1.ResourceAttributes) (bool, string, error) { return false, "nope", nil }
		e.advance(ssarTTL * 2)
		if got := e.s.access(ctx, protocol.AccessCheck{Cluster: "c1", Verb: "patch", GVR: "apps/v1/deployments", Namespace: "ns"}); got.Allowed || got.Reason != "nope" {
			t.Fatalf("denied: %+v", got)
		}
	})
	t.Run("write 401 is write_unavailable", func(t *testing.T) {
		w := newConn(true, deployment("ns", "web"))
		w.dyn.PrependReactor("patch", "deployments", func(ktesting.Action) (bool, runtime.Object, error) {
			return true, nil, apierrors.NewUnauthorized("expired")
		})
		e := elevated(newEnv(newConn(true, deployment("ns", "web")), w))
		_, err := e.s.Action(ctx, scaleParams("1"))
		if code(t, err) != "write_unavailable" {
			t.Fatal(err)
		}
	})
}

func TestCheckLink(t *testing.T) {
	ctx := context.Background()
	if err := newEnv(newConn(true), nil).s.CheckLink(ctx, "c1", ""); code(t, err) != "bad_request" {
		t.Fatalf("no link: %v", err)
	}
	ns := func(uid string) *fakeConn {
		c := newConn(true)
		c.dyn.PrependReactor("get", "namespaces", func(ktesting.Action) (bool, runtime.Object, error) {
			o := namespaceObj(uid)
			return true, o, nil
		})
		return c
	}
	if err := newEnv(ns("a"), ns("b")).s.CheckLink(ctx, "c1", "c1-write"); code(t, err) != "link_mismatch" {
		t.Fatalf("mismatch: %v", err)
	}
	if err := newEnv(ns("a"), ns("a")).s.CheckLink(ctx, "c1", "c1-write"); err != nil {
		t.Fatalf("match: %v", err)
	}
}

func namespaceObj(uid string) *unstructured.Unstructured {
	o := mk("v1", "Namespace", "", "kube-system", nil)
	o.SetUID(types.UID(uid))
	return o
}

func TestCheckLinkWhileElevatedComparesReadContext(t *testing.T) {
	ns := func(uid string) *fakeConn {
		c := newConn(true)
		c.dyn.PrependReactor("get", "namespaces", func(ktesting.Action) (bool, runtime.Object, error) { return true, namespaceObj(uid), nil })
		return c
	}
	e := elevated(newEnv(ns("a"), ns("b"))) // Conn("c1") now answers with the write connection
	if err := e.s.CheckLink(context.Background(), "c1", "c1-write"); code(t, err) != "link_mismatch" {
		t.Fatalf("elevated CheckLink compared the write context with itself: %v", err)
	}
}
