package wire

import (
	"context"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/zyno-io/v8s/agent/internal/fake"
	"github.com/zyno-io/v8s/agent/internal/streams"
	"github.com/zyno-io/v8s/agent/pkg/protocol"
)

// A stream body that does not read for longer than interval+timeout (a debug container or node-shell pod that is still
// starting) must not lose its socket: pongs are only processed inside Read.
func TestKeepaliveStreamSurvivesBodyThatDoesNotReadYet(t *testing.T) {
	e := startEnv(t, fake.Config{Clusters: 1, Pods: 10}, func(o *Options) {
		o.PingInterval = 30 * time.Millisecond
		o.PingTimeout = 60 * time.Millisecond
		o.Streams.Register("slow", func(ctx context.Context, p protocol.StreamOpenParams, claim func(run func(ctx context.Context, t streams.Transport)) string) (protocol.StreamOpenResult, error) {
			id := claim(func(ctx context.Context, tr streams.Transport) {
				time.Sleep(500 * time.Millisecond) // many intervals and timeouts without a reader
				if err := tr.Write(ctx, false, []byte("still here")); err != nil {
					return
				}
				_, _, _ = tr.Read(ctx)
			})
			return protocol.StreamOpenResult{StreamID: id}, nil
		})
	})
	c := connect(t, e)
	res := openStream(t, c, protocol.StreamOpenParams{Type: "slow"})
	ws := e.dialStream(t, res.StreamID)
	readUntil(t, ws, 5*time.Second, func(bin bool, b []byte) bool { return !bin && string(b) == "still here" })
}

func dialRaw(t *testing.T, e *env) *websocket.Conn {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	ws, _, err := websocket.Dial(ctx, "ws"+e.ts.URL[4:]+"/ws", &websocket.DialOptions{Subprotocols: []string{protocol.Subprotocol, "token." + testToken}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ws.CloseNow() })
	return ws
}

func waitClients(e *env, want int, d time.Duration) bool {
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if e.srv.ClientCount() == want {
			return true
		}
		time.Sleep(10 * time.Millisecond)
	}
	return e.srv.ClientCount() == want
}

// A client that keeps reading answers the server's pings and stays connected well past several intervals.
func TestKeepaliveAnsweredPingsKeepConnection(t *testing.T) {
	e := startEnv(t, fake.Config{Clusters: 1, Pods: 10}, func(o *Options) {
		o.PingInterval = 30 * time.Millisecond
		o.PingTimeout = 200 * time.Millisecond
	})
	ws := dialRaw(t, e)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		for {
			if _, _, err := ws.Read(ctx); err != nil {
				return
			}
		}
	}()
	if !waitClients(e, 1, 2*time.Second) {
		t.Fatal("client never registered")
	}
	time.Sleep(600 * time.Millisecond) // 20 intervals
	if got := e.srv.ClientCount(); got != 1 {
		t.Fatalf("connection dropped despite answering pings (clients=%d)", got)
	}
}

// A client that never reads cannot answer pings: the server drops it.
func TestKeepaliveUnansweredPingDisconnects(t *testing.T) {
	e := startEnv(t, fake.Config{Clusters: 1, Pods: 10}, func(o *Options) {
		o.PingInterval = 30 * time.Millisecond
		o.PingTimeout = 100 * time.Millisecond
	})
	_ = dialRaw(t, e) // no reader: pongs are only produced while reading
	if !waitClients(e, 1, 2*time.Second) {
		t.Fatal("client never registered")
	}
	if !waitClients(e, 0, 3*time.Second) {
		t.Fatal("unresponsive client was not disconnected")
	}
}
