package wire

import (
	"bytes"
	"context"
	"encoding/binary"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/vmihailenco/msgpack/v5"
	"github.com/zyno-io/v8s/agent/internal/cluster"
	"github.com/zyno-io/v8s/agent/internal/fake"
	"github.com/zyno-io/v8s/agent/internal/ingest"
	"github.com/zyno-io/v8s/agent/internal/kube"
	"github.com/zyno-io/v8s/agent/internal/ops"
	"github.com/zyno-io/v8s/agent/internal/settings"
	"github.com/zyno-io/v8s/agent/internal/store"
	"github.com/zyno-io/v8s/agent/internal/view"
	"github.com/zyno-io/v8s/agent/pkg/protocol"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const testToken = "deadbeef"

type env struct {
	ts  *httptest.Server
	mgr *cluster.Manager
	eng *view.Engine
	srv *Server
}

func startEnv(t *testing.T, cfg fake.Config, mut func(*Options)) *env {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	eng := view.NewEngine()
	src := fake.NewSource(cfg)
	mgr := cluster.NewManager(ctx, src, eng, nil)
	set, _ := settings.Open("")
	mgr.SetSettings(set)
	svc := ops.New(ops.Config{Conn: func(id string) (kube.Conn, error) { return src.Backend(mgr.Effective(id)) }, Links: mgr.Links, RawConn: func(id string) (kube.Conn, error) { return src.Backend(id) }, Elevated: mgr.Elevated, Resolve: mgr.Resolve})
	go eng.Run(ctx)
	opt := Options{Manager: mgr, Engine: eng, Ops: svc, Settings: set, Token: testToken, Mode: "fake", AgentVersion: "test", Tick: 100 * time.Millisecond}
	g2Services(ctx, &opt, src, mgr, svc, set)
	if mut != nil {
		mut(&opt)
	}
	srv := NewServer(opt)
	ts := httptest.NewServer(srv.Handler())
	u, _ := url.Parse(ts.URL)
	port, _ := strconv.Atoi(u.Port())
	srv.SetPort(port)
	t.Cleanup(func() {
		ts.Close()
		if opt.Streams != nil {
			opt.Streams.Close()
		}
		cancel()
		mgr.Close()
	})
	return &env{ts: ts, mgr: mgr, eng: eng, srv: srv}
}

type frame struct {
	kind    protocol.FrameKind
	channel uint32
	seq     uint32
	payload []byte
	size    int
}

type client struct {
	t      *testing.T
	ws     *websocket.Conn
	ctx    context.Context
	frames chan frame
	dict   map[uint32]string
	nextID uint32
	// ready is the number of clusters in the latest cluster.state frame received by any call to next. Cluster
	// state is pushed on change only, so a test that waits for readiness after other calls have already consumed
	// the frame (they will under a slow race build) must look at what it has seen, not wait for another frame.
	ready int
}

func (e *env) dial(t *testing.T, header http.Header, protos ...string) (*client, *http.Response, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	t.Cleanup(cancel)
	if protos == nil {
		protos = []string{protocol.Subprotocol, "token." + testToken}
	}
	ws, resp, err := websocket.Dial(ctx, "ws"+e.ts.URL[4:]+"/ws", &websocket.DialOptions{Subprotocols: protos, HTTPHeader: header})
	if err != nil {
		return nil, resp, err
	}
	ws.SetReadLimit(4 << 20)
	c := &client{t: t, ws: ws, ctx: ctx, frames: make(chan frame, 4096), dict: map[uint32]string{}}
	t.Cleanup(func() { ws.CloseNow() })
	go func() {
		for {
			_, b, err := ws.Read(ctx)
			if err != nil {
				close(c.frames)
				return
			}
			k, ch, seq, p, err := ParseFrame(b)
			if err != nil {
				t.Errorf("bad frame: %v", err)
				return
			}
			c.frames <- frame{k, ch, seq, p, len(b)}
		}
	}()
	return c, resp, nil
}

func (c *client) next(timeout time.Duration) (frame, bool) {
	select {
	case f, ok := <-c.frames:
		if ok && f.size > protocol.MaxFrameBytes {
			c.t.Fatalf("frame of %d bytes exceeds %d", f.size, protocol.MaxFrameBytes)
		}
		if ok && f.kind == protocol.KindClusterState {
			var st protocol.ClusterStates
			if err := msgpack.Unmarshal(f.payload, &st); err != nil {
				c.t.Fatal(err)
			}
			c.ready = 0
			for _, s := range st.States {
				if s.Phase == protocol.PhaseReady {
					c.ready++
				}
			}
		}
		if ok && f.kind == protocol.KindDict {
			var d protocol.DictAdd
			if err := msgpack.Unmarshal(f.payload, &d); err != nil {
				c.t.Fatal(err)
			}
			for i, s := range d.Strings {
				c.dict[d.Base+uint32(i)] = s
			}
		}
		return f, ok
	case <-time.After(timeout):
		return frame{}, false
	}
}

func (c *client) send(kind protocol.FrameKind, channel, seq uint32, payload any) {
	c.t.Helper()
	var buf bytes.Buffer
	buf.Write(make([]byte, HeaderLen))
	if payload != nil {
		if err := msgpack.NewEncoder(&buf).Encode(payload); err != nil {
			c.t.Fatal(err)
		}
	}
	PutHeader(buf.Bytes(), kind, channel, seq)
	if err := c.ws.Write(c.ctx, websocket.MessageBinary, buf.Bytes()); err != nil {
		c.t.Fatal(err)
	}
}

func (c *client) ack(seq uint32) { c.send(protocol.KindAck, 0, seq, nil) }

// call sends a request and returns the raw response payload, buffering nothing: other frames received
// meanwhile are handed to onOther.
func (c *client) call(method string, params any, onOther func(frame)) protocol.Response {
	c.t.Helper()
	c.nextID++
	id := c.nextID
	c.send(protocol.KindRequest, id, 0, protocol.Request{Method: method, Params: params})
	for {
		f, ok := c.next(10 * time.Second)
		if !ok {
			c.t.Fatalf("no response to %s", method)
		}
		if f.kind == protocol.KindResponse && f.channel == id {
			var raw struct {
				Result msgpack.RawMessage  `msgpack:"result"`
				Error  *protocol.ErrorBody `msgpack:"error"`
			}
			if err := msgpack.Unmarshal(f.payload, &raw); err != nil {
				c.t.Fatal(err)
			}
			return protocol.Response{Result: []byte(raw.Result), Error: raw.Error}
		}
		if onOther != nil {
			onOther(f)
		}
	}
}

func decodeResult(t *testing.T, r protocol.Response, v any) {
	t.Helper()
	if r.Error != nil {
		t.Fatalf("error response: %+v", r.Error)
	}
	if err := msgpack.Unmarshal(r.Result.([]byte), v); err != nil {
		t.Fatal(err)
	}
}

func (c *client) waitReady(n int) {
	c.t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		if c.ready == n {
			return
		}
		c.next(time.Second) // updates c.ready on a cluster.state frame
	}
	c.t.Fatalf("clusters did not become ready: %d of %d", c.ready, n)
}

