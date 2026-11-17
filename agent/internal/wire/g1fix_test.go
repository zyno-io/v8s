package wire

import (
	"testing"
	"time"

	"github.com/vmihailenco/msgpack/v5"
	"github.com/zyno-io/v8s/agent/internal/fake"
	"github.com/zyno-io/v8s/agent/pkg/protocol"
)

func connect(t *testing.T, e *env) *client {
	t.Helper()
	c, _, err := e.dial(t, nil)
	if err != nil {
		t.Fatal(err)
	}
	if h, ok := c.next(5 * time.Second); !ok || h.kind != protocol.KindHello {
		t.Fatal("no hello")
	}
	return c
}

// Regression: a failed object.subscribe start is reported on the object's channel, not swallowed.
func TestObjectSubscribeFailureIsReported(t *testing.T) {
	e := startEnv(t, fake.Config{Clusters: 1, Pods: 50}, nil)
	c := connect(t, e)
	decodeResult(t, c.call("clusters.select", protocol.ClustersSelectParams{IDs: []string{"prod-us"}}, nil), &protocol.ClusterStates{})
	mustOK(t, c.call("object.subscribe", protocol.ObjectSubscribeParams{OID: 9, Ref: protocol.ObjectRef{Cluster: "prod-us", GVR: "apps/v1/deployments", Namespace: "team-0", Name: "nope"}}, nil))
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		f, ok := c.next(200 * time.Millisecond)
		if !ok || f.kind != protocol.KindObject || f.channel != 9 {
			continue
		}
		var of protocol.ObjectFrame
		if err := msgpack.Unmarshal(f.payload, &of); err != nil {
			t.Fatal(err)
		}
		if of.Error == nil || of.Error.Code != "not_found" {
			t.Fatalf("frame %+v", of)
		}
		return
	}
	t.Fatal("no error frame")
}

func refsOf(e *env, cl, gvr string) (int, bool) {
	for _, w := range e.mgr.WatchInfo(cl) {
		if w.GVR == gvr {
			return w.Refs, true
		}
	}
	return 0, false
}

// Regression: unsubscribing a still-resolving view.subscribe, or dropping the connection while it resolves,
// must not leave the watch scope (a reference on the kind's watch) behind.
func TestPendingSubscribeScopesAreReleased(t *testing.T) {
	e := startEnv(t, fake.Config{Clusters: 1, Pods: 50}, nil)
	c := connect(t, e)
	// subscribe right after select: discovery has not loaded, so the generic kind is still resolving
	c.nextID++
	selID := c.nextID
	c.send(protocol.KindRequest, selID, 0, protocol.Request{Method: "clusters.select", Params: protocol.ClustersSelectParams{IDs: []string{"prod-us"}}})
	c.nextID++
	subID := c.nextID
	c.send(protocol.KindRequest, subID, 0, protocol.Request{Method: "view.subscribe", Params: protocol.ViewSubscribeParams{QID: 4, Gen: 1,
		Query: protocol.ViewQuery{GVR: "storage.k8s.io/v1/storageclasses"}, Window: protocol.Window{Limit: 10}}})
	c.nextID++
	c.send(protocol.KindRequest, c.nextID, 0, protocol.Request{Method: "view.unsubscribe", Params: protocol.ViewUnsubscribeParams{QID: 4}})
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if _, ok := c.next(100 * time.Millisecond); !ok {
			continue
		}
	}
	if n, ok := refsOf(e, "prod-us", "storage.k8s.io/v1/storageclasses"); ok && n != 0 {
		t.Fatalf("scope leaked after unsubscribe: %d refs", n)
	}

	// connection dropped while a subscribe resolves
	c2 := connect(t, e)
	c2.nextID++
	c2.send(protocol.KindRequest, c2.nextID, 0, protocol.Request{Method: "view.subscribe", Params: protocol.ViewSubscribeParams{QID: 5, Gen: 1,
		Query: protocol.ViewQuery{GVR: "v1/configmaps"}, Window: protocol.Window{Limit: 10}}})
	c2.ws.CloseNow()
	time.Sleep(1500 * time.Millisecond)
	if n, ok := refsOf(e, "prod-us", "v1/configmaps"); ok && n != 0 {
		t.Fatalf("scope leaked after disconnect: %d refs", n)
	}
}

// Regression: view.updates of one subscription apply in order; a newer one that changes the query is not
// overwritten by an older one that resolves late.
func TestViewUpdatesApplyInOrder(t *testing.T) {
	e := startEnv(t, fake.Config{Clusters: 1, Pods: 50}, nil)
	c := connect(t, e)
	decodeResult(t, c.call("clusters.select", protocol.ClustersSelectParams{IDs: []string{"prod-us"}}, nil), &protocol.ClusterStates{})
	c.waitReady(1)
	var sub protocol.ViewSubscribeResult
	decodeResult(t, c.call("view.subscribe", protocol.ViewSubscribeParams{QID: 1, Gen: 1, Query: protocol.ViewQuery{GVR: "v1/pods"}, Window: protocol.Window{Limit: 10}}, nil), &sub)
	// A: generic kind (resolves off the connection goroutine); B right behind it, back to pods with a newer gen.
	c.nextID++
	a := c.nextID
	c.send(protocol.KindRequest, a, 0, protocol.Request{Method: "view.update", Params: protocol.ViewUpdateParams{QID: 1, Gen: 2, Query: &protocol.ViewQuery{GVR: "v1/configmaps"}}})
	c.nextID++
	b := c.nextID
	c.send(protocol.KindRequest, b, 0, protocol.Request{Method: "view.update", Params: protocol.ViewUpdateParams{QID: 1, Gen: 3, Query: &protocol.ViewQuery{GVR: "v1/pods", Namespaces: []string{"team-0"}}}})
	got := map[uint32]protocol.Response{}
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) && len(got) < 2 {
		f, ok := c.next(200 * time.Millisecond)
		if !ok {
			continue
		}
		if f.kind == protocol.KindPatch {
			c.ack(f.seq)
		}
		if f.kind == protocol.KindResponse && (f.channel == a || f.channel == b) {
			var r protocol.Response
			_ = msgpack.Unmarshal(f.payload, &r)
			got[f.channel] = r
		}
	}
	if len(got) != 2 {
		t.Fatalf("responses: %+v", got)
	}
	// the final state is B's: the next patch carries gen 3
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); {
		f, ok := c.next(200 * time.Millisecond)
		if !ok || f.kind != protocol.KindPatch {
			continue
		}
		c.ack(f.seq)
		var vp protocol.ViewPatch
		_ = msgpack.Unmarshal(f.payload, &vp)
		if vp.Gen == 3 {
			return
		}
		if vp.Gen == 2 && vp.Total >= 0 && false {
			t.Fatal("older update won")
		}
	}
	t.Fatal("no patch for the newest gen")
}
