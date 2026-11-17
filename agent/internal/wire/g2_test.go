package wire

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/vmihailenco/msgpack/v5"
	"github.com/zyno-io/v8s/agent/internal/cluster"
	"github.com/zyno-io/v8s/agent/internal/drainer"
	"github.com/zyno-io/v8s/agent/internal/fake"
	"github.com/zyno-io/v8s/agent/internal/flux"
	"github.com/zyno-io/v8s/agent/internal/helm"
	"github.com/zyno-io/v8s/agent/internal/ingest"
	"github.com/zyno-io/v8s/agent/internal/jobs"
	"github.com/zyno-io/v8s/agent/internal/kube"
	"github.com/zyno-io/v8s/agent/internal/ops"
	"github.com/zyno-io/v8s/agent/internal/settings"
	"github.com/zyno-io/v8s/agent/internal/streams"
	"github.com/zyno-io/v8s/agent/pkg/protocol"
	"helm.sh/helm/v4/pkg/action"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

// g2Services builds the G2 services the way cmd/v8s does, for a fake source.
func g2Services(ctx context.Context, opt *Options, src *fake.Source, mgr *cluster.Manager, svc *ops.Service, set *settings.Store) {
	connFn := func(id string) (kube.Conn, error) { return src.Backend(id) }
	opt.Jobs = jobs.New(ctx, nil)
	opt.Streams = streams.New(streams.Config{Router: svc, Conn: connFn, Settings: set.Get, AllowPortForward: true, User: "test"})
	opt.Drain = drainer.New(drainer.Config{Router: svc, Conn: connFn, EvictRetryDelay: 20 * time.Millisecond})
	opt.Flux = flux.New(flux.Config{
		Get: func(ctx context.Context, cl, gvr, ns, name string) (*unstructured.Unstructured, error) {
			g, v, r, _ := ingest.ParseGVR(gvr)
			c, err := connFn(cl)
			if err != nil {
				return nil, err
			}
			return c.Dynamic().Resource(schema.GroupVersionResource{Group: g, Version: v, Resource: r}).Namespace(ns).Get(ctx, name, metav1.GetOptions{})
		},
		Scan: mgr.ScanKind, Clusters: func() []string { return mgr.List().Selected }, Resolve: mgr.Resolve,
	})
	hc := helm.Config{
		Runtime: func(id string) (kube.Runtime, error) {
			c, err := connFn(id)
			if err != nil {
				return nil, err
			}
			rt, _ := kube.RuntimeOf(c)
			return rt, nil
		},
		Route: func(ctx context.Context, cl, verb, gvr, sub, ns, name string) (string, bool, error) {
			r, err := svc.Route(ctx, cl, verb, gvr, sub, ns, name)
			if err != nil {
				return "", false, err
			}
			return r.ContextID, r.ViaWrite, nil
		},
	}
	hc.NewActionConfig = func(ctx context.Context, id, ns string) (*action.Configuration, error) {
		rt, err := hc.Runtime(id)
		if err != nil {
			return nil, err
		}
		return helm.NewFakeActionConfig(rt.Clientset(), ns)
	}
	opt.Helm = helm.New(hc)
}

func (e *env) dialStream(t *testing.T, id string) *websocket.Conn {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	t.Cleanup(cancel)
	ws, _, err := websocket.Dial(ctx, "ws"+e.ts.URL[4:]+"/stream", &websocket.DialOptions{
		Subprotocols: []string{protocol.Subprotocol, "token." + testToken, protocol.StreamSubprotocolPrefix + id}})
	if err != nil {
		t.Fatal(err)
	}
	ws.SetReadLimit(4 << 20)
	t.Cleanup(func() { ws.CloseNow() })
	return ws
}

func openStream(t *testing.T, c *client, p protocol.StreamOpenParams) protocol.StreamOpenResult {
	t.Helper()
	var res protocol.StreamOpenResult
	decodeResult(t, c.call("stream.open", p, nil), &res)
	return res
}

func readUntil(t *testing.T, ws *websocket.Conn, d time.Duration, stop func(bin bool, b []byte) bool) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), d)
	defer cancel()
	for {
		typ, b, err := ws.Read(ctx)
		if err != nil {
			t.Fatalf("stream ended before the expected frame: %v", err)
		}
		if stop(typ == websocket.MessageBinary, b) {
			return
		}
	}
}

