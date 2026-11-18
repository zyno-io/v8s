package cluster

import (
	"context"
	"testing"
	"time"

	"github.com/zyno-io/v8s/agent/internal/disco"
	"github.com/zyno-io/v8s/agent/internal/fake"
	"github.com/zyno-io/v8s/agent/internal/ingest"
	"github.com/zyno-io/v8s/agent/internal/ipx"
	"github.com/zyno-io/v8s/agent/internal/kube"
	"github.com/zyno-io/v8s/agent/internal/view"
	"github.com/zyno-io/v8s/agent/pkg/protocol"
)

// searchIP waits until every IP store of the selected clusters is synced, then searches.
func searchIP(t *testing.T, m *Manager, text string, perKind int) *protocol.IPSearch {
	t.Helper()
	q, ok := ipx.Parse(text)
	if !ok {
		t.Fatalf("%q is not an IP query", text)
	}
	var res *protocol.IPSearch
	waitFor(t, "IP stores synced for "+text, func() bool {
		res = m.SearchIP(context.Background(), q, perKind)
		return len(res.Pending) == 0
	})
	return res
}

func group(res *protocol.IPSearch, kind string) *protocol.IPGroup {
	for i := range res.Groups {
		if res.Groups[i].Kind == kind {
			return &res.Groups[i]
		}
	}
	return nil
}

func ipManager(t *testing.T, cfg fake.Config) *Manager {
	t.Helper()
	m, _ := newFakeManager(t, cfg)
	return m
}

func TestSearchIPPodEndpointNode(t *testing.T) {
	m := ipManager(t, fake.Config{Clusters: 1, Pods: 2000})
	if _, err := m.Select([]string{"prod-us"}); err != nil {
		t.Fatal(err)
	}
	// An endpoint address is a real pod IP: find one through the pod CIDRs.
	all := searchIP(t, m, "10.64.0.0/10", 50)
	eps := group(all, "EndpointSlice")
	if eps == nil || len(eps.Hits) == 0 {
		t.Fatalf("no endpoint hits: %+v", all.Groups)
	}
	if h := eps.Hits[0]; h.Match != protocol.IPMatchWithin || h.Label != "Endpoint" || h.Detail == "" {
		t.Fatalf("endpoint hit within a CIDR: %+v", h)
	}
	ip := eps.Hits[0].Value
	svc := eps.Hits[0].Detail

	res := searchIP(t, m, ip, 5)
	pods, ep, node := group(res, "Pod"), group(res, "EndpointSlice"), group(res, "Node")
	if pods == nil || pods.Hits[0].Match != protocol.IPMatchExact || pods.Hits[0].Label != "Pod IP" || pods.Hits[0].Value != ip {
		t.Fatalf("pod group: %+v", pods)
	}
	if ep == nil || ep.Hits[0].Match != protocol.IPMatchExact || ep.Hits[0].Detail != svc {
		t.Fatalf("endpoint group: %+v", ep)
	}
	if node == nil || node.Hits[0].Match != protocol.IPMatchContains || node.Hits[0].Label != "Pod CIDR" {
		t.Fatalf("node group: %+v", node)
	}
	if res.Mode != protocol.IPQueryAddr || res.Query != ip {
		t.Fatalf("mode/query: %s %s", res.Mode, res.Query)
	}
	// exact groups come before the node's contains match
	if last := res.Groups[len(res.Groups)-1]; last.Hits[0].Match != protocol.IPMatchContains {
		t.Fatalf("group order: %+v", res.Groups)
	}

	// a node address
	res = searchIP(t, m, "10.0.0.10", 5)
	if g := group(res, "Node"); g == nil || g.Hits[0].Name != "node-0" || g.Hits[0].Match != protocol.IPMatchExact || g.Hits[0].Label != "Node internal IP" {
		t.Fatalf("node ip: %+v", res.Groups)
	}
}

