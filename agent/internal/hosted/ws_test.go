package hosted

import (
	"bytes"
	"context"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/vmihailenco/msgpack/v5"
	"github.com/zyno-io/v8s/agent/internal/wire"
	"github.com/zyno-io/v8s/agent/pkg/protocol"
)

// wsClient is a minimal protocol client over the cookie-authenticated socket.
type wsClient struct {
	t      *testing.T
	ws     *websocket.Conn
	ctx    context.Context
	frames chan wsFrame
	stash  []wsFrame
	last   protocol.ClusterStates
	nextID uint32
}

type wsFrame struct {
	kind    protocol.FrameKind
	channel uint32
	seq     uint32
	payload []byte
}

func goodHeaders(e *env) http.Header {
	return http.Header{"Origin": {e.ts.URL}, "Sec-Fetch-Site": {"same-origin"}}
}

func (b *browser) dialWS(h http.Header, protos ...string) (*wsClient, *http.Response, error) {
	t := b.e.t
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	t.Cleanup(cancel)
	if protos == nil {
		protos = []string{protocol.Subprotocol}
	}
	hc := &http.Client{Jar: b.Jar, Transport: b.Transport}
	ws, resp, err := websocket.Dial(ctx, "wss"+b.e.ts.URL[5:]+"/ws", &websocket.DialOptions{HTTPClient: hc, HTTPHeader: h, Subprotocols: protos})
	if err != nil {
		return nil, resp, err
	}
	ws.SetReadLimit(4 << 20)
	c := &wsClient{t: t, ws: ws, ctx: ctx, frames: make(chan wsFrame, 4096)}
	t.Cleanup(func() { ws.CloseNow() })
	go func() {
		defer close(c.frames)
		for {
			_, msg, err := ws.Read(ctx)
			if err != nil {
				return
			}
			k, ch, seq, p, err := wire.ParseFrame(msg)
			if err != nil {
				return
			}
			c.frames <- wsFrame{k, ch, seq, p}
		}
	}()
	return c, resp, nil
}

func (c *wsClient) next(d time.Duration) (wsFrame, bool) {
	if len(c.stash) > 0 {
		f := c.stash[0]
		c.stash = c.stash[1:]
		return f, true
	}
	select {
	case f, ok := <-c.frames:
		return f, ok
	case <-time.After(d):
		return wsFrame{}, false
	}
}

func (c *wsClient) send(kind protocol.FrameKind, channel, seq uint32, payload any) {
	c.t.Helper()
	var buf bytes.Buffer
	buf.Write(make([]byte, wire.HeaderLen))
	if payload != nil {
		if err := msgpack.NewEncoder(&buf).Encode(payload); err != nil {
			c.t.Fatal(err)
		}
	}
	wire.PutHeader(buf.Bytes(), kind, channel, seq)
	if err := c.ws.Write(c.ctx, websocket.MessageBinary, buf.Bytes()); err != nil {
		c.t.Fatal(err)
	}
}

func (c *wsClient) call(method string, params any) protocol.Response {
	c.t.Helper()
	c.nextID++
	id := c.nextID
	c.send(protocol.KindRequest, id, 0, protocol.Request{Method: method, Params: params})
	for {
		var f wsFrame
		var ok bool
		select {
		case f, ok = <-c.frames:
		case <-time.After(10 * time.Second):
		}
		if !ok {
			c.t.Fatalf("no response to %s", method)
		}
		if f.kind != protocol.KindResponse || f.channel != id {
			c.stash = append(c.stash, f)
			continue
		}
		{
			var raw struct {
				Result msgpack.RawMessage  `msgpack:"result"`
				Error  *protocol.ErrorBody `msgpack:"error"`
			}
			if err := msgpack.Unmarshal(f.payload, &raw); err != nil {
				c.t.Fatal(err)
			}
			return protocol.Response{Result: []byte(raw.Result), Error: raw.Error}
		}
	}
}