func TestAuthAndOrigin(t *testing.T) {
	e := startEnv(t, fake.Config{Clusters: 1, Pods: 10}, func(o *Options) { o.AllowedOrigins = []string{"https://app.example"} })
	cases := []struct {
		name   string
		header http.Header
		protos []string
		ok     bool
	}{
		{"valid, no origin", nil, nil, true},
		{"valid, allowed origin", http.Header{"Origin": {"https://app.example"}}, nil, true},
		{"valid, own origin", http.Header{"Origin": {e.ts.URL}}, nil, true},
		{"wrong token", nil, []string{protocol.Subprotocol, "token.nope"}, false},
		{"missing token", nil, []string{protocol.Subprotocol}, false},
		{"missing subprotocol", nil, []string{"token." + testToken}, false},
		{"bad origin", http.Header{"Origin": {"https://evil.example"}}, nil, false},
		{"dev origin not allowed without dev mode", http.Header{"Origin": {"http://localhost:5173"}}, nil, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c, resp, err := e.dial(t, tc.header, tc.protos...)
			if tc.ok {
				if err != nil {
					t.Fatalf("dial: %v", err)
				}
				f, _ := c.next(2 * time.Second)
				var h protocol.Hello
				if f.kind != protocol.KindHello || msgpack.Unmarshal(f.payload, &h) != nil || h.Version != protocol.Version || h.Mode != "fake" {
					t.Fatalf("bad hello: %+v %+v", f, h)
				}
				return
			}
			if err == nil || resp == nil || resp.StatusCode != http.StatusForbidden {
				t.Fatalf("want 403, got err=%v resp=%v", err, resp)
			}
		})
	}
	e2 := startEnv(t, fake.Config{Clusters: 1, Pods: 10}, func(o *Options) { o.DevOrigins = true })
	if _, _, err := e2.dial(t, http.Header{"Origin": {"http://localhost:5173"}}); err != nil {
		t.Fatalf("dev origin should be allowed: %v", err)
	}
	if _, _, err := e2.dial(t, http.Header{"Origin": {"http://localhost.evil.com"}}); err == nil {
		t.Fatal("lookalike origin must be rejected")
	}
}