func selectBoth(t *testing.T, e *env, c *client, ids ...string) {
	decodeResult(t, c.call("clusters.select", protocol.ClustersSelectParams{IDs: ids}, nil), &protocol.ClusterStates{})
	c.waitReady(len(ids))
}

func TestStreamAuthAndSingleUseID(t *testing.T) {
	e := startEnv(t, fake.Config{Clusters: 1, Pods: 100, LogRate: 50}, nil)
	c := connect(t, e)
	selectBoth(t, e, c, "prod-us")
	if r := c.call("stream.open", protocol.StreamOpenParams{Type: "logs", Ref: &protocol.ObjectRef{Cluster: "prod-us", GVR: "v1/pods", Namespace: "team-0", Name: "x"}}, nil); r.Error == nil || r.Error.Code != "not_found" {
		t.Fatalf("missing pod: %+v", r.Error)
	}
	if r := c.call("stream.open", protocol.StreamOpenParams{Type: "nope"}, nil); r.Error == nil || r.Error.Code != "bad_request" {
		t.Fatalf("unknown type: %+v", r.Error)
	}
}

func firstPod(t *testing.T, e *env, cl string) (ns, name string) {
	be, _ := e.srv.opt.Manager.Source().Backend(cl)
	rt, _ := kube.RuntimeOf(be)
	l, err := rt.Clientset().CoreV1().Pods("team-3").List(context.Background(), metav1.ListOptions{LabelSelector: "app=api"})
	if err != nil || len(l.Items) == 0 {
		t.Fatalf("no demo pods: %v", err)
	}
	return l.Items[0].Namespace, l.Items[0].Name
}

func TestLogsStreamOverWebSocketWithAuth(t *testing.T) {
	e := startEnv(t, fake.Config{Clusters: 1, Pods: 100, LogRate: 200}, nil)
	c := connect(t, e)
	selectBoth(t, e, c, "prod-us")
	ns, name := firstPod(t, e, "prod-us")
	res := openStream(t, c, protocol.StreamOpenParams{Type: "logs", Follow: true, Ref: &protocol.ObjectRef{Cluster: "prod-us", GVR: "v1/pods", Namespace: ns, Name: name}})
	if res.StreamID == "" || res.UsedContext != "prod-us" {
		t.Fatalf("%+v", res)
	}
	// wrong token, missing id, bad origin are refused before upgrading
	ctx := context.Background()
	for _, protos := range [][]string{
		{protocol.Subprotocol, "token.nope", "stream." + res.StreamID},
		{protocol.Subprotocol, "token." + testToken},
	} {
		if _, resp, err := websocket.Dial(ctx, "ws"+e.ts.URL[4:]+"/stream", &websocket.DialOptions{Subprotocols: protos}); err == nil || resp == nil || resp.StatusCode != http.StatusForbidden {
			t.Fatalf("want 403: %v %v", err, resp)
		}
	}
	if _, resp, err := websocket.Dial(ctx, "ws"+e.ts.URL[4:]+"/stream", &websocket.DialOptions{HTTPHeader: http.Header{"Origin": {"https://evil.example"}},
		Subprotocols: []string{protocol.Subprotocol, "token." + testToken, "stream." + res.StreamID}}); err == nil || resp.StatusCode != http.StatusForbidden {
		t.Fatalf("origin: %v", err)
	}
	ws := e.dialStream(t, res.StreamID)
	lines := 0
	readUntil(t, ws, 10*time.Second, func(bin bool, b []byte) bool {
		if bin {
			if len(b) > protocol.LogBatchBytes {
				t.Fatalf("frame %d", len(b))
			}
			lines += strings.Count(string(b), "\n")
		}
		return lines >= 300 // history (1000 tail) arrives within the initial credit
	})
	// the id was consumed
	if _, _, err := websocket.Dial(ctx, "ws"+e.ts.URL[4:]+"/stream", &websocket.DialOptions{Subprotocols: []string{protocol.Subprotocol, "token." + testToken, "stream." + res.StreamID}}); err != nil {
		// the upgrade succeeds, the stream is closed with a policy error
		t.Fatalf("dial: %v", err)
	}
}

