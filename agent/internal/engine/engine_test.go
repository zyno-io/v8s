package engine

import (
	"context"
	"testing"

	"github.com/zyno-io/v8s/agent/internal/fake"
	"github.com/zyno-io/v8s/agent/internal/settings"
	"github.com/zyno-io/v8s/agent/pkg/protocol"
)

// The Hubble problem monitor asks for the client count from its own goroutine; before the wire server exists
// that must answer 0 rather than dereference nil.
func TestClientCountBeforeServerExists(t *testing.T) {
	e := &Engine{}
	if n := e.clientCount(); n != 0 {
		t.Fatalf("client count before the server exists: %d", n)
	}
}

// update.status is always registered: without a checker (fake mode, opted out) it answers an empty status.
func TestUpdateStatusHandlerNilChecker(t *testing.T) {
	set, err := settings.Open("")
	if err != nil {
		t.Fatal(err)
	}
	e := New(Config{Ctx: context.Background(), Source: fake.NewSource(fake.Config{Clusters: 1, Pods: 5}), Settings: set})
	defer e.Close()
	h := e.Handlers["update.status"]
	if h == nil {
		t.Fatal("no update.status handler")
	}
	res, err := h(context.Background(), nil)
	if err != nil || res != (protocol.UpdateStatus{}) {
		t.Fatalf("res %+v err %v", res, err)
	}
}

// A settings store outlives hosted engines: every closed engine must detach its callback.
func TestCloseUnsubscribesSettings(t *testing.T) {
	set, err := settings.Open("")
	if err != nil {
		t.Fatal(err)
	}
	src := fake.NewSource(fake.Config{Clusters: 1, Pods: 5})
	base := set.Listeners()
	for i := 0; i < 5; i++ {
		e := New(Config{Ctx: context.Background(), Source: src, Settings: set})
		if set.Listeners() != base+1 {
			t.Fatalf("round %d: listeners = %d, want %d", i, set.Listeners(), base+1)
		}
		e.Close()
		if n := set.Listeners(); n != base {
			t.Fatalf("round %d: listeners after Close = %d, want %d", i, n, base)
		}
	}
}
