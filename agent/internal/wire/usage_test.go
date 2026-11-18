package wire

import (
	"testing"
	"time"

	"github.com/vmihailenco/msgpack/v5"
	"github.com/zyno-io/v8s/agent/internal/fake"
	"github.com/zyno-io/v8s/agent/pkg/protocol"
)

func ok(t *testing.T, r protocol.Response) {
	t.Helper()
	if r.Error != nil {
		t.Fatalf("error response: %+v", r.Error)
	}
}

func demands(e *env) int { return e.mgr.UsageDemands() }

func waitDemands(t *testing.T, e *env, want int) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if demands(e) == want {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("usage demands = %d, want %d", demands(e), want)
}

// cpuUseCell finds the index of the cpuUse cell in a row.
func cpuUseIdx(t *testing.T, cols []protocol.Column) int {
	t.Helper()
	for i, c := range cols {
		if c.Key == "cpuUse" {
			return i + 2
		}
	}
	t.Fatal("no cpuUse column")
	return 0
}

// patches reads view patches until pred says stop; it acks each one.
func (c *client) patchesUntil(t *testing.T, d time.Duration, pred func(vp protocol.ViewPatch) bool) bool {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		f, ok := c.next(300 * time.Millisecond)
		if !ok || f.kind != protocol.KindPatch {
			continue
		}
		var vp protocol.ViewPatch
		if err := msgpack.Unmarshal(f.payload, &vp); err != nil {
			t.Fatal(err)
		}
		c.ack(f.seq)
		if pred(vp) {
			return true
		}
	}
	return false
}

func TestUsageFlagOpensAndReleasesDemand(t *testing.T) {
	e := startEnv(t, fake.Config{Clusters: 1, Pods: 400}, nil)
	c, _, err := e.dial(t, nil)
	if err != nil {
		t.Fatal(err)
	}
	c.next(2 * time.Second) // hello
	var st protocol.ClusterStates
	decodeResult(t, c.call("clusters.select", protocol.ClustersSelectParams{IDs: []string{"prod-us"}}, nil), &st)
	c.waitReady(1)

	// A subscription without the flag holds no demand and its usage cells are nil.
	var plain protocol.ViewSubscribeResult
	decodeResult(t, c.call("view.subscribe", protocol.ViewSubscribeParams{QID: 1, Gen: 1, Window: protocol.Window{Limit: 50}}, nil), &plain)
	idx := cpuUseIdx(t, plain.Columns)
	if demands(e) != 0 {
		t.Fatalf("demands = %d without the usage flag", demands(e))
	}
	sawNil := c.patchesUntil(t, 5*time.Second, func(vp protocol.ViewPatch) bool {
		for _, r := range vp.Rows {
			if r[idx] != nil {
				t.Fatalf("cpuUse = %v without the usage flag", r[idx])
			}
		}
		return len(vp.Rows) > 0
	})
	if !sawNil {
		t.Fatal("no rows")
	}

	// usage: true on a second subscription opens a demand, and rows with values arrive.
	var sub protocol.ViewSubscribeResult
	decodeResult(t, c.call("view.subscribe", protocol.ViewSubscribeParams{QID: 2, Gen: 1, Window: protocol.Window{Limit: 50}, Usage: true}, nil), &sub)
	waitDemands(t, e, 1)
	var gen2 uint32
	if !c.patchesUntil(t, 15*time.Second, func(vp protocol.ViewPatch) bool {
		gen2 = vp.Gen
		for _, r := range vp.Rows {
			if r[idx] != nil {
				return true
			}
		}
		return false
	}) {
		t.Fatal("no row ever carried a cpuUse value")
	}

	// A usage-only update releases the demand and keeps gen (it is not a query change).
	off := false
	ok(t, c.call("view.update", protocol.ViewUpdateParams{QID: 2, Gen: 1, Usage: &off}, nil))
	waitDemands(t, e, 0)
	if !c.patchesUntil(t, 5*time.Second, func(vp protocol.ViewPatch) bool {
		if vp.Gen != gen2 {
			t.Fatalf("a usage-only update bumped gen: %d -> %d", gen2, vp.Gen)
		}
		return len(vp.Rows) > 0 && vp.Rows[0][idx] == nil // the rows are re-sent without values
	}) {
		t.Log("(no re-sent rows observed before the timeout; the demand release is what is asserted)")
	}
	on := true
	ok(t, c.call("view.update", protocol.ViewUpdateParams{QID: 2, Gen: 1, Usage: &on}, nil))
	waitDemands(t, e, 1)

	// A namespace change re-targets the demand: still exactly one is open (the new one replaced the old).
	q := protocol.ViewQuery{GVR: "v1/pods", Namespaces: []string{"team-1"}}
	ok(t, c.call("view.update", protocol.ViewUpdateParams{QID: 2, Gen: 2, Query: &q}, nil))
	time.Sleep(100 * time.Millisecond)
	if n := demands(e); n != 1 {
		t.Fatalf("demands after a namespace change = %d, want 1", n)
	}

	// Unsubscribing releases it.
	ok(t, c.call("view.unsubscribe", protocol.ViewUnsubscribeParams{QID: 2}, nil))
	waitDemands(t, e, 0)

	// A demand still open when the connection ends is released by the cleanup.
	decodeResult(t, c.call("view.subscribe", protocol.ViewSubscribeParams{QID: 3, Gen: 1, Window: protocol.Window{Limit: 10}, Usage: true}, nil), &sub)
	waitDemands(t, e, 1)
	c.ws.CloseNow()
	waitDemands(t, e, 0)
}

func TestUsageFlagOnAKindWithoutUsageHoldsNoDemand(t *testing.T) {
	e := startEnv(t, fake.Config{Clusters: 1, Pods: 100}, nil)
	c, _, err := e.dial(t, nil)
	if err != nil {
		t.Fatal(err)
	}
	c.next(2 * time.Second)
	var st protocol.ClusterStates
	decodeResult(t, c.call("clusters.select", protocol.ClustersSelectParams{IDs: []string{"prod-us"}}, nil), &st)
	c.waitReady(1)
	var sub protocol.ViewSubscribeResult
	decodeResult(t, c.call("view.subscribe", protocol.ViewSubscribeParams{QID: 1, Gen: 1,
		Query: protocol.ViewQuery{GVR: "apps/v1/deployments"}, Window: protocol.Window{Limit: 10}, Usage: true}, nil), &sub)
	time.Sleep(100 * time.Millisecond)
	if n := demands(e); n != 0 {
		t.Fatalf("a deployments view opened %d usage demands", n)
	}
}
