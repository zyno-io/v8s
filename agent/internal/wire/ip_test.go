package wire

import (
	"testing"
	"time"

	"github.com/zyno-io/v8s/agent/internal/fake"
	"github.com/zyno-io/v8s/agent/pkg/protocol"
)

// An IP search request returns the grouped hits next to the name hits; plain names carry no ip.
func TestSearchIPOverWire(t *testing.T) {
	e := startEnv(t, fake.Config{Clusters: 1, Pods: 300}, nil)
	c, _, err := e.dial(t, nil, protocol.Subprotocol, "token."+testToken)
	if err != nil {
		t.Fatal(err)
	}
	if h, ok := c.next(5 * time.Second); !ok || h.kind != protocol.KindHello {
		t.Fatal("no hello")
	}
	decodeResult(t, c.call("clusters.select", protocol.ClustersSelectParams{IDs: []string{"prod-us"}}, nil), &protocol.ClusterStates{})
	c.waitReady(1)

	var sr protocol.SearchResult
	for end := time.Now().Add(20 * time.Second); ; time.Sleep(20 * time.Millisecond) {
		sr = protocol.SearchResult{}
		decodeResult(t, c.call("search", protocol.SearchParams{Text: "10.96.250.1", Limit: 10, IPPerKind: 3}, nil), &sr)
		if sr.IP != nil && len(sr.IP.Pending) == 0 && len(sr.IP.Groups) > 0 {
			break
		}
		if time.Now().After(end) {
			t.Fatalf("no IP result: %+v", sr.IP)
		}
	}
	if sr.IP.Query != "10.96.250.1" || sr.IP.Mode != protocol.IPQueryAddr || sr.IP.Groups[0].Kind != "Service" {
		t.Fatalf("ip: %+v", sr.IP)
	}
	h := sr.IP.Groups[0].Hits[0]
	if h.Name != "ingress-nginx-controller" || h.Namespace != "ingress-nginx" || h.Match != protocol.IPMatchExact || h.Label != "Cluster IP" || h.GVR != "v1/services" {
		t.Fatalf("hit: %+v", h)
	}

	sr = protocol.SearchResult{}
	decodeResult(t, c.call("search", protocol.SearchParams{Text: "svc-12", Limit: 10}, nil), &sr)
	if sr.IP != nil || len(sr.Results) == 0 {
		t.Fatalf("name search: %+v", sr)
	}
}
