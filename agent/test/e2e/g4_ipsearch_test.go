//go:build e2e

package e2e

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/zyno-io/v8s/agent/internal/cluster"
	"github.com/zyno-io/v8s/agent/internal/ipx"
	"github.com/zyno-io/v8s/agent/internal/kube"
	"github.com/zyno-io/v8s/agent/internal/view"
	"github.com/zyno-io/v8s/agent/pkg/protocol"
)

// TestIPSearch searches the real cluster by address: a pod's IP finds the pod (exact), and the API server's
// ClusterIP 10.96.0.1 finds the `kubernetes` Service (the default kind and k3d service CIDR; set
// V8S_E2E_API_CLUSTER_IP for another).
func TestIPSearch(t *testing.T) {
	path := os.Getenv("V8S_E2E_KUBECONFIG")
	if path == "" {
		t.Skip("V8S_E2E_KUBECONFIG not set")
	}
	_ = os.Setenv("KUBE_FEATURE_WatchListClient", "true")
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	src := &cluster.KubeSource{Loader: kube.Loader{Path: path}}
	cs := src.Clusters()
	if len(cs) == 0 {
		t.Fatal("no contexts in the kubeconfig")
	}
	id := cs[0].ID
	eng := view.NewEngine()
	go eng.Run(ctx)
	m := cluster.NewManager(ctx, src, eng, nil)
	defer m.Close()
	if _, err := m.Select([]string{id}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "cluster ready", 60*time.Second, func() bool {
		st := m.States().States
		return len(st) == 1 && st[0].Phase == protocol.PhaseReady
	})

	// search retries while sources still load (pending), for up to 5 s
	search := func(text string, ok func(*protocol.IPSearch) bool) *protocol.IPSearch {
		t.Helper()
		q, parsed := ipx.Parse(text)
		if !parsed {
			t.Fatalf("%q is not an IP query", text)
		}
		var res *protocol.IPSearch
		waitFor(t, "IP search "+text, 5*time.Second, func() bool {
			res = m.SearchIP(ctx, q, 5)
			return len(res.Pending) == 0 && ok(res)
		})
		return res
	}
	find := func(res *protocol.IPSearch, kind string) *protocol.IPGroup {
		for i := range res.Groups {
			if res.Groups[i].Kind == kind {
				return &res.Groups[i]
			}
		}
		return nil
	}

	// any pod address in the cluster (a /0 CIDR contains every IPv4 token)
	all := search("0.0.0.0/0", func(r *protocol.IPSearch) bool { return find(r, "Pod") != nil })
	pod := find(all, "Pod").Hits[0]
	res := search(pod.Value, func(r *protocol.IPSearch) bool { return find(r, "Pod") != nil })
	g := find(res, "Pod")
	if g.Hits[0].Match != protocol.IPMatchExact || g.Hits[0].Value != pod.Value {
		t.Fatalf("pod %s: %+v", pod.Value, g.Hits)
	}
	found := false
	for _, h := range g.Hits {
		found = found || (h.Name == pod.Name && h.Namespace == pod.Namespace)
	}
	if !found {
		t.Fatalf("pod %s/%s not among the hits for its own IP: %+v", pod.Namespace, pod.Name, g.Hits)
	}

	apiIP := os.Getenv("V8S_E2E_API_CLUSTER_IP")
	if apiIP == "" {
		apiIP = "10.96.0.1"
	}
	res = search(apiIP, func(r *protocol.IPSearch) bool { return find(r, "Service") != nil })
	svc := find(res, "Service").Hits[0]
	if svc.Name != "kubernetes" || svc.Namespace != "default" || svc.Match != protocol.IPMatchExact {
		t.Fatalf("%s: %+v", apiIP, svc)
	}
}