func TestExecEchoShellAndDownloadAndWriteRouting(t *testing.T) {
	e := startEnv(t, fake.Config{Clusters: 2, Pods: 100, RBAC: true}, nil)
	c := connect(t, e)
	selectBoth(t, e, c, "prod-us", "prod-eu")
	// prod-eu is read-only: exec needs its linked write context; without a link it is refused up front
	ns, name := firstPod(t, e, "prod-eu")
	ref := &protocol.ObjectRef{Cluster: "prod-eu", GVR: "v1/pods", Namespace: ns, Name: name}
	if r := c.call("stream.open", protocol.StreamOpenParams{Type: "exec", Ref: ref}, nil); r.Error == nil || r.Error.Code != "forbidden" {
		t.Fatalf("unlinked write-only stream: %+v", r.Error)
	}
	var st protocol.Settings
	decodeResult(t, c.call("settings.set", protocol.SettingsSetParams{Patch: map[string]any{"links": map[string]any{"prod-eu": "prod-eu-admin"}}}, nil), &st)
	res := openStream(t, c, protocol.StreamOpenParams{Type: "exec", Ref: ref})
	if res.UsedContext != "prod-eu-admin" {
		t.Fatalf("exec must route through the write context: %+v", res)
	}
	ws := e.dialStream(t, res.StreamID)
	var out strings.Builder
	var ready protocol.StreamServerMsg
	ctx := context.Background()
	if err := ws.Write(ctx, websocket.MessageBinary, append([]byte{protocol.StreamChanStdin}, "hi there\r"...)); err != nil {
		t.Fatal(err)
	}
	readUntil(t, ws, 10*time.Second, func(bin bool, b []byte) bool {
		if bin && b[0] == protocol.StreamChanStdout {
			out.Write(b[1:])
		} else if !bin {
			var sm protocol.StreamServerMsg
			_ = json.Unmarshal(b, &sm)
			if sm.Ready != nil {
				ready = sm
			}
		}
		return strings.Contains(out.String(), "echo: hi there")
	})
	if ready.Ready == nil || ready.Ready.UsedContext != "prod-eu-admin" {
		t.Fatalf("ready %+v", ready)
	}
	rz, _ := json.Marshal(protocol.StreamClientMsg{Resize: &protocol.StreamResize{Cols: 100, Rows: 30}})
	_ = ws.Write(ctx, websocket.MessageText, rz)
	_ = ws.Write(ctx, websocket.MessageBinary, append([]byte{protocol.StreamChanStdin}, "exit\r"...))
	var exit *int
	readUntil(t, ws, 10*time.Second, func(bin bool, b []byte) bool {
		if !bin {
			var sm protocol.StreamServerMsg
			_ = json.Unmarshal(b, &sm)
			exit = sm.Exit
		}
		return exit != nil
	})
	if *exit != 0 {
		t.Fatalf("exit %d", *exit)
	}

	// a download: the whole log as one HTTP response, one-shot
	d := openStream(t, c, protocol.StreamOpenParams{Type: "logs", Download: true, TailLines: ptr(int64(500)), Ref: &protocol.ObjectRef{Cluster: "prod-us", GVR: "v1/pods", Namespace: "team-3", Name: mustPodName(t, e, "prod-us")}})
	resp, err := http.Get(e.ts.URL + d.DownloadURL)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || bytes.Count(body, []byte("\n")) != 500 {
		t.Fatalf("%d %d lines", resp.StatusCode, bytes.Count(body, []byte("\n")))
	}
	resp, _ = http.Get(e.ts.URL + d.DownloadURL)
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("download id reuse: %d", resp.StatusCode)
	}
}

func ptr[T any](v T) *T { return &v }

func mustPodName(t *testing.T, e *env, cl string) string {
	_, n := firstPod(t, e, cl)
	return n
}

func (c *client) waitJob(id string, timeout time.Duration) protocol.JobFrame {
	c.t.Helper()
	var last protocol.JobFrame
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		f, ok := c.next(200 * time.Millisecond)
		if !ok {
			continue
		}
		if f.kind == protocol.KindPatch {
			c.ack(f.seq)
		}
		if f.kind != protocol.KindJob {
			continue
		}
		var jf protocol.JobFrame
		if err := msgpack.Unmarshal(f.payload, &jf); err != nil {
			c.t.Fatal(err)
		}
		if jf.JobID == id {
			last = jf
			if jf.State != protocol.JobRunning {
				return jf
			}
		}
	}
	c.t.Fatalf("job %s did not finish: %+v", id, last)
	return last
}

