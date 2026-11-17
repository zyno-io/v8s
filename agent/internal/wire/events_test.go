package wire

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/vmihailenco/msgpack/v5"
	"github.com/zyno-io/v8s/agent/internal/fake"
	"github.com/zyno-io/v8s/agent/internal/ingest"
	"github.com/zyno-io/v8s/agent/pkg/protocol"
)

func involvedQuery(gvr, ns, name string) protocol.ViewQuery {
	return protocol.ViewQuery{GVR: ingest.EventsGVR, Sort: []protocol.Sort{{Col: "lastSeen", Desc: true}},
		Involved: &protocol.ObjectRef{Cluster: "prod-us", GVR: gvr, Namespace: ns, Name: name}}
}

func eventsEnv(t *testing.T) (*env, *client) {
	t.Helper()
	e := startEnv(t, fake.Config{Clusters: 1, Pods: 3000}, nil)
	c := connect(t, e)
	decodeResult(t, c.call("clusters.select", protocol.ClustersSelectParams{IDs: []string{"prod-us"}}, nil), &protocol.ClusterStates{})
	c.waitReady(1)
	return e, c
}

// eventView follows one events subscription: rows by id with their cells rendered as strings.
type eventView struct {
	c    *client
	cols []protocol.Column
	rows map[uint32]map[string]string
}

func (v *eventView) cell(raw any, col protocol.Column) string {
	if col.Type == protocol.ColDict {
		return v.c.dict[toU32(raw)]
	}
	return fmt.Sprint(raw)
}

// pump reads frames until cond holds for the rows seen so far.
func (v *eventView) pump(t *testing.T, gen uint32, what string, cond func(rows []map[string]string) bool) {
	t.Helper()
	for deadline := time.Now().Add(15 * time.Second); time.Now().Before(deadline); {
		f, ok := v.c.next(200 * time.Millisecond)
		if ok && f.kind == protocol.KindPatch {
			v.c.ack(f.seq)
			var vp protocol.ViewPatch
			if err := msgpack.Unmarshal(f.payload, &vp); err != nil {
				t.Fatal(err)
			}
			if vp.Gen != gen {
				continue
			}
			for _, id := range vp.Removed {
				delete(v.rows, id)
			}
			for _, r := range vp.Rows {
				m := map[string]string{}
				for i, col := range v.cols {
					m[col.Key] = v.cell(r[2+i], col)
				}
				v.rows[toU32(r[0])] = m
			}
		}
		var cur []map[string]string
		for _, r := range v.rows {
			cur = append(cur, r)
		}
		if cond(cur) {
			return
		}
	}
	t.Fatalf("timed out: %s (rows %v)", what, v.rows)
}

func subscribeEvents(t *testing.T, c *client, qid, gen uint32, q protocol.ViewQuery) *eventView {
	t.Helper()
	var sub protocol.ViewSubscribeResult
	decodeResult(t, c.call("view.subscribe", protocol.ViewSubscribeParams{QID: qid, Gen: gen, Query: q, Window: protocol.Window{Limit: protocol.MaxObjectEvents}}, nil), &sub)
	return &eventView{c: c, cols: sub.Columns, rows: map[uint32]map[string]string{}}
}

func hasRow(rows []map[string]string, typ, reason string) bool {
	for _, r := range rows {
		if r["type"] == typ && r["reason"] == reason {
			return true
		}
	}
	return false
}

func objectEventWatches(e *env) (refs []int) {
	for _, w := range e.mgr.WatchInfo("prod-us") {
		if strings.HasPrefix(w.GVR, ingest.ObjectEventsGVR) {
			refs = append(refs, w.Refs)
		}
	}
	return refs
}

func TestInvolvedEventsViewLiveAndLingers(t *testing.T) {
	e, c := eventsEnv(t)
	v := subscribeEvents(t, c, 1, 1, involvedQuery("apps/v1/deployments", "team-0", "svc-0"))
	var keys []string
	for _, col := range v.cols {
		keys = append(keys, col.Key)
	}
	if !strings.Contains(strings.Join(keys, ","), "source") || keys[len(keys)-3] != "source" {
		t.Fatalf("columns %v", keys)
	}
	v.pump(t, 1, "the generated Normal event", func(rows []map[string]string) bool {
		return hasRow(rows, "Normal", "ScalingReplicaSet")
	})
	for _, r := range v.rows {
		if r["object"] != "Deployment/svc-0" || r["source"] != "deployment-controller" {
			t.Fatalf("row of another object or without a source: %v", r)
		}
	}
	// a scale through object.action lands as a new row on the open view
	res := c.call("object.action", protocol.ObjectActionParams{RequestID: "scale-1",
		Ref: protocol.ObjectRef{Cluster: "prod-us", GVR: "apps/v1/deployments", Namespace: "team-0", Name: "svc-0"}, Action: "scale", Params: map[string]any{"replicas": 11}}, nil)
	if res.Error != nil {
		t.Fatalf("scale: %+v", res.Error)
	}
	v.pump(t, 1, "the live scaling event", func(rows []map[string]string) bool {
		for _, r := range rows {
			if strings.HasSuffix(r["message"], "to 11") {
				return true
			}
		}
		return false
	})
	if refs := objectEventWatches(e); len(refs) != 1 || refs[0] != 1 {
		t.Fatalf("watches while open: %v", refs)
	}
	if r := c.call("view.unsubscribe", protocol.ViewUnsubscribeParams{QID: 1}, nil); r.Error != nil {
		t.Fatalf("unsubscribe: %+v", r.Error)
	}
	if refs := objectEventWatches(e); len(refs) != 1 || refs[0] != 0 {
		t.Fatalf("the watch should linger with no references: %v", refs)
	}
}