func decode(t *testing.T, r protocol.Response, v any) {
	t.Helper()
	if r.Error != nil {
		t.Fatalf("error response: %+v", r.Error)
	}
	if err := msgpack.Unmarshal(r.Result.([]byte), v); err != nil {
		t.Fatal(err)
	}
}

func (c *wsClient) hello() protocol.Hello {
	f, ok := c.next(5 * time.Second)
	if !ok || f.kind != protocol.KindHello {
		c.t.Fatalf("no hello: %+v", f)
	}
	var h protocol.Hello
	if err := msgpack.Unmarshal(f.payload, &h); err != nil {
		c.t.Fatal(err)
	}
	return h
}

func (c *wsClient) waitReady(id string) {
	c.t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		f, ok := c.next(time.Second)
		if !ok || f.kind != protocol.KindClusterState {
			continue
		}
		var st protocol.ClusterStates
		if err := msgpack.Unmarshal(f.payload, &st); err != nil {
			c.t.Fatal(err)
		}
		c.last = st
		for _, s := range st.States {
			if s.ID == id && s.Phase == protocol.PhaseReady {
				return
			}
		}
	}
	c.t.Fatalf("cluster %s did not become ready; last state %+v", id, c.last)
}

func TestWebSocketAuth(t *testing.T) {
	e := newEnv(t, nil)
	anon := e.browser()
	b := e.browser()
	b.login("alice-sub")

	status := func(err error, resp *http.Response) int {
		if err == nil {
			t.Fatal("dial unexpectedly succeeded")
		}
		if resp == nil {
			t.Fatalf("no response: %v", err)
		}
		return resp.StatusCode
	}
	hdr := func(origin, sfs string) http.Header {
		h := http.Header{}
		if origin != "" {
			h.Set("Origin", origin)
		}
		if sfs != "" {
			h.Set("Sec-Fetch-Site", sfs)
		}
		return h
	}
	cases := []struct {
		name   string
		b      *browser
		h      http.Header
		protos []string
		want   int
	}{
		{"missing cookie", anon, goodHeaders(e), nil, 401},
		{"foreign origin", b, hdr("https://evil.example", "same-origin"), nil, 403},
		{"missing origin", b, hdr("", "same-origin"), nil, 403},
		{"missing sec-fetch-site", b, hdr(e.ts.URL, ""), nil, 403},
		{"cross-site fetch metadata", b, hdr(e.ts.URL, "cross-site"), nil, 403},
		{"same-site (sibling subdomain) fetch metadata", b, hdr(e.ts.URL, "same-site"), nil, 403},
		{"missing subprotocol", b, goodHeaders(e), []string{"token.abc"}, 400},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, resp, err := tc.b.dialWS(tc.h, tc.protos...)
			if got := status(err, resp); got != tc.want {
				t.Fatalf("got %d, want %d", got, tc.want)
			}
		})
	}
	if e.srv.pool.Retained() != 0 {
		t.Fatal("a rejected socket started an engine")
	}
	// The desktop token subprotocol is not an alternative to the cookie.
	_, resp, err := anon.dialWS(goodHeaders(e), protocol.Subprotocol, "token.deadbeef")
	if status(err, resp) != 401 {
		t.Fatal("token subprotocol accepted without a session")
	}
	// A valid request works and reports hosted mode.
	c, _, err := b.dialWS(goodHeaders(e))
	if err != nil {
		t.Fatalf("good dial: %v", err)
	}
	if h := c.hello(); h.Mode != "hosted" || h.Version != protocol.Version {
		t.Fatalf("hello: %+v", h)
	}
	// Ending the session closes the socket.
	e.srv.store.Delete(b.cookie(SessionCookie).Value)
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		f, ok := c.next(200 * time.Millisecond)
		_ = f
		if !ok && c.ctx.Err() == nil {
			select {
			case _, open := <-c.frames:
				if !open {
					return
				}
			default:
			}
		}
	}
	t.Fatal("socket survived logout")
}