func TestSearchIPPrefixCIDRRange(t *testing.T) {
	m := ipManager(t, fake.Config{Clusters: 1, Pods: 500})
	if _, err := m.Select([]string{"prod-us"}); err != nil {
		t.Fatal(err)
	}
	res := searchIP(t, m, "10.96.0.", 5)
	svc := group(res, "Service")
	if svc == nil || svc.Total < 200 || len(svc.Hits) != 5 {
		t.Fatalf("service prefix group: %+v", svc)
	}
	for _, h := range svc.Hits {
		if h.Match != protocol.IPMatchPrefix || h.Label != "Cluster IP" {
			t.Fatalf("hit: %+v", h)
		}
	}
	if svc.GVR != "v1/services" {
		t.Fatalf("gvr: %s", svc.GVR)
	}
	// per-kind cap is honored and clamped
	if g := group(searchIP(t, m, "10.96.0.", 2), "Service"); len(g.Hits) != 2 || g.Total != svc.Total {
		t.Fatalf("cap: %+v", g)
	}
	if g := group(searchIP(t, m, "10.96.0.", 1000), "Service"); len(g.Hits) != 50 {
		t.Fatalf("max cap: %d", len(g.Hits))
	}

	res = searchIP(t, m, "10.64.0.0/23", 5)
	if g := group(res, "Pod"); g == nil || g.Hits[0].Match != protocol.IPMatchWithin {
		t.Fatalf("cidr pods: %+v", g)
	}
	if g := group(res, "Node"); g == nil || g.Hits[0].Match != protocol.IPMatchExact || g.Hits[0].Name != "node-0" {
		t.Fatalf("cidr node: %+v", g)
	}

	res = searchIP(t, m, "10.96.250.1-10.96.250.2", 5)
	if g := group(res, "Service"); g == nil || g.Total != 2 || g.Hits[0].Match != protocol.IPMatchWithin || res.Mode != protocol.IPQueryRange {
		t.Fatalf("range: %+v", res)
	}

	// dual stack: v6 and v4 do not mix
	res = searchIP(t, m, "fd00:10:96::1", 5)
	if g := group(res, "Service"); g == nil || g.Hits[0].Name != "dual" || g.Hits[0].Match != protocol.IPMatchExact || len(res.Groups) != 1 {
		t.Fatalf("v6: %+v", res.Groups)
	}
	if res := searchIP(t, m, "fd00:10:", 5); group(res, "Service") == nil || res.Mode != protocol.IPQueryPrefix {
		t.Fatalf("v6 prefix: %+v", res)
	}
	if res := searchIP(t, m, "172.31.255.1", 5); len(res.Groups) != 0 {
		t.Fatalf("unknown address must not match: %+v", res.Groups)
	}
}

func TestSearchIPMultiCluster(t *testing.T) {
	m := ipManager(t, fake.Config{Clusters: 2, Pods: 800})
	if _, err := m.Select([]string{"prod-us", "prod-eu"}); err != nil {
		t.Fatal(err)
	}
	res := searchIP(t, m, "192.168.100.10", 5)
	svc, ing := group(res, "Service"), group(res, "Ingress")
	if svc == nil || svc.Total != 2 || len(svc.Hits) != 2 {
		t.Fatalf("services: %+v", svc)
	}
	if svc.Hits[0].Cluster != "prod-eu" || svc.Hits[1].Cluster != "prod-us" || svc.Hits[0].Name != "ingress-nginx-controller" {
		t.Fatalf("hits not ordered by cluster: %+v", svc.Hits)
	}
	if svc.Hits[0].Label != "LB ingress IP" && svc.Hits[0].Label != "Requested LB IP" {
		t.Fatalf("label: %+v", svc.Hits[0])
	}
	if ing == nil || ing.Total < 2 {
		t.Fatalf("ingresses: %+v", ing)
	}
	// the legacy-lb Service only requests 192.168.100.250
	res = searchIP(t, m, "192.168.100.250", 5)
	if g := group(res, "Service"); g == nil || g.Hits[0].Name != "legacy-lb" || g.Hits[0].Label != "Requested LB IP" {
		t.Fatalf("legacy-lb: %+v", res.Groups)
	}
}

type denyKindBackend struct {
	kube.Backend
	gvr string
}

func (b denyKindBackend) API(gvr string, m *ingest.Module) (ingest.API, error) {
	if gvr == b.gvr {
		return forbiddenAPI{}, nil
	}
	return b.Backend.API(gvr, m)
}

type denyKindSource struct {
	*fake.Source
	gvr string
}

func (s denyKindSource) Backend(id string) (Backend, error) {
	b, err := s.Source.Backend(id)
	if err != nil {
		return nil, err
	}
	return denyKindBackend{b, s.gvr}, nil
}

func TestSearchIPSkipsForbiddenKinds(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	eng := view.NewEngine()
	go eng.Run(ctx)
	m := NewManager(ctx, denyKindSource{fake.NewSource(fake.Config{Clusters: 1, Pods: 200}), "v1/services"}, eng, nil)
	defer m.Close()
	if _, err := m.Select([]string{"prod-us"}); err != nil {
		t.Fatal(err)
	}
	q, _ := ipx.Parse("10.96.0.")
	var res *protocol.IPSearch
	waitFor(t, "forbidden services skipped", func() bool {
		res = m.SearchIP(ctx, q, 5)
		for _, s := range res.Skipped {
			if s.Kind == "Service" && s.Reason == "forbidden" && s.Cluster == "prod-us" {
				return true
			}
		}
		return false
	})
	if group(res, "Service") != nil {
		t.Fatalf("a forbidden kind has no group: %+v", res.Groups)
	}
}

type slowDiscoverBackend struct {
	kube.Backend
	release chan struct{}
}

