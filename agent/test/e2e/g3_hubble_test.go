//go:build e2e

package e2e

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/zyno-io/v8s/agent/internal/cluster"
	"github.com/zyno-io/v8s/agent/internal/hubble"
	"github.com/zyno-io/v8s/agent/internal/kube"
	"github.com/zyno-io/v8s/agent/internal/problems"
	"github.com/zyno-io/v8s/agent/internal/streams"
	"github.com/zyno-io/v8s/agent/pkg/protocol"
)

const (
	hubbleCtx = "kind-v8s-e2e-cilium"
)

type hubbleTransport struct {
	mu     sync.Mutex
	frames []protocol.FlowsFrame
	closed chan struct{}
	once   sync.Once
}

func (h *hubbleTransport) Read(ctx context.Context) (bool, []byte, error) {
	select {
	case <-ctx.Done():
		return false, nil, ctx.Err()
	case <-h.closed:
		return false, nil, errors.New("closed")
	}
}

func (h *hubbleTransport) Write(_ context.Context, _ bool, data []byte) error {
	var f protocol.FlowsFrame
	if err := json.Unmarshal(data, &f); err != nil {
		return err
	}
	h.mu.Lock()
	h.frames = append(h.frames, f)
	h.mu.Unlock()
	return nil
}

func (h *hubbleTransport) Close(int, string) { h.once.Do(func() { close(h.closed) }) }

func (h *hubbleTransport) flows() (n int, dropped int, rate *protocol.FlowRate) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, f := range h.frames {
		n += len(f.Flows)
		for _, fl := range f.Flows {
			if fl.Verdict == "DROPPED" {
				dropped++
			}
		}
		if f.Rate != nil {
			rate = f.Rate
		}
	}
	return
}