func TestEndToEndSubscribePods(t *testing.T) {
	e := newEnv(t, nil)
	b := e.browser()
	b.login("alice-sub")
	c, _, err := b.dialWS(goodHeaders(e))
	if err != nil {
		t.Fatal(err)
	}
	c.hello()

	var list protocol.ClustersListResult
	decode(t, c.call("clusters.list", nil), &list)
	ids := []string{}
	for _, cl := range list.Clusters {
		ids = append(ids, cl.ID)
		if cl.ID == "cluster-1" && (len(cl.Groups) != 1 || cl.Groups[0] != "Dev") {
			t.Fatalf("display groups: %+v", cl.Groups)
		}
	}
	if strings.Join(ids, ",") != "cluster-1,cluster-3" { // devs: cluster-1, "*": cluster-3; not ops-only cluster-2
		t.Fatalf("alice sees %v", ids)
	}
	var st protocol.ClusterStates
	decode(t, c.call("clusters.select", protocol.ClustersSelectParams{IDs: []string{"cluster-1"}}), &st)
	c.waitReady("cluster-1")

	// The backend got the user's own cluster token (aud = the cluster app), via a per-request token source.
	e.mu.Lock()
	creds := e.creds["alice@example.com/cluster-1"]
	e.mu.Unlock()
	tok, err := creds.Token()
	if err != nil || audOf(t, tok) != "k8s-app" {
		t.Fatalf("cluster credential: %v aud=%q", err, audOf(t, tok))
	}
	if creds.Corr == "" || strings.Contains(creds.Corr, b.cookie(SessionCookie).Value) {
		t.Fatalf("correlation id %q must be an HMAC, never the session id", creds.Corr)
	}

	var sub protocol.ViewSubscribeResult
	decode(t, c.call("view.subscribe", protocol.ViewSubscribeParams{QID: 1, Gen: 1, Window: protocol.Window{Limit: 50}}), &sub)
	if len(sub.Columns) != len(protocol.PodColumns) {
		t.Fatalf("pod columns: %d", len(sub.Columns))
	}
	deadline := time.Now().Add(20 * time.Second)
	got := false
	for time.Now().Before(deadline) && !got {
		f, ok := c.next(time.Second)
		if !ok || f.kind != protocol.KindPatch {
			continue
		}
		var vp protocol.ViewPatch
		if err := msgpack.Unmarshal(f.payload, &vp); err != nil {
			t.Fatal(err)
		}
		c.send(protocol.KindAck, 0, f.seq, nil)
		got = vp.Total == 100 && len(vp.Rows) > 0 // 300 fake pods / 3 clusters, one cluster selected
	}
	if !got {
		t.Fatal("never received the pods patch")
	}
	// Port-forward is disabled in hosted mode.
	pf := c.call("stream.open", protocol.StreamOpenParams{Type: "portforward", Port: 8080,
		Ref: &protocol.ObjectRef{Cluster: "cluster-1", GVR: "v1/pods", Namespace: "default", Name: "x"}})
	if pf.Error == nil || pf.Error.Code != "forbidden" {
		t.Fatalf("port-forward must be refused in hosted mode, got %+v", pf.Error)
	}
	// Settings are per user and persisted server-side, as the same document as desktop.
	if _, err := os.Stat(e.srv.settingsPath("alice-sub")); err != nil {
		t.Fatalf("per-user settings not persisted: %v", err)
	}

	// Nothing secret in the logs: no session id, ID token, refresh token or client secret.
	logs := e.logs.String()
	sess := b.session()
	for name, secret := range map[string]string{"session id": sess.ID(), "cluster ID token": tok, "refresh token": sess.refresh,
		"login secret": loginSecret, "cluster secret": clusterSecret} {
		if secret != "" && strings.Contains(logs, secret) {
			t.Fatalf("%s leaked into the logs", name)
		}
	}
}