func TestFrameCodecRoundTrip(t *testing.T) {
	b := make([]byte, HeaderLen+3)
	PutHeader(b, protocol.KindPatch, 0xdeadbeef, 0x01020304)
	copy(b[HeaderLen:], "abc")
	if b[0] != 4 || binary.BigEndian.Uint32(b[1:]) != 0xdeadbeef {
		t.Fatalf("layout: %x", b)
	}
	k, ch, seq, p, err := ParseFrame(b)
	if err != nil || k != protocol.KindPatch || ch != 0xdeadbeef || seq != 0x01020304 || string(p) != "abc" {
		t.Fatalf("round trip: %v %v %v %q %v", k, ch, seq, p, err)
	}
	if _, _, _, _, err := ParseFrame(b[:5]); err == nil {
		t.Fatal("short frame must fail")
	}
}

// TestPatchEncodingRoundTrip encodes a real window through the patch encoder and decodes it as protocol.ViewPatch.
func TestPatchEncodingRoundTrip(t *testing.T) {
	eng := view.NewEngine()
	src := fake.NewCluster("t", 400, 0, 7)
	api := src
	_ = api
	st := fillStore(t, src)
	eng.Attach("t", st)
	sub, err := eng.Subscribe(protocol.ViewQuery{}, 3, protocol.Window{Offset: 10, Limit: 50})
	if err != nil {
		t.Fatal(err)
	}
	c := &conn{}
	c.initCodec()
	p := sub.Prepare()
	if p == nil || len(p.Rows) != 50 || !p.SendIDs {
		t.Fatalf("pending: %+v", p)
	}
	nr, nd, withIDs := c.buildPatch(p)
	if nr != 50 || !withIDs {
		t.Fatalf("nr=%d ids=%v", nr, withIDs)
	}
	frameBytes := c.buf.Bytes()
	_, _, _, payload, _ := ParseFrame(frameBytes)
	var vp protocol.ViewPatch
	if err := msgpack.Unmarshal(payload, &vp); err != nil {
		t.Fatal(err)
	}
	if vp.Gen != 3 || vp.Total != 400 || vp.Offset != 10 || len(vp.IDs) != 200 || len(vp.Rows) != 50 || nd != 0 {
		t.Fatalf("decoded: gen=%d total=%d off=%d ids=%d rows=%d", vp.Gen, vp.Total, vp.Offset, len(vp.IDs), len(vp.Rows))
	}
	for i, r := range vp.Rows {
		if len(r) != 2+len(protocol.PodColumns) {
			t.Fatalf("row width %d", len(r))
		}
		id := binary.LittleEndian.Uint32(vp.IDs[4*i:])
		if toU32(r[0]) != id {
			t.Fatalf("row %d id %v != ids[%d]=%d", i, r[0], i, id)
		}
		if _, ok := r[3].(string); !ok {
			t.Fatalf("name cell is %T", r[3])
		}
	}
	// Dictionary strings referenced by dict-typed cells were registered, ids start at 1.
	if add, ok := c.d.take(1 << 20); !ok || add.Base != 1 || len(add.Strings) == 0 {
		t.Fatalf("dict: %+v", add)
	}
}