func TestInvolvedEventsRejections(t *testing.T) {
	_, c := eventsEnv(t)
	q := involvedQuery(ingest.PodsGVR, "team-0", "p")
	q.GVR = ingest.PodsGVR // involved only makes sense on the events view
	if r := c.call("view.subscribe", protocol.ViewSubscribeParams{QID: 1, Gen: 1, Query: q, Window: protocol.Window{Limit: 10}}, nil); r.Error == nil || r.Error.Code != "bad_request" {
		t.Fatalf("involved with gvr v1/pods: %+v", r.Error)
	}
	if r := c.call("view.subscribe", protocol.ViewSubscribeParams{QID: 2, Gen: 1, Query: protocol.ViewQuery{GVR: ingest.ObjectEventsGVR}, Window: protocol.Window{Limit: 10}}, nil); r.Error == nil || r.Error.Code != "bad_request" {
		t.Fatalf("direct object-events gvr: %+v", r.Error)
	}
	bad := involvedQuery("apps/v1/deployments", "team-0", "svc-0")
	bad.Involved.Cluster = "not-selected"
	if r := c.call("view.subscribe", protocol.ViewSubscribeParams{QID: 3, Gen: 1, Query: bad, Window: protocol.Window{Limit: 10}}, nil); r.Error == nil || r.Error.Code != "not_found" {
		t.Fatalf("unselected cluster: %+v", r.Error)
	}
	// A malformed query is refused at once, before any watch starts for it.
	unk := involvedQuery("apps/v1/deployments", "team-0", "svc-0")
	unk.Sort = []protocol.Sort{{Col: "no-such-column"}}
	start := time.Now()
	if r := c.call("view.subscribe", protocol.ViewSubscribeParams{QID: 4, Gen: 1, Query: unk, Window: protocol.Window{Limit: 10}}, nil); r.Error == nil || r.Error.Code != "bad_request" {
		t.Fatalf("unknown sort column: %+v", r.Error)
	}
	if d := time.Since(start); d > time.Second {
		t.Fatalf("malformed involved query took %v (a watch was started first)", d)
	}
}

func TestInvolvedEventsViewUpdateReResolves(t *testing.T) {
	e, c := eventsEnv(t)
	v := subscribeEvents(t, c, 1, 1, involvedQuery("apps/v1/deployments", "team-0", "svc-0"))
	v.pump(t, 1, "svc-0 events", func(rows []map[string]string) bool { return hasRow(rows, "Normal", "ScalingReplicaSet") })
	next := involvedQuery("apps/v1/deployments", "team-1", "svc-1")
	if r := c.call("view.update", protocol.ViewUpdateParams{QID: 1, Gen: 2, Query: &next}, nil); r.Error != nil {
		t.Fatalf("update: %+v", r.Error)
	}
	v.rows = map[uint32]map[string]string{}
	v.pump(t, 2, "svc-1 events", func(rows []map[string]string) bool {
		return hasRow(rows, "Normal", "ScalingReplicaSet") && len(rows) == 1 && rows[0]["object"] == "Deployment/svc-1"
	})
	if refs := objectEventWatches(e); len(refs) != 2 {
		t.Fatalf("both watches should exist (one lingering): %v", refs)
	}
	// a plain view.update (sort, same object) keeps working through the rewrite
	same := involvedQuery("apps/v1/deployments", "team-1", "svc-1")
	same.Sort = []protocol.Sort{{Col: "lastSeen", Desc: false}}
	if r := c.call("view.update", protocol.ViewUpdateParams{QID: 1, Gen: 3, Query: &same}, nil); r.Error != nil {
		t.Fatalf("same-object update: %+v", r.Error)
	}
	v.pump(t, 3, "still svc-1 only", func(rows []map[string]string) bool { return len(rows) >= 1 })
	for _, r := range v.rows {
		if r["object"] != "Deployment/svc-1" {
			t.Fatalf("row of another object after update: %v", r)
		}
	}
}
