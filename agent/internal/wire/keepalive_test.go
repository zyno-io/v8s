package wire

import (
	"context"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/zyno-io/v8s/agent/internal/fake"
	"github.com/zyno-io/v8s/agent/pkg/protocol"
)

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