func toU32(v any) uint32 {
	switch n := v.(type) {
	case uint8:
		return uint32(n)
	case uint16:
		return uint32(n)
	case uint32:
		return n
	case uint64:
		return uint32(n)
	case int8:
		return uint32(n)
	case int16:
		return uint32(n)
	case int32:
		return uint32(n)
	case int64:
		return uint32(n)
	}
	return 1<<32 - 1
}

func TestEndToEndFake(t *testing.T) {
	e := startEnv(t, fake.Config{Clusters: 3, Pods: 3000, Churn: 1500}, nil)
	c, _, err := e.dial(t, nil)
	if err != nil {
		t.Fatal(err)
	}
	if f, _ := c.next(2 * time.Second); f.kind != protocol.KindHello {
		t.Fatal("expected hello first")
	}
	var list protocol.ClustersListResult
	decodeResult(t, c.call("clusters.list", nil, nil), &list)
	if len(list.Clusters) != 3 || len(list.Selected) != 3 || list.Clusters[0].ID != "prod-us" || list.Clusters[1].ID != "prod-eu" || list.Clusters[2].ID != "staging" {
		t.Fatalf("clusters.list: %+v", list)
	}
	if r := c.call("nope", nil, nil); r.Error == nil || r.Error.Code != "bad_request" {
		t.Fatalf("unknown method: %+v", r)
	}
	if r := c.call("clusters.select", protocol.ClustersSelectParams{IDs: []string{"missing"}}, nil); r.Error == nil {
		t.Fatal("selecting an unknown cluster must fail")
	}
	var st protocol.ClusterStates
	decodeResult(t, c.call("clusters.select", protocol.ClustersSelectParams{IDs: list.Selected}, nil), &st)
	waitKinds := time.Now().Add(10 * time.Second)
	for time.Now().Before(waitKinds) {
		var kr protocol.KindsListResult
		decodeResult(t, c.call("kinds.list", nil, nil), &kr)
		if len(kr.Kinds) >= 25 && len(kr.Kinds[0].Clusters) == 3 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	var kinds protocol.KindsListResult
	decodeResult(t, c.call("kinds.list", nil, nil), &kinds)
	byGVR := map[string]protocol.Kind{}
	for _, k := range kinds.Kinds {
		byGVR[k.GVR] = k
		if !k.Available || len(k.Clusters) != 3 {
			t.Fatalf("kind not served by all selected clusters: %+v", k)
		}
	}
	if k := byGVR["v1/pods"]; !k.Curated || k.ShortNames[0] != "po" || k.Version != "v1" {
		t.Fatalf("bad pods kind: %+v", k)
	}
	if k := byGVR["storage.k8s.io/v1/storageclasses"]; k.Curated || k.Group != "storage.k8s.io" || len(kinds.Kinds) < 25 {
		t.Fatalf("bad generic kind: %+v (%d kinds)", k, len(kinds.Kinds))
	}
	if len(st.States) != 3 {
		t.Fatalf("select states: %+v", st)
	}
	c.waitReady(3)

	var sub protocol.ViewSubscribeResult
	decodeResult(t, c.call("view.subscribe", protocol.ViewSubscribeParams{QID: 1, Gen: 1,
		Window: protocol.Window{Limit: 300}}, nil), &sub)
	if len(sub.Columns) != len(protocol.PodColumns) || sub.Columns[1].Key != "name" {
		t.Fatalf("columns: %+v", sub.Columns)
	}

	var first *protocol.ViewPatch
	rows, patches := 0, 0
	var updates int
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) && updates < 2 {
		f, ok := c.next(time.Second)
		if !ok {
			continue
		}
		if f.kind != protocol.KindPatch {
			continue
		}
		var vp protocol.ViewPatch
		if err := msgpack.Unmarshal(f.payload, &vp); err != nil {
			t.Fatal(err)
		}
		if f.channel != 1 || vp.Gen != 1 {
			t.Fatalf("patch for wrong sub: ch=%d gen=%d", f.channel, vp.Gen)
		}
		c.ack(f.seq)
		patches++
		if first == nil {
			first = &vp
			rows = len(vp.Rows)
			// the fake clusters churn (adds and deletes) while the test runs, so the total is only about 3000;
			// the ids always cover the window, whose size is bounded by the total
			if vp.Total < 2900 || vp.Total > 3100 || len(vp.IDs) != 4*300 || rows < 300 {
				t.Fatalf("first patch: total=%d ids=%d rows=%d", vp.Total, len(vp.IDs), rows)
			}
			r := vp.Rows[0]
			if c.dict[toU32(r[2])] == "" {
				t.Fatalf("cluster cell does not resolve through the dictionary: %v", r[2])
			}
			continue
		}
		if len(vp.Rows) > 0 || len(vp.Removed) > 0 || vp.IDs != nil {
			updates++
		}
	}
	if first == nil || updates < 2 {
		t.Fatalf("churn updates not observed: patches=%d updates=%d", patches, updates)
	}
}

