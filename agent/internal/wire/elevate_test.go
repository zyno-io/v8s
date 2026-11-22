package wire

import (
	"testing"
	"time"

	"github.com/vmihailenco/msgpack/v5"
	"github.com/zyno-io/v8s/agent/internal/fake"
	"github.com/zyno-io/v8s/agent/pkg/protocol"
)

func TestElevateOverWire(t *testing.T) {
	e := startEnv(t, fake.Config{Clusters: 2, Pods: 100, RBAC: true}, nil)
	c, _, err := e.dial(t, nil, protocol.Subprotocol, "token."+testToken)
	if err != nil {
		t.Fatal(err)
	}
	if h, ok := c.next(5 * time.Second); !ok || h.kind != protocol.KindHello {
		t.Fatal("no hello")
	}
	var set protocol.Settings
	decodeResult(t, c.call("settings.set", protocol.SettingsSetParams{Patch: map[string]any{
		"links": map[string]any{"prod-eu": "prod-eu-admin"}, "autoSwitchWrite": false}}, nil), &set)
	decodeResult(t, c.call("clusters.select", protocol.ClustersSelectParams{IDs: []string{"prod-us", "prod-eu"}}, nil), &protocol.ClusterStates{})
	c.waitReady(2)

	// seen is the latest elevation of prod-eu any ClusterState frame reported (the frame may arrive before the
	// response, so every call observes frames too).
	var seen struct{ elevated, ready bool }
	observe := func(f frame) {
		if f.kind == protocol.KindPatch {
			c.ack(f.seq)
		}
		if f.kind != protocol.KindClusterState {
			return
		}
		var st protocol.ClusterStates
		if err := msgpack.Unmarshal(f.payload, &st); err != nil {
			t.Fatal(err)
		}
		for _, s := range st.States {
			if s.ID == "prod-us" && s.Elevated {
				t.Fatal("prod-us elevated")
			}
			if s.ID == "prod-eu" {
				seen.elevated, seen.ready = s.Elevated, s.Phase == protocol.PhaseReady
			}
		}
	}
	waitElevated := func(want bool) {
		t.Helper()
		for end := time.Now().Add(10 * time.Second); time.Now().Before(end); {
			if seen.elevated == want && seen.ready {
				return
			}
			if f, ok := c.next(100 * time.Millisecond); ok {
				observe(f)
			}
		}
		t.Fatalf("no ClusterState with prod-eu elevated=%v", want)
	}

	if r := c.call("clusters.elevate", protocol.ClustersElevateParams{ID: "prod-us", Elevated: true}, observe); r.Error == nil || r.Error.Code != "bad_request" {
		t.Fatalf("unlinked cluster: %+v", r.Error)
	}
	// read-only + autoSwitchWrite off: a restart asks for confirmation
	act := protocol.ObjectActionParams{RequestID: "e1", Ref: protocol.ObjectRef{Cluster: "prod-eu", GVR: "apps/v1/deployments", Namespace: "team-0", Name: "svc-0"}, Action: "restart"}
	if r := c.call("object.action", act, nil); r.Error == nil || r.Error.Code != "write_confirm" {
		t.Fatalf("before elevating: %+v", r.Error)
	}

	var st protocol.ClusterStates
	decodeResult(t, c.call("clusters.elevate", protocol.ClustersElevateParams{ID: "prod-eu", Elevated: true}, observe), &st)
	if len(st.States) != 2 {
		t.Fatalf("states: %+v", st)
	}
	waitElevated(true)

	act.RequestID = "e2"
	var mr protocol.MutationResult
	decodeResult(t, c.call("object.action", act, observe), &mr)
	if mr.UsedContext != "prod-eu-admin" {
		t.Fatalf("elevated action: %+v", mr)
	}

	decodeResult(t, c.call("clusters.elevate", protocol.ClustersElevateParams{ID: "prod-eu", Elevated: false}, observe), &st)
	waitElevated(false)
	act.RequestID = "e3"
	if r := c.call("object.action", act, nil); r.Error == nil || r.Error.Code != "write_confirm" {
		t.Fatalf("after revert: %+v", r.Error)
	}

	// Relinked while elevated: the elevation ends instead of running as a context nobody verified.
	decodeResult(t, c.call("clusters.elevate", protocol.ClustersElevateParams{ID: "prod-eu", Elevated: true}, observe), &st)
	waitElevated(true)
	decodeResult(t, c.call("settings.set", protocol.SettingsSetParams{Patch: map[string]any{"links": map[string]any{"prod-eu": "prod-us"}}}, observe), &set)
	waitElevated(false)
	if got := e.mgr.Elevated("prod-eu"); got != "" {
		t.Fatalf("still elevated as %q after the link changed", got)
	}
}