// TestHubbleOnCiliumCluster talks to the hubble-relay of the local Cilium kind cluster (namespace shop:
// frontend -> backend with a deny-all-ingress NetworkPolicy on backend, so POLICY_DENIED drops are expected).
// It is read-only. Skipped when the cluster is not there.
func TestHubbleOnCiliumCluster(t *testing.T) {
	path := os.Getenv("V8S_E2E_HUBBLE_KUBECONFIG")
	if path == "" {
		t.Skip("V8S_E2E_HUBBLE_KUBECONFIG not set (test/e2e/cluster.sh cilium-up writes test/e2e/out/kubeconfig)")
	}
	if _, err := os.Stat(path); err != nil {
		t.Skipf("no cilium kubeconfig at %s (%v)", path, err)
	}
	_ = os.Setenv("KUBE_FEATURE_WatchListClient", "true")
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	src := &cluster.KubeSource{Loader: kube.Loader{Path: path}}
	var have bool
	for _, c := range src.Clusters() {
		have = have || c.ID == hubbleCtx
	}
	if !have {
		t.Skipf("context %s not in %s", hubbleCtx, path)
	}
	be, err := src.Backend(hubbleCtx)
	if err != nil {
		t.Skipf("backend: %v", err)
	}
	dctx, dcancel := context.WithTimeout(ctx, 15*time.Second)
	info, err := hubble.Discover(dctx, be.Dynamic())
	dcancel()
	if err != nil {
		t.Skipf("cilium cluster API not answering (tunnel on 127.0.0.1:46444?): %v", err)
	}

	hub := problems.NewHub(problems.HubConfig{Hold: time.Second})
	eng := hub.Cluster(hubbleCtx)
	var clients atomic.Int32
	clients.Store(1)
	svc := hubble.New(hubble.Config{
		Conn:         func(id string) (kube.Conn, error) { return src.Backend(id) },
		Clusters:     func() []string { return []string{hubbleCtx} },
		Problems:     hub,
		ClientCount:  func() int { return int(clients.Load()) },
		MonitorEvery: 500 * time.Millisecond,
	})
	defer svc.Close()

	t.Run("discovery", func(t *testing.T) {
		if info.Service != "hubble-relay" || info.Namespace != "kube-system" || info.Pod == "" || info.Port == 0 {
			t.Fatalf("relay info %+v", info)
		}
		t.Logf("relay %s/%s pod %s port %d", info.Namespace, info.Service, info.Pod, info.Port)
	})

	t.Run("status through a port-forward", func(t *testing.T) {
		st := svc.Status(ctx, hubbleCtx)
		if !st.Available || st.MaxFlows == 0 || st.Version == "" {
			t.Fatalf("status %+v", st)
		}
		t.Logf("status: version %s nodes %d/%d flows %d/%d (%.1f/s)", st.Version, st.ConnectedNodes, len(st.Nodes), st.NumFlows, st.MaxFlows, st.FlowsPerSec)
	})

	t.Run("flows stream scoped to shop", func(t *testing.T) {
		var run func(context.Context, streams.Transport)
		_, err := svc.OpenFlows(ctx, protocol.StreamOpenParams{Type: "flows", Flows: &protocol.FlowsStreamParams{
			Cluster: hubbleCtx, Scope: protocol.FlowScope{Namespace: "shop"}}}, func(f func(context.Context, streams.Transport)) string { run = f; return "e2e" })
		if err != nil {
			t.Fatal(err)
		}
		tr := &hubbleTransport{closed: make(chan struct{})}
		sctx, scancel := context.WithCancel(ctx)
		done := make(chan struct{})
		go func() { run(sctx, tr); close(done) }()
		defer func() { scancel(); <-done }()
		deadline := time.Now().Add(60 * time.Second)
		for time.Now().Before(deadline) {
			if n, _, _ := tr.flows(); n >= 5 {
				break
			}
			time.Sleep(250 * time.Millisecond)
		}
		n, dropped, _ := tr.flows()
		if n == 0 {
			t.Fatal("no flows for namespace shop in 60 s")
		}
		tr.mu.Lock()
		for _, f := range tr.frames {
			for _, fl := range f.Flows {
				if fl.Src.Namespace != "shop" && fl.Dst.Namespace != "shop" {
					t.Fatalf("flow outside the scope: %+v", fl)
				}
			}
		}
		tr.mu.Unlock()
		t.Logf("%d flows (%d dropped) in the stream", n, dropped)
	})

	t.Run("unscoped follow is refused", func(t *testing.T) {
		_, err := svc.OpenFlows(ctx, protocol.StreamOpenParams{Type: "flows", Flows: &protocol.FlowsStreamParams{Cluster: hubbleCtx}},
			func(func(context.Context, streams.Transport)) string { return "x" })
		if err == nil {
			t.Fatal("unscoped flows stream accepted")
		}
	})

	t.Run("drops view", func(t *testing.T) {
		var d protocol.HubbleDrops
		waitFor(t, "POLICY_DENIED drops for backend", 90*time.Second, func() bool {
			d = svc.Drops(ctx, protocol.HubbleDropsParams{Clusters: []string{hubbleCtx}, WindowSec: 600})
			for _, g := range d.Groups {
				if g.Dst.Workload == "backend" && g.Dst.Namespace == "shop" && (g.Reason == "POLICY_DENIED" || g.Reason == "POLICY_DENY") {
					return true
				}
			}
			return false
		})
		for _, g := range d.Groups {
			if g.Dst.Workload == "backend" {
				t.Logf("drop group %s/%s -> %s/%s %s x%d policies %+v", g.Src.Namespace, g.Src.Workload, g.Dst.Namespace, g.Dst.Workload, g.Reason, g.Count, g.Policies)
				found := false
				for _, p := range g.Policies {
					found = found || p.Name == "deny-all-ingress"
				}
				if !found {
					t.Errorf("deny-all-ingress not named as a likely policy: %+v", g.Policies)
				}
			}
		}
	})

	t.Run("service map", func(t *testing.T) {
		sm, err := svc.ServiceMap(ctx, protocol.HubbleServiceMapParams{Cluster: hubbleCtx, Namespace: "shop", WindowSec: 600})
		if err != nil {
			t.Fatal(err)
		}
		if len(sm.Edges) == 0 {
			t.Fatalf("empty service map: %+v", sm)
		}
		t.Logf("service map: %d nodes %d edges", len(sm.Nodes), len(sm.Edges))
	})

	t.Run("network.policy-drops problem", func(t *testing.T) {
		if hub.ClassEnabled(hubbleCtx, hubble.ClassPolicyDrops) {
			t.Fatal("network.policy-drops must be off by default")
		}
		// enable it the way the UI does: settings.problemPrefs
		set := protocol.Settings{ProblemPrefs: map[string]any{"classes": map[string]any{hubble.ClassPolicyDrops: true}}, Acks: map[string]any{}}
		hub.SettingsChanged(set)
		mctx, mcancel := context.WithCancel(ctx)
		defer mcancel()
		svc.StartProblems(mctx)
		var p problems.Problem
		waitFor(t, "network.policy-drops problem", 120*time.Second, func() bool {
			for _, x := range eng.Problems() {
				if x.Class == hubble.ClassPolicyDrops && x.Subject.Name == "backend" {
					p = x
					return true
				}
			}
			return false
		})
		if p.Subject.NS != "shop" || p.Count < 10 || p.Source != "hubble" {
			t.Fatalf("problem %+v", p)
		}
		t.Logf("problem: %s (%d)", p.Title, p.Count)
		clients.Store(0)
		waitFor(t, "subscription to stop without clients", 30*time.Second, func() bool { return !svc.Subscribed(hubbleCtx) })
	})
}