func TestDrainPreflightAndJobOverWire(t *testing.T) {
	e := startEnv(t, fake.Config{Clusters: 1, Pods: 100}, nil)
	c := connect(t, e)
	selectBoth(t, e, c, "prod-us")
	var pf protocol.DrainPreflightResult
	decodeResult(t, c.call("drain.preflight", protocol.DrainPreflightParams{Cluster: "prod-us", Node: "node-3"}, nil), &pf)
	if len(pf.Blocked) != 3 || pf.Blocked[0].PDB == nil || len(pf.Unmanaged) != 1 {
		t.Fatalf("%+v", pf)
	}
	var js protocol.JobsStartResult
	decodeResult(t, c.call("jobs.start", protocol.JobsStartParams{RequestID: "d1", Type: "drain", Params: map[string]any{"cluster": "prod-us", "node": "node-3",
		"options": map[string]any{"force": true, "deleteEmptyDirData": true, "timeoutSeconds": 30}}}, nil), &js)
	jf := c.waitJob(js.JobID, 30*time.Second)
	if jf.State != protocol.JobDone || jf.Type != "drain" || jf.Subject != "node-3" || len(jf.Progress) == 0 {
		t.Fatalf("%+v", jf)
	}
	// the retry with the same requestId returns the same job
	var again protocol.JobsStartResult
	decodeResult(t, c.call("jobs.start", protocol.JobsStartParams{RequestID: "d1", Type: "drain", Params: map[string]any{"cluster": "prod-us", "node": "node-3"}}, nil), &again)
	if again.JobID != js.JobID {
		t.Fatalf("%s != %s", again.JobID, js.JobID)
	}
	var list protocol.JobsListResult
	decodeResult(t, c.call("jobs.list", nil, nil), &list)
	if len(list.Jobs) != 1 || list.Jobs[0].State != protocol.JobDone {
		t.Fatalf("%+v", list)
	}
	if r := c.call("jobs.start", protocol.JobsStartParams{Type: "nope", Params: map[string]any{}}, nil); r.Error == nil || r.Error.Code != "bad_request" {
		t.Fatalf("%+v", r.Error)
	}
	if r := c.call("jobs.cancel", protocol.JobsCancelParams{JobID: "missing"}, nil); r.Error == nil {
		t.Fatal("cancel of a missing job")
	}
}

func getObj(t *testing.T, c *client, ref protocol.ObjectRef) protocol.ObjectPayload {
	t.Helper()
	var p protocol.ObjectPayload
	decodeResult(t, c.call("object.get", protocol.ObjectGetParams{Cluster: ref.Cluster, GVR: ref.GVR, Namespace: ref.Namespace, Name: ref.Name}, nil), &p)
	return p
}

