package wire

import (
	"strings"
	"testing"
	"time"

	"github.com/vmihailenco/msgpack/v5"
	"github.com/zyno-io/v8s/agent/internal/fake"
	"github.com/zyno-io/v8s/agent/internal/flux"
	"github.com/zyno-io/v8s/agent/pkg/protocol"
)

// waitTotal reads frames (acking patches) until the subscription's total reaches want.
func (c *client) waitTotal(qid uint32, want int) {
	c.t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		f, ok := c.next(200 * time.Millisecond)
		if !ok || f.kind != protocol.KindPatch {
			continue
		}
		c.ack(f.seq)
		var vp protocol.ViewPatch
		if err := msgpack.Unmarshal(f.payload, &vp); err != nil {
			c.t.Fatal(err)
		}
		if f.channel == qid && vp.Total == want {
			return
		}
	}
	c.t.Fatalf("subscription %d never reached total %d", qid, want)
}

func colKeys(cols []protocol.Column) string {
	var k []string
	for _, c := range cols {
		k = append(k, c.Key)
	}
	return strings.Join(k, ",")
}

func TestG1ViewsObjectsSettingsSearchOverWire(t *testing.T) {
	e := startEnv(t, fake.Config{Clusters: 2, Pods: 400, RBAC: true}, nil)
	c, _, err := e.dial(t, nil, protocol.Subprotocol, "token."+testToken)
	if err != nil {
		t.Fatal(err)
	}
	if h, ok := c.next(5 * time.Second); !ok || h.kind != protocol.KindHello {
		t.Fatal("no hello")
	}
	decodeResult(t, c.call("clusters.select", protocol.ClustersSelectParams{IDs: []string{"prod-us", "prod-eu"}}, nil), &protocol.ClusterStates{})
	c.waitReady(2)

	// curated view with columns from the module
	var sub protocol.ViewSubscribeResult
	decodeResult(t, c.call("view.subscribe", protocol.ViewSubscribeParams{QID: 1, Gen: 1, Query: protocol.ViewQuery{GVR: "apps/v1/deployments"}, Window: protocol.Window{Limit: 50}}, nil), &sub)
	if !strings.Contains(colKeys(sub.Columns), "upToDate") || sub.Columns[3].Label == "" {
		t.Fatalf("deployment columns: %s", colKeys(sub.Columns))
	}
	c.waitTotal(1, 808) // 400 apps + 4 Flux controllers per cluster

	// Flux kinds are curated mode B modules; their view spans both clusters
	decodeResult(t, c.call("view.subscribe", protocol.ViewSubscribeParams{QID: 2, Gen: 1,
		Query:  protocol.ViewQuery{GVR: "kustomize.toolkit.fluxcd.io/v1/kustomizations", Sort: []protocol.Sort{{Col: "statusTone"}}},
		Window: protocol.Window{Limit: 50}}, nil), &sub)
	if colKeys(sub.Columns) != "cluster,name,namespace,ready,suspended,revision,source,interval,lastReconcile,message,requestedAt,lastHandled,dependsOn,statusTone,createdAt" {
		t.Fatalf("generic columns: %s", colKeys(sub.Columns))
	}
	nks := 0 // the demo Flux objects of both clusters
	for _, cl := range []string{"prod-us", "prod-eu"} {
		for _, o := range flux.DemoObjects(cl, time.Now()) {
			if o.GetKind() == "Kustomization" {
				nks++
			}
		}
	}
	c.waitTotal(2, nks)
	if r := c.call("view.subscribe", protocol.ViewSubscribeParams{QID: 3, Gen: 1, Query: protocol.ViewQuery{GVR: "nope.io/v1/nothing"}}, nil); r.Error == nil || r.Error.Code != "not_found" {
		t.Fatalf("unserved kind: %+v", r.Error)
	}

	// object.get: Flux-managed deployment
	ref := protocol.ObjectRef{Cluster: "prod-us", GVR: "apps/v1/deployments", Namespace: "team-0", Name: "svc-0"}
	var obj protocol.ObjectPayload
	decodeResult(t, c.call("object.get", protocol.ObjectGetParams{Cluster: ref.Cluster, GVR: ref.GVR, Namespace: ref.Namespace, Name: ref.Name}, nil), &obj)
	if !strings.Contains(obj.YAML, "kind: Deployment") || strings.Contains(obj.YAML, "managedFields") || obj.GitOps == nil || obj.GitOps.Kind != "Kustomization" || len(obj.ManagedBy) == 0 {
		t.Fatalf("object.get: %+v", obj)
	}
	if r := c.call("object.get", protocol.ObjectGetParams{Cluster: "prod-us", GVR: ref.GVR, Namespace: "team-0", Name: "missing"}, nil); r.Error == nil || r.Error.Code != "not_found" {
		t.Fatalf("missing object: %+v", r.Error)
	}

	// object.subscribe pushes KindObject frames; a scale action shows up in the next one
	mustOK(t, c.call("object.subscribe", protocol.ObjectSubscribeParams{OID: 7, Ref: ref}, nil))
	var frames []protocol.ObjectFrame
	collect := func(f frame) {
		if f.kind == protocol.KindObject && f.channel == 7 {
			var of protocol.ObjectFrame
			if err := msgpack.Unmarshal(f.payload, &of); err != nil {
				t.Fatal(err)
			}
			frames = append(frames, of)
		}
		if f.kind == protocol.KindPatch {
			c.ack(f.seq)
		}
	}
	var mr protocol.MutationResult
	decodeResult(t, c.call("object.action", protocol.ObjectActionParams{RequestID: "r1", Ref: ref, Action: "scale", Params: map[string]any{"replicas": 7}}, collect), &mr)
	if mr.UsedContext != "prod-us" {
		t.Fatalf("used context: %+v", mr)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) && (len(frames) < 2 || !strings.Contains(frames[len(frames)-1].YAML, "replicas: 7")) {
		if f, ok := c.next(100 * time.Millisecond); ok {
			collect(f)
		}
	}
	if len(frames) < 2 || !strings.Contains(frames[len(frames)-1].YAML, "replicas: 7") {
		t.Fatalf("object frames: %d", len(frames))
	}
	// dedupe: the same requestId replays the stored result
	var mr2 protocol.MutationResult
	decodeResult(t, c.call("object.action", protocol.ObjectActionParams{RequestID: "r1", Ref: ref, Action: "scale", Params: map[string]any{"replicas": 9}}, collect), &mr2)
	var live protocol.ObjectPayload
	decodeResult(t, c.call("object.get", protocol.ObjectGetParams{Cluster: ref.Cluster, GVR: ref.GVR, Namespace: ref.Namespace, Name: ref.Name}, collect), &live)
	if mr2.ResourceVersion != mr.ResourceVersion || !strings.Contains(live.YAML, "replicas: 7") {
		t.Fatalf("requestId was not deduplicated: %+v", mr2)
	}
	mustOK(t, c.call("object.unsubscribe", protocol.ObjectUnsubscribeParams{OID: 7}, nil))

	// dry run, stale-resourceVersion apply (conflict), fresh apply bumps the version
	edited := strings.Replace(live.YAML, "replicas: 7", "replicas: 4", 1)
	var dr protocol.ObjectDryRunResult
	decodeResult(t, c.call("object.dryRun", protocol.ObjectDryRunParams{Ref: ref, YAML: edited, Mode: "update"}, nil), &dr)
	if !dr.OK {
		t.Fatalf("dry run: %+v", dr)
	}
	stale := protocol.ObjectApplyParams{RequestID: "a1", Ref: ref, YAML: edited, Mode: "update", ResourceVersion: "1"}
	if r := c.call("object.apply", stale, nil); r.Error == nil || r.Error.Code != "conflict" || r.Error.Live == nil {
		t.Fatalf("stale apply: %+v", r.Error)
	}
	var ar protocol.ObjectApplyResult
	decodeResult(t, c.call("object.apply", protocol.ObjectApplyParams{RequestID: "a2", Ref: ref, YAML: edited, Mode: "update", ResourceVersion: live.ResourceVersion}, nil), &ar)
	if ar.ResourceVersion == "" || ar.ResourceVersion == live.ResourceVersion {
		t.Fatalf("apply did not bump resourceVersion: %+v", ar)
	}

	// RBAC: prod-eu is read-only until its admin context is linked
	denied := protocol.ObjectActionParams{RequestID: "d1", Ref: protocol.ObjectRef{Cluster: "prod-eu", GVR: ref.GVR, Namespace: "team-0", Name: "svc-0"}, Action: "restart"}
	if r := c.call("object.action", denied, nil); r.Error == nil || r.Error.Code != "forbidden" {
		t.Fatalf("read-only cluster: %+v", r.Error)
	}
	var set protocol.Settings
	decodeResult(t, c.call("settings.set", protocol.SettingsSetParams{Patch: map[string]any{"links": map[string]any{"prod-eu": "prod-eu-admin"}, "stars": []any{"apps/v1/deployments"}}}, nil), &set)
	if set.Links["prod-eu"] != "prod-eu-admin" || !set.AutoSwitchWrite || len(set.Stars) != 1 {
		t.Fatalf("settings.set: %+v", set)
	}
	var chk protocol.AccessCheckResult
	decodeResult(t, c.call("access.check", protocol.AccessCheckParams{Checks: []protocol.AccessCheck{
		{Cluster: "prod-eu", Verb: "patch", GVR: ref.GVR, Namespace: "team-0"}, {Cluster: "prod-us", Verb: "patch", GVR: ref.GVR}}}, nil), &chk)
	if len(chk.Results) != 2 || !chk.Results[0].Allowed || !chk.Results[0].ViaWrite || !chk.Results[1].Allowed || chk.Results[1].ViaWrite {
		t.Fatalf("access.check: %+v", chk)
	}
	denied.RequestID = "d2"
	decodeResult(t, c.call("object.action", denied, nil), &mr)
	if mr.UsedContext != "prod-eu-admin" {
		t.Fatalf("auto-switch to the write context: %+v", mr)
	}
	var lc protocol.ClustersListResult
	decodeResult(t, c.call("clusters.list", nil, nil), &lc)
	for _, cl := range lc.Clusters {
		if cl.ID == "prod-eu-admin" {
			t.Fatal("linked write context listed as a cluster")
		}
	}

	// search
	var sr protocol.SearchResult
	decodeResult(t, c.call("search", protocol.SearchParams{Text: "SVC-12", Limit: 10}, nil), &sr)
	if len(sr.Results) == 0 || len(sr.Results) > 10 || !strings.HasPrefix(sr.Results[0].Name, "svc-12") {
		t.Fatalf("search: %+v", sr)
	}
	var back protocol.Settings
	decodeResult(t, c.call("settings.get", nil, nil), &back)
	if len(back.Stars) != 1 {
		t.Fatalf("settings.get: %+v", back)
	}
}

func mustOK(t *testing.T, r protocol.Response) {
	t.Helper()
	if r.Error != nil {
		t.Fatalf("error response: %+v", r.Error)
	}
}