// TestBackpressure: a client that never acks receives at most MaxUnacked patch frames / MaxInFlightBytes;
// acking releases more; the whole large first patch eventually arrives, split into <=128 KiB frames.
func TestBackpressure(t *testing.T) {
	e := startEnv(t, fake.Config{Clusters: 1, Pods: 20000}, func(o *Options) { o.Tick = 5 * time.Millisecond })
	c, _, err := e.dial(t, nil)
	if err != nil {
		t.Fatal(err)
	}
	c.next(time.Second) // hello
	decodeResult(t, c.call("clusters.select", protocol.ClustersSelectParams{IDs: []string{"prod-us"}}, nil), &protocol.ClusterStates{})
	c.waitReady(1)
	c.call("view.subscribe", protocol.ViewSubscribeParams{QID: 9, Gen: 1, Window: protocol.Window{Limit: 20000}}, nil)

	var held []frame
	bytesHeld := 0
	end := time.Now().Add(1500 * time.Millisecond)
	for time.Now().Before(end) {
		f, ok := c.next(100 * time.Millisecond)
		if ok && f.kind == protocol.KindPatch {
			held = append(held, f)
			bytesHeld += f.size
			if len(held) > protocol.MaxUnacked || bytesHeld > protocol.MaxInFlightBytes {
				t.Fatalf("unacked patches: %d frames, %d bytes", len(held), bytesHeld)
			}
		}
	}
	if len(held) != protocol.MaxUnacked {
		t.Fatalf("expected exactly %d patch frames while unacked, got %d", protocol.MaxUnacked, len(held))
	}

	seen := map[uint32]bool{}
	var ids []byte
	total := 0
	absorb := func(f frame) {
		var vp protocol.ViewPatch
		if err := msgpack.Unmarshal(f.payload, &vp); err != nil {
			t.Fatal(err)
		}
		total = vp.Total
		if len(vp.IDs) > 0 {
			ids = vp.IDs
		}
		for _, r := range vp.Rows {
			seen[toU32(r[0])] = true
		}
		c.ack(f.seq)
	}
	for _, f := range held {
		absorb(f)
	}
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) && (total < 20000 || len(seen) < total) {
		if f, ok := c.next(200 * time.Millisecond); ok && f.kind == protocol.KindPatch {
			absorb(f)
		}
	}
	if total != 20000 || len(ids) != 4*total || len(seen) < total {
		t.Fatalf("incomplete first patch after acks: ids=%d rows=%d", len(ids)/4, len(seen))
	}
}

// fillStore runs a fake cluster through the real ListerWatcher/reflector path via a manager-less
// shortcut: list pages, project, Replace.
func fillStore(t testing.TB, c *fake.Cluster) *store.Store {
	t.Helper()
	lw := &ingest.ListerWatcher{API: ingest.PodAdapter{PodAPI: c}}
	obj, err := lw.List(metav1.ListOptions{})
	if err != nil {
		t.Fatal(err)
	}
	st := store.New()
	var items []interface{}
	for _, r := range obj.(*ingest.PodRowList).Items {
		items = append(items, r)
	}
	_ = st.Replace(items, "1")
	return st
}