func TestFluxActionsAndReconcileFeedbackOverWire(t *testing.T) {
	e := startEnv(t, fake.Config{Clusters: 1, Pods: 100}, nil)
	c := connect(t, e)
	selectBoth(t, e, c, "prod-us")
	ref := protocol.ObjectRef{Cluster: "prod-us", GVR: "kustomize.toolkit.fluxcd.io/v1/kustomizations", Namespace: "flux-system", Name: "podinfo"}
	var res protocol.MutationResult
	decodeResult(t, c.call("object.action", protocol.ObjectActionParams{RequestID: "f1", Ref: ref, Action: "flux.reconcile"}, nil), &res)
	if res.Token == "" || res.UsedContext != "prod-us" {
		t.Fatalf("%+v", res)
	}
	p := getObj(t, c, ref)
	if !strings.Contains(p.YAML, `reconcile.fluxcd.io/requestedAt: "`+res.Token+`"`) || strings.Contains(p.YAML, `lastHandledReconcileAt: "`+res.Token+`"`) {
		t.Fatalf("right after the request the object is not handled yet:\n%s", p.YAML)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		if p = getObj(t, c, ref); strings.Contains(p.YAML, `lastHandledReconcileAt: "`+res.Token+`"`) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("not handled within 5 s:\n%s", p.YAML)
		}
		time.Sleep(100 * time.Millisecond)
	}
	// suspend, and force on a HelmRelease writes forceAt with the same token
	decodeResult(t, c.call("object.action", protocol.ObjectActionParams{RequestID: "f2", Ref: ref, Action: "flux.suspend"}, nil), &res)
	if p = getObj(t, c, ref); !strings.Contains(p.YAML, "suspend: true") {
		t.Fatalf("%s", p.YAML)
	}
	hr := protocol.ObjectRef{Cluster: "prod-us", GVR: "helm.toolkit.fluxcd.io/v2/helmreleases", Namespace: "flux-system", Name: "podinfo"}
	decodeResult(t, c.call("object.action", protocol.ObjectActionParams{RequestID: "f3", Ref: hr, Action: "flux.force"}, nil), &res)
	p = getObj(t, c, hr)
	if !strings.Contains(p.YAML, `reconcile.fluxcd.io/forceAt: "`+res.Token+`"`) || !strings.Contains(p.YAML, `reconcile.fluxcd.io/requestedAt: "`+res.Token+`"`) {
		t.Fatalf("%s", p.YAML)
	}
	if r := c.call("object.action", protocol.ObjectActionParams{RequestID: "f4", Ref: ref, Action: "flux.force"}, nil); r.Error == nil || r.Error.Code != "bad_request" {
		t.Fatalf("force on a Kustomization: %+v", r.Error)
	}
	// graph and inventory
	var g protocol.FluxGraphResult
	for end := time.Now().Add(10 * time.Second); time.Now().Before(end) && len(g.Nodes) < 5; time.Sleep(100 * time.Millisecond) {
		decodeResult(t, c.call("flux.graph", protocol.FluxGraphParams{Cluster: "prod-us"}, nil), &g) // the baseline watches sync after discovery
	}
	if len(g.Nodes) < 5 || len(g.Edges) == 0 {
		t.Fatalf("graph %d nodes %d edges", len(g.Nodes), len(g.Edges))
	}
	ks := protocol.ObjectRef{Cluster: "prod-us", GVR: "kustomize.toolkit.fluxcd.io/v1/kustomizations", Namespace: "flux-system", Name: "apps"}
	var inv protocol.FluxInventoryResult
	decodeResult(t, c.call("flux.inventory", protocol.FluxInventoryParams{Ref: ks}, nil), &inv)
	if len(inv.Entries) < 10 || inv.Revision == "" {
		t.Fatalf("inventory %+v", inv)
	}
	live := 0
	for _, en := range inv.Entries {
		if en.Live {
			live++
		}
	}
	if live == 0 {
		t.Fatal("no inventory entry was joined to a live object")
	}
}