func (b slowDiscoverBackend) Discover(ctx context.Context) ([]disco.Resource, error) {
	select {
	case <-b.release:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	return b.Backend.Discover(ctx)
}

type slowSource struct {
	*fake.Source
	release chan struct{}
}

func (s slowSource) Backend(id string) (Backend, error) {
	b, err := s.Source.Backend(id)
	if err != nil {
		return nil, err
	}
	return slowDiscoverBackend{b, s.release}, nil
}

func TestSearchIPPendingBeforeDiscovery(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	eng := view.NewEngine()
	go eng.Run(ctx)
	release := make(chan struct{})
	m := NewManager(ctx, slowSource{fake.NewSource(fake.Config{Clusters: 1, Pods: 200}), release}, eng, nil)
	defer m.Close()
	if _, err := m.Select([]string{"prod-us"}); err != nil {
		t.Fatal(err)
	}
	q, _ := ipx.Parse("10.96.0.1")
	var res *protocol.IPSearch
	waitFor(t, "pending cluster", func() bool {
		res = m.SearchIP(ctx, q, 5)
		for _, p := range res.Pending {
			if p.Cluster == "prod-us" && p.Kind == "" {
				return true
			}
		}
		return false
	})
	close(release)
	waitFor(t, "no longer pending", func() bool {
		res = m.SearchIP(ctx, q, 5)
		return len(res.Pending) == 0 && group(res, "Service") != nil
	})
}

func TestSearchAllNameAndIP(t *testing.T) {
	m := ipManager(t, fake.Config{Clusters: 1, Pods: 300})
	if _, err := m.Select([]string{"prod-us"}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "name hits", func() bool {
		return len(m.SearchAll(context.Background(), protocol.SearchParams{Text: "svc-1", Limit: 5}).Results) > 0
	})
	res := m.SearchAll(context.Background(), protocol.SearchParams{Text: "svc-1", Limit: 5})
	if res.IP != nil {
		t.Fatalf("name text must not carry ip: %+v", res.IP)
	}
	var got protocol.SearchResult
	waitFor(t, "ip result", func() bool {
		got = m.SearchAll(context.Background(), protocol.SearchParams{Text: "10.96.0.", Limit: 5})
		return got.IP != nil && len(got.IP.Pending) == 0 && len(got.IP.Groups) > 0
	})
	if got.Results == nil {
		t.Fatal("the name search still runs in IP mode")
	}
	// a wrong detection costs nothing: "1.2" is a prefix, and the name search finds nothing
	if r := m.SearchAll(context.Background(), protocol.SearchParams{Text: "1.2", Limit: 5}); r.IP == nil || r.IP.Mode != protocol.IPQueryPrefix {
		t.Fatalf("1.2: %+v", r.IP)
	}
}

const (
	gvrPools = "metallb.io/v1beta1/ipaddresspools"
	gvrNADs  = "k8s.cni.cncf.io/v1/network-attachment-definitions"
)

// onDemandIPWatches lists the running watches of on-demand IP kinds.
func onDemandIPWatches(m *Manager, cl string) map[string]WatchInfo {
	out := map[string]WatchInfo{}
	for _, w := range m.WatchInfo(cl) {
		if mod := ingest.Lookup(w.GVR); mod != nil && mod.IPOnDemand {
			out[w.GVR] = w
		}
	}
	return out
}

func TestWeakIPQueryStartsNothing(t *testing.T) {
	m := ipManager(t, fake.Config{Clusters: 1, Pods: 200})
	if _, err := m.Select([]string{"prod-us"}); err != nil {
		t.Fatal(err)
	}
	readyAndDiscovered(t, m, "prod-us")
	for _, text := range []string{"10.", "1.2", "10.96", "fd00::"} {
		if res := searchIP(t, m, text, 5); res.Mode != protocol.IPQueryPrefix {
			t.Fatalf("%s: %+v", text, res)
		}
	}
	if w := onDemandIPWatches(m, "prod-us"); len(w) != 0 {
		t.Fatalf("a weak query started watches: %+v", w)
	}
}

func TestStrongIPQueryTouchesSourcesAndTheyLinger(t *testing.T) {
	m := ipManager(t, fake.Config{Clusters: 1, Pods: 200})
	m.Linger = 400 * time.Millisecond
	if _, err := m.Select([]string{"prod-us"}); err != nil {
		t.Fatal(err)
	}
	readyAndDiscovered(t, m, "prod-us")
	res := searchIP(t, m, "192.168.100.10", 5)
	// the MetalLB pool `public` (192.168.100.0/24) contains the address; the Service holds it
	pool := group(res, "IPAddressPool")
	if pool == nil || pool.Hits[0].Name != "public" || pool.Hits[0].Match != protocol.IPMatchContains || pool.Hits[0].Label != "Pool range" || pool.Hits[0].Value != "192.168.100.0/24" {
		t.Fatalf("pool group: %+v (all: %+v)", pool, res.Groups)
	}
	if svc := group(res, "Service"); svc == nil || svc.Hits[0].Match != protocol.IPMatchExact {
		t.Fatalf("service group: %+v", res.Groups)
	}
	if res.Groups[0].Kind != "Service" && res.Groups[0].Kind != "Ingress" {
		t.Fatalf("exact matches first: %+v", res.Groups)
	}
	ws := onDemandIPWatches(m, "prod-us")
	for _, gvr := range []string{gvrPools, gvrNADs, "networking.k8s.io/v1/servicecidrs"} {
		if w, ok := ws[gvr]; !ok || w.Tier0 || w.Refs != 0 {
			t.Fatalf("%s: %+v (%v)", gvr, w, ws)
		}
	}
	// a later keystroke of the same query restarts the linger instead of starting anything new
	time.Sleep(250 * time.Millisecond)
	searchIP(t, m, "192.168.100.10", 5)
	time.Sleep(250 * time.Millisecond)
	if _, ok := onDemandIPWatches(m, "prod-us")[gvrPools]; !ok {
		t.Fatal("a repeated query must reset the linger")
	}
	// and they stop after the linger
	waitFor(t, "sources stop", func() bool { return len(onDemandIPWatches(m, "prod-us")) == 0 })
}

func TestNADHitAndOddEvenClusters(t *testing.T) {
	m := ipManager(t, fake.Config{Clusters: 2, Pods: 200})
	if _, err := m.Select([]string{"prod-us", "prod-eu"}); err != nil {
		t.Fatal(err)
	}
	// 192.168.50.7 is in team-1/macvlan-conf's range (and may be a pod's secondary address)
	res := searchIP(t, m, "192.168.50.7", 5)
	nad := group(res, "NetworkAttachmentDefinition")
	if nad == nil || nad.Total != 2 || nad.Hits[0].Name != "macvlan-conf" || nad.Hits[0].Namespace != "team-1" || nad.Hits[0].Match != protocol.IPMatchContains {
		t.Fatalf("nad: %+v", res.Groups)
	}
	// Calico runs on the odd-index cluster only, Cilium on the even-index one
	res = searchIP(t, m, "10.64.1.5", 5)
	if g := group(res, "IPPool"); g == nil || g.Total != 1 || g.Hits[0].Cluster != "prod-eu" {
		t.Fatalf("calico pool: %+v", res.Groups)
	}
	res = searchIP(t, m, "172.18.250.9", 5)
	if g := group(res, "CiliumLoadBalancerIPPool"); g == nil || g.Total != 1 || g.Hits[0].Cluster != "prod-us" {
		t.Fatalf("cilium lb pool: %+v", res.Groups)
	}
}

func TestStrongQueryReportsBudgetSkips(t *testing.T) {
	m := ipManager(t, fake.Config{Clusters: 1, Pods: 100})
	if _, err := m.Select([]string{"prod-us"}); err != nil {
		t.Fatal(err)
	}
	readyAndDiscovered(t, m, "prod-us")
	m.Budget = len(m.WatchInfo("prod-us")) // exactly the baseline: nothing lingering can be evicted
	res := searchIP(t, m, "192.168.100.10", 5)
	budget := 0
	for _, s := range res.Skipped {
		if s.Reason == "budget" {
			budget++
		}
	}
	if budget == 0 || len(onDemandIPWatches(m, "prod-us")) != 0 {
		t.Fatalf("skipped=%+v watches=%v", res.Skipped, onDemandIPWatches(m, "prod-us"))
	}
	if group(res, "Service") == nil {
		t.Fatal("the baseline kinds are still searched")
	}
}

func TestForbiddenOnDemandSourceIsSkipped(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	eng := view.NewEngine()
	go eng.Run(ctx)
	m := NewManager(ctx, denyKindSource{fake.NewSource(fake.Config{Clusters: 1, Pods: 100}), gvrPools}, eng, nil)
	defer m.Close()
	if _, err := m.Select([]string{"prod-us"}); err != nil {
		t.Fatal(err)
	}
	readyAndDiscovered(t, m, "prod-us")
	q, _ := ipx.Parse("192.168.100.10")
	var res *protocol.IPSearch
	waitFor(t, "forbidden pools skipped", func() bool {
		res = m.SearchIP(ctx, q, 5)
		for _, s := range res.Skipped {
			if s.Kind == "IPAddressPool" && s.Reason == "forbidden" {
				return true
			}
		}
		return false
	})
	if group(res, "NetworkAttachmentDefinition") != nil && group(res, "IPAddressPool") != nil {
		t.Fatalf("pools must be absent: %+v", res.Groups)
	}
}
