package wire

import (
	"testing"
	"time"

	"github.com/zyno-io/v8s/agent/internal/fake"
	"github.com/zyno-io/v8s/agent/pkg/protocol"
)

func colIdx(t *testing.T, cols []protocol.Column, key string) int {
	t.Helper()
	for i, c := range cols {
		if c.Key == key {
			return i + 2
		}
	}
	t.Fatalf("no %s column", key)
	return 0
}

// The Overview capacity card: a capacity view with usage opens a demand, the row arrives with the cluster totals, and
// turning usage off keeps the demand without bumping gen.
func TestCapacityViewOpensDemandAndServesTheRow(t *testing.T) {
	e := startEnv(t, fake.Config{Clusters: 1, Pods: 400}, nil)
	c, _, err := e.dial(t, nil)
	if err != nil {
		t.Fatal(err)
	}
	c.next(2 * time.Second) // hello
	var st protocol.ClusterStates
	decodeResult(t, c.call("clusters.select", protocol.ClustersSelectParams{IDs: []string{"prod-us"}}, nil), &st)
	c.waitReady(1)

	var sub protocol.ViewSubscribeResult
	q := protocol.ViewQuery{GVR: protocol.CapacityGVR, Clusters: []string{"prod-us"}}
	decodeResult(t, c.call("view.subscribe", protocol.ViewSubscribeParams{QID: 1, Gen: 1, Query: q, Window: protocol.Window{Limit: 1}, Usage: true}, nil), &sub)
	waitDemands(t, e, 1)
	nodes, cpuUse, cpuReq := colIdx(t, sub.Columns, "nodes"), colIdx(t, sub.Columns, "cpuUse"), colIdx(t, sub.Columns, "cpuReq")
	num := func(v any) int64 {
		switch x := v.(type) {
		case int8:
			return int64(x)
		case int16:
			return int64(x)
		case int32:
			return int64(x)
		case int64:
			return x
		case uint8:
			return int64(x)
		case uint16:
			return int64(x)
		case uint32:
			return int64(x)
		case uint64:
			return int64(x)
		}
		return -99
	}
	if !c.patchesUntil(t, 20*time.Second, func(vp protocol.ViewPatch) bool {
		for _, r := range vp.Rows {
			if num(r[nodes]) == 150 && num(r[cpuReq]) > 0 && num(r[cpuUse]) >= 0 {
				return true
			}
		}
		return false
	}) {
		t.Fatal("the capacity row with totals and usage never arrived")
	}

	// usage:false keeps the capacity demand (the row still carries requests and limits; node polling stops) and does not bump gen.
	no := false
	ok(t, c.call("view.update", protocol.ViewUpdateParams{QID: 1, Gen: 1, Usage: &no}, nil))
	time.Sleep(100 * time.Millisecond)
	if n := demands(e); n != 1 {
		t.Fatalf("capacity demands after usage:false = %d, want 1", n)
	}
	ok(t, c.call("view.unsubscribe", protocol.ViewUnsubscribeParams{QID: 1}, nil))
}

// A capacity view subscribed without usage still gets its row.
func TestCapacityViewWithoutUsageStillServesTheRow(t *testing.T) {
	e := startEnv(t, fake.Config{Clusters: 1, Pods: 400}, nil)
	c, _, err := e.dial(t, nil)
	if err != nil {
		t.Fatal(err)
	}
	c.next(2 * time.Second)
	var st protocol.ClusterStates
	decodeResult(t, c.call("clusters.select", protocol.ClustersSelectParams{IDs: []string{"prod-us"}}, nil), &st)
	c.waitReady(1)
	var sub protocol.ViewSubscribeResult
	q := protocol.ViewQuery{GVR: protocol.CapacityGVR, Clusters: []string{"prod-us"}}
	decodeResult(t, c.call("view.subscribe", protocol.ViewSubscribeParams{QID: 1, Gen: 1, Query: q, Window: protocol.Window{Limit: 1}}, nil), &sub)
	waitDemands(t, e, 1)
	nodes := colIdx(t, sub.Columns, "nodes")
	if !c.patchesUntil(t, 20*time.Second, func(vp protocol.ViewPatch) bool {
		for _, r := range vp.Rows {
			if r[nodes] != nil {
				return true
			}
		}
		return false
	}) {
		t.Fatal("no capacity row without usage")
	}
}