func TestHelmViewReleaseDiffAndGitopsGuardOverWire(t *testing.T) {
	e := startEnv(t, fake.Config{Clusters: 1, Pods: 100}, nil)
	c := connect(t, e)
	selectBoth(t, e, c, "prod-us")
	var sub protocol.ViewSubscribeResult
	decodeResult(t, c.call("view.subscribe", protocol.ViewSubscribeParams{QID: 1, Gen: 1, Query: protocol.ViewQuery{GVR: protocol.HelmGVR}, Window: protocol.Window{Limit: 50}}, nil), &sub)
	if colKeys(sub.Columns) != "cluster,name,namespace,revision,status,updated,managedBy,statusTone" {
		t.Fatalf("columns %s", colKeys(sub.Columns))
	}
	var rows [][]any
	deadline := time.Now().Add(10 * time.Second)
	for len(rows) < 8 && time.Now().Before(deadline) {
		f, ok := c.next(200 * time.Millisecond)
		if !ok || f.kind != protocol.KindPatch {
			continue
		}
		c.ack(f.seq)
		var vp protocol.ViewPatch
		_ = msgpack.Unmarshal(f.payload, &vp)
		rows = append(rows, vp.Rows...)
	}
	names := map[string]bool{}
	for _, r := range rows {
		names[c.str(r[3])] = true // cluster,name: cells start at index 2
		names[c.str(r[2])] = true
	}
	if !names["podinfo"] || !names["broken-release"] {
		t.Fatalf("release rows: %v", names)
	}
	var rel protocol.HelmRelease
	decodeResult(t, c.call("helm.release", protocol.HelmReleaseParams{Cluster: "prod-us", Namespace: "podinfo", Name: "podinfo"}, nil), &rel)
	if rel.Revision != 3 || rel.Chart == "" || rel.Manifest == "" || len(rel.History) != 3 {
		t.Fatalf("%+v", rel)
	}
	var diff protocol.HelmDiffResult
	decodeResult(t, c.call("helm.diff", protocol.HelmDiffParams{Ref: protocol.ObjectRef{Cluster: "prod-us", GVR: protocol.HelmGVR, Namespace: "podinfo", Name: "podinfo"}, FromRevision: 1, ToRevision: 3}, nil), &diff)
	if !strings.Contains(diff.Values, "@@") && !strings.Contains(diff.Manifest, "@@") {
		t.Fatalf("%+v", diff)
	}
	// actions are jobs; a Flux-managed release is refused with gitops_managed unless forced
	r := c.call("jobs.start", protocol.JobsStartParams{RequestID: "h1", Type: "helm.rollback", Params: map[string]any{"cluster": "prod-us", "namespace": "monitoring", "name": "kube-prometheus-stack", "revision": 1}}, nil)
	if r.Error == nil || r.Error.Code != "gitops_managed" || r.Error.Hint == "" {
		t.Fatalf("%+v", r.Error)
	}
	var js protocol.JobsStartResult
	decodeResult(t, c.call("jobs.start", protocol.JobsStartParams{RequestID: "h2", Type: "helm.rollback", Params: map[string]any{"cluster": "prod-us", "namespace": "podinfo", "name": "podinfo", "revision": 2}}, nil), &js)
	if jf := c.waitJob(js.JobID, 20*time.Second); jf.State != protocol.JobDone {
		t.Fatalf("%+v", jf)
	}
	decodeResult(t, c.call("helm.release", protocol.HelmReleaseParams{Cluster: "prod-us", Namespace: "podinfo", Name: "podinfo"}, nil), &rel)
	if rel.Revision != 4 {
		t.Fatalf("rollback must create revision 4, got %d", rel.Revision)
	}
}

func (c *client) str(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case int8, int16, int32, int64, uint8, uint16, uint32, uint64, int, uint:
		var id uint32
		switch n := x.(type) {
		case int8:
			id = uint32(n)
		case int16:
			id = uint32(n)
		case int32:
			id = uint32(n)
		case int64:
			id = uint32(n)
		case uint8:
			id = uint32(n)
		case uint16:
			id = uint32(n)
		case uint32:
			id = n
		case uint64:
			id = uint32(n)
		case int:
			id = uint32(n)
		case uint:
			id = uint32(n)
		}
		return c.dict[id]
	}
	return ""
}

func TestPortForwardOverWire(t *testing.T) {
	e := startEnv(t, fake.Config{Clusters: 1, Pods: 100}, nil)
	c := connect(t, e)
	selectBoth(t, e, c, "prod-us")
	_, name := firstPod(t, e, "prod-us")
	res := openStream(t, c, protocol.StreamOpenParams{Type: "portforward", Ref: &protocol.ObjectRef{Cluster: "prod-us", GVR: "v1/pods", Namespace: "team-3", Name: name}, Port: 8080})
	if res.LocalPort == 0 || res.ForwardID == "" || res.StreamID != "" {
		t.Fatalf("%+v", res)
	}
	resp, err := http.Get("http://127.0.0.1:" + itoa(res.LocalPort))
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if !strings.Contains(string(b), name) {
		t.Fatalf("%s", b)
	}
	var l protocol.PortForwardListResult
	decodeResult(t, c.call("portforward.list", nil, nil), &l)
	if len(l.Forwards) != 1 || l.Forwards[0].ID != res.ForwardID || l.Forwards[0].State != "running" {
		t.Fatalf("%+v", l)
	}
	mustOK(t, c.call("portforward.stop", protocol.PortForwardStopParams{ID: res.ForwardID}, nil))
	time.Sleep(100 * time.Millisecond)
	if _, err := http.Get("http://127.0.0.1:" + itoa(res.LocalPort)); err == nil {
		t.Fatal("forward still listening after stop")
	}
	if r := c.call("portforward.stop", protocol.PortForwardStopParams{ID: "nope"}, nil); r.Error == nil {
		t.Fatal("unknown forward")
	}
}

func itoa(n int) string { b, _ := json.Marshal(n); return string(b) }
