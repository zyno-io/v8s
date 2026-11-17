package hubble

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/zyno-io/v8s/agent/internal/hubble/pb"
	"github.com/zyno-io/v8s/agent/internal/ops"
	"github.com/zyno-io/v8s/agent/internal/problems/ptest"
	"github.com/zyno-io/v8s/agent/internal/streams"
	"github.com/zyno-io/v8s/agent/pkg/protocol"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/protobuf/types/known/timestamppb"
	"google.golang.org/protobuf/types/known/wrapperspb"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// ---- helpers ------------------------------------------------------------------------------

func startRelay(t *testing.T, r *FakeRelay) *Link {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	stop, _ := r.Serve(ln)
	cc, err := grpc.NewClient(ln.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cc.Close(); stop() })
	return &Link{Info: protocol.HubbleRelayInfo{Namespace: "kube-system", Service: "hubble-relay", Pod: "hubble-relay-x", Port: 4245},
		Client: pb.NewObserverClient(cc), Close: func() {}, Dead: func() bool { return false }}
}

func testService(t *testing.T, r *FakeRelay, mod func(*Config)) *Service {
	t.Helper()
	l := startRelay(t, r)
	cfg := Config{Connect: func(context.Context, string) (*Link, error) { return l, nil }, LinkIdle: time.Hour}
	if mod != nil {
		mod(&cfg)
	}
	s := New(cfg)
	t.Cleanup(s.Close)
	return s
}

type memTransport struct {
	mu     sync.Mutex
	frames []protocol.FlowsFrame
	in     chan []byte
	closed chan struct{}
	once   sync.Once
}

func newMemTransport() *memTransport {
	return &memTransport{in: make(chan []byte, 8), closed: make(chan struct{})}
}

func (m *memTransport) Read(ctx context.Context) (bool, []byte, error) {
	select {
	case b := <-m.in:
		return false, b, nil
	case <-ctx.Done():
		return false, nil, ctx.Err()
	case <-m.closed:
		return false, nil, errors.New("closed")
	}
}

func (m *memTransport) Write(_ context.Context, _ bool, data []byte) error {
	var f protocol.FlowsFrame
	if err := json.Unmarshal(data, &f); err != nil {
		return err
	}
	m.mu.Lock()
	m.frames = append(m.frames, f)
	m.mu.Unlock()
	return nil
}

func (m *memTransport) Close(int, string) { m.once.Do(func() { close(m.closed) }) }

func (m *memTransport) snapshot() []protocol.FlowsFrame {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]protocol.FlowsFrame(nil), m.frames...)
}

func waitFor(t *testing.T, what string, d time.Duration, cond func() bool) {
	t.Helper()
	for end := time.Now().Add(d); time.Now().Before(end); time.Sleep(20 * time.Millisecond) {
		if cond() {
			return
		}
	}
	t.Fatalf("timed out waiting for %s", what)
}

// ---- sampler --------------------------------------------------------------------------------

type fakeClock struct{ t time.Time }

func (c *fakeClock) now() time.Time          { return c.t }
func (c *fakeClock) advance(d time.Duration) { c.t = c.t.Add(d) }

func TestSamplerCapsRateAndReportsTrueRate(t *testing.T) {
	cases := []struct {
		name          string
		offeredPerSec int
		wantShownMax  int
		wantSampled   bool
	}{
		{"below the cap passes everything", 150, 150, false},
		{"above the cap is sampled", 3400, 215, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			clk := &fakeClock{t: time.UnixMilli(1_700_000_000_000)}
			s := NewSampler(200, clk.now)
			shown := 0
			step := time.Second / time.Duration(c.offeredPerSec)
			for i := 0; i < c.offeredPerSec*3; i++ { // three seconds of traffic
				clk.advance(step)
				if s.Offer() {
					shown++
				}
			}
			perSec := shown / 3
			if perSec > c.wantShownMax || perSec < min(c.offeredPerSec, 150)-20 {
				t.Fatalf("shown %d/s want <= %d", perSec, c.wantShownMax)
			}
			sh, tot := s.Rates()
			if tot < c.offeredPerSec-c.offeredPerSec/10 || tot > c.offeredPerSec+c.offeredPerSec/10 {
				t.Fatalf("true rate %d, offered %d", tot, c.offeredPerSec)
			}
			if (tot > sh) != c.wantSampled {
				t.Fatalf("sampled=%v shown=%d total=%d", tot > sh, sh, tot)
			}
		})
	}
}

// ---- scope refusal ----------------------------------------------------------------------------

func TestScopeRefusal(t *testing.T) {
	cases := []struct {
		name  string
		scope protocol.FlowScope
		ok    bool
	}{
		{"empty", protocol.FlowScope{}, false},
		{"workload without namespace", protocol.FlowScope{Workload: "api"}, false},
		{"pod without namespace", protocol.FlowScope{Pod: "api-1"}, false},
		{"namespace", protocol.FlowScope{Namespace: "shop"}, true},
		{"workload", protocol.FlowScope{Namespace: "shop", Workload: "api"}, true},
		{"pod", protocol.FlowScope{Namespace: "shop", Pod: "api-1"}, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := requestFilters(c.scope, protocol.FlowFilter{})
			if (err == nil) != c.ok {
				t.Fatalf("err=%v want ok=%v", err, c.ok)
			}
			if err != nil {
				var oe *ops.Error
				if !errors.As(err, &oe) || oe.Body.Code != "bad_request" {
					t.Fatalf("want bad_request, got %v", err)
				}
			}
		})
	}
	// and through stream.open: no relay is even contacted for an unscoped follow
	s := New(Config{Connect: func(context.Context, string) (*Link, error) {
		t.Fatal("relay contacted for an unscoped stream")
		return nil, nil
	}})
	_, err := s.OpenFlows(context.Background(), protocol.StreamOpenParams{Type: "flows", Flows: &protocol.FlowsStreamParams{Cluster: "c"}},
		func(func(context.Context, streams.Transport)) string { return "id" })
	if err == nil {
		t.Fatal("unscoped follow accepted")
	}
}

func TestRequestFiltersAreServerSide(t *testing.T) {
	fs, err := requestFilters(protocol.FlowScope{Namespace: "shop", Workload: "backend"},
		protocol.FlowFilter{Verdict: "dropped", DropReason: "POLICY_DENIED", Protocol: "TCP", HTTPStatus: "5+", Direction: "ingress"})
	if err != nil {
		t.Fatal(err)
	}
	if len(fs) != 2 {
		t.Fatalf("want a source and a destination entry, got %d", len(fs))
	}
	d := fs[1]
	if d.DestinationPod[0] != "shop/" || d.DestinationWorkload[0].Name != "backend" || d.Verdict[0] != pb.Verdict_DROPPED ||
		d.DropReasonDesc[0] != 133 || d.Protocol[0] != "tcp" || d.HttpStatusCode[0] != "5+" || d.TrafficDirection[0] != pb.TrafficDirection_INGRESS {
		t.Fatalf("destination entry %+v", d)
	}
	if _, err := requestFilters(protocol.FlowScope{Namespace: "shop"}, protocol.FlowFilter{Verdict: "bogus"}); err == nil {
		t.Fatal("bad verdict accepted")
	}
}

// ---- relay discovery --------------------------------------------------------------------------

func obj(kind, ns, name string, labels map[string]any, body map[string]any) unstructured.Unstructured {
	m := map[string]any{"kind": kind, "metadata": map[string]any{"name": name, "namespace": ns}}
	if labels != nil {
		m["metadata"].(map[string]any)["labels"] = labels
	}
	for k, v := range body {
		m[k] = v
	}
	return unstructured.Unstructured{Object: m}
}

func TestPickRelay(t *testing.T) {
	svc := obj("Service", "kube-system", "hubble-relay", nil, map[string]any{"spec": map[string]any{"ports": []any{map[string]any{"port": int64(80), "targetPort": int64(4245)}}}})
	other := obj("Service", "monitoring", "hubble-relay", nil, nil)
	lbl := map[string]any{"kubernetes.io/service-name": "hubble-relay"}
	slice := func(name string, ready *bool, pod string, port int64) unstructured.Unstructured {
		cond := map[string]any{}
		if ready != nil {
			cond["ready"] = *ready
		}
		return obj("EndpointSlice", "kube-system", name, lbl, map[string]any{
			"endpoints": []any{map[string]any{"conditions": cond, "targetRef": map[string]any{"kind": "Pod", "name": pod}}},
			"ports":     []any{map[string]any{"port": port}}})
	}
	no, yes := false, true
	cases := []struct {
		name    string
		svcs    []unstructured.Unstructured
		slices  []unstructured.Unstructured
		pod     string
		port    int
		wantErr string
	}{
		{"no service", nil, nil, "", 0, "none"},
		{"ready pod", []unstructured.Unstructured{svc}, []unstructured.Unstructured{slice("a", &yes, "relay-1", 4245)}, "relay-1", 4245, ""},
		{"ready unset counts as ready", []unstructured.Unstructured{svc}, []unstructured.Unstructured{slice("a", nil, "relay-2", 9000)}, "relay-2", 9000, ""},
		{"skips not-ready, takes the next", []unstructured.Unstructured{svc}, []unstructured.Unstructured{slice("a", &no, "relay-1", 4245), slice("b", &yes, "relay-3", 4245)}, "relay-3", 4245, ""},
		{"kube-system wins over another namespace", []unstructured.Unstructured{other, svc}, []unstructured.Unstructured{slice("a", &yes, "relay-1", 4245)}, "relay-1", 4245, ""},
		{"service without ready pod", []unstructured.Unstructured{svc}, []unstructured.Unstructured{slice("a", &no, "relay-1", 4245)}, "", 0, "notready"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			info, err := pickRelay(c.svcs, c.slices)
			switch c.wantErr {
			case "none":
				if !errors.Is(err, ErrNoRelay) {
					t.Fatalf("err=%v", err)
				}
			case "notready":
				var nr *NotReadyError
				if !errors.As(err, &nr) || nr.Info.Namespace != "kube-system" {
					t.Fatalf("err=%v", err)
				}
			default:
				if err != nil || info.Pod != c.pod || info.Port != c.port || info.Namespace != "kube-system" {
					t.Fatalf("info=%+v err=%v", info, err)
				}
			}
		})
	}
	_ = metav1.ListOptions{}
}

// ---- aggregation ------------------------------------------------------------------------------

func dropFlow(src, dst *pb.Endpoint, reason int32, at time.Time) *pb.Flow {
	return &pb.Flow{Verdict: pb.Verdict_DROPPED, DropReasonDesc: reason, Source: src, Destination: dst, Time: timestamppb.New(at),
		TrafficDirection: pb.TrafficDirection_INGRESS}
}

type staticPolicies []PolicySummary

func (p staticPolicies) Policies(ns string) []PolicySummary {
	var out []PolicySummary
	for _, s := range p {
		if s.Namespace == ns || s.Namespace == "" {
			out = append(out, s)
		}
	}
	return out
}

func TestDropAggregationAndPolicies(t *testing.T) {
	now := time.Now()
	a := newDropAgg()
	for i := 0; i < 7; i++ {
		a.add(dropFlow(epProbe, epBackend, 133, now.Add(-time.Duration(i)*time.Second)))
	}
	a.add(dropFlow(epProbe, epBackend, 181, now))
	a.add(dropFlow(epFrontend, epDB, 133, now))
	a.add(&pb.Flow{Verdict: pb.Verdict_FORWARDED, Source: epFrontend, Destination: epBackend})
	pol := staticPolicies{
		{Kind: "NetworkPolicy", Namespace: "shop", Name: "deny-all-ingress", Selector: map[string]string{"app": "backend"}, Types: []string{"Ingress"}},
		{Kind: "NetworkPolicy", Namespace: "shop", Name: "other-app", Selector: map[string]string{"app": "web"}, Types: []string{"Ingress"}},
		{Kind: "NetworkPolicy", Namespace: "shop", Name: "egress-only", Selector: map[string]string{"app": "backend"}, Types: []string{"Egress"}},
		{Kind: "CiliumClusterwideNetworkPolicy", Name: "cluster-default", Selector: nil},
	}
	gs := a.groups("c1", pol)
	if len(gs) != 3 {
		t.Fatalf("groups: %+v", gs)
	}
	top := gs[0]
	if top.Count != 7 || top.Src.Workload != "probe" || top.Dst.Workload != "backend" || top.Reason != "POLICY_DENIED" || top.LastSeen != now.UnixMilli()-0 && top.LastSeen > now.UnixMilli() {
		t.Fatalf("top group %+v", top)
	}
	names := map[string]bool{}
	for _, p := range top.Policies {
		names[p.Name] = true
	}
	if !names["deny-all-ingress"] || !names["cluster-default"] || names["other-app"] || names["egress-only"] {
		t.Fatalf("likely policies %+v", top.Policies)
	}
}

func TestCountersWindowExpiry(t *testing.T) {
	c := newCounters()
	base := int64(10 * 60_000)
	c.add("k", base, 4)
	c.add("k", base+2*60_000, 7)
	if n, first := c.sum("k", base+2*60_000, 5); n != 11 || first != base {
		t.Fatalf("in window: n=%d first=%d", n, first)
	}
	// 5 minutes later the first minute fell out of the window, 6 minutes later both did
	if n, _ := c.sum("k", base+5*60_000, 5); n != 7 {
		t.Fatalf("first minute should have expired: %d", n)
	}
	if n, _ := c.sum("k", base+7*60_000, 5); n != 0 {
		t.Fatalf("all expired: %d", n)
	}
	c.prune(base+20*60_000, 6)
	if len(c.keys()) != 0 {
		t.Fatalf("prune left %v", c.keys())
	}
}

func TestServiceMapAggregation(t *testing.T) {
	now := timestamppb.Now()
	httpResp := func(code uint32) *pb.Layer7 {
		return &pb.Layer7{Type: pb.L7FlowType_RESPONSE, Record: &pb.Layer7_Http{Http: &pb.HTTP{Code: code}}}
	}
	httpReq := &pb.Layer7{Type: pb.L7FlowType_REQUEST, Record: &pb.Layer7_Http{Http: &pb.HTTP{Method: "GET"}}}
	a := newMapAgg("shop")
	for i := 0; i < 5; i++ {
		a.add(&pb.Flow{Source: epFrontend, Destination: epBackend, L7: httpReq, Time: now})
	}
	a.add(&pb.Flow{Source: epFrontend, Destination: epBackend, L7: httpResp(200), Time: now})
	a.add(&pb.Flow{Source: epFrontend, Destination: epBackend, L7: httpResp(503), Time: now})
	a.add(dropFlow(epProbe, epBackend, 133, time.Now()))
	a.add(&pb.Flow{Source: epBackend, Destination: epDB}) // plain TCP: a non-reply flow is a request
	a.add(&pb.Flow{Source: epDB, Destination: epBackend, IsReply: wrapperspb.Bool(true)})
	a.add(&pb.Flow{Source: epDNS, Destination: epDNS}) // neither side in the namespace: ignored
	a.add(&pb.Flow{Source: world("world"), Destination: epFrontend})
	res := a.result("c1", 300, false)
	edge := map[string]protocol.ServiceMapEdge{}
	for _, e := range res.Edges {
		edge[e.From+">"+e.To] = e
	}
	if e := edge["shop/frontend>shop/backend"]; e.Requests != 5 || e.Errors != 1 || e.Drops != 0 {
		t.Fatalf("frontend->backend %+v", e)
	}
	if e := edge["shop/probe>shop/backend"]; e.Drops != 1 || e.Requests != 0 {
		t.Fatalf("probe->backend %+v", e)
	}
	if e := edge["shop/backend>shop/db"]; e.Requests != 1 {
		t.Fatalf("backend->db %+v", e)
	}
	if e := edge["shop/db>shop/backend"]; e.Requests != 0 {
		t.Fatalf("a reply is not a request: %+v", e)
	}
	if _, ok := edge["kube-system/coredns>kube-system/coredns"]; ok {
		t.Fatal("flow outside the namespace counted")
	}
	if e := edge["world>shop/frontend"]; e.Requests != 1 {
		t.Fatalf("world->frontend %+v", e)
	}
	var world *protocol.ServiceMapNode
	for i := range res.Nodes {
		if res.Nodes[i].ID == "world" {
			world = &res.Nodes[i]
		}
	}
	if world == nil || !world.External {
		t.Fatalf("nodes %+v", res.Nodes)
	}
}

// ---- problems ---------------------------------------------------------------------------------

func TestEvalDraftsThresholds(t *testing.T) {
	now := int64(100 * 60_000)
	c := newCounters()
	c.add("pd|shop/probe>shop/backend|POLICY_DENIED", now-3*60_000, 6)
	c.add("pd|shop/probe>shop/backend|POLICY_DENIED", now, 4)
	c.add("pd|shop/other>shop/backend|POLICY_DENIED", now, 9) // below the threshold
	c.add("l7|shop/backend", now, 25)
	c.add("dns|shop/frontend|NXDOMAIN", now, 30)
	ds := evalDrafts(c, now, maskDrops|maskL7|maskDNS)
	if got := ds[ClassPolicyDrops]; len(got) != 1 || got[0].Count != 10 || got[0].Subject.Name != "backend" || got[0].Since != now-3*60_000 {
		t.Fatalf("policy-drops %+v", got)
	}
	if len(ds[ClassL7Errors]) != 1 || len(ds[ClassDNSErrors]) != 1 {
		t.Fatalf("l7/dns %+v", ds)
	}
	// a class that is not subscribed produces nothing even if counters exist
	if ds := evalDrafts(c, now, maskDrops); len(ds[ClassL7Errors]) != 0 || len(ds[ClassDNSErrors]) != 0 {
		t.Fatal("disabled class produced findings")
	}
	// the window slid past every drop
	if ds := evalDrafts(c, now+10*60_000, maskDrops); len(ds[ClassPolicyDrops]) != 0 {
		t.Fatalf("expired drops still raise: %+v", ds[ClassPolicyDrops])
	}
}

func TestMaskFiltersAreNarrow(t *testing.T) {
	if fs := maskDrops.filters(); len(fs) != 1 || fs[0].Verdict[0] != pb.Verdict_DROPPED {
		t.Fatalf("drops filter %+v", fs)
	}
	fs := (maskDrops | maskL7).filters()
	if len(fs) != 2 || fs[1].HttpStatusCode[0] != "5+" || fs[1].Protocol[0] != "http" {
		t.Fatalf("l7 filter %+v", fs)
	}
	if len((classMask(0)).filters()) != 0 {
		t.Fatal("empty mask must request nothing")
	}
}

func problemFixture(t *testing.T) (*ptest.Harness, *Service, *atomic.Int32) {
	t.Helper()
	h := ptest.New(t)
	clients := new(atomic.Int32)
	clients.Store(1)
	r := NewFakeRelay()
	s := testService(t, r, func(c *Config) {
		c.Problems = h.Hub
		c.Clusters = func() []string { return []string{"c1"} }
		c.ClientCount = func() int { return int(clients.Load()) }
		c.MonitorEvery = 30 * time.Millisecond
	})
	return h, s, clients
}

func enable(h *ptest.Harness, class string, on bool) {
	h.Set.ProblemPrefs = map[string]any{"classes": map[string]any{class: on}}
	h.Hub.SettingsChanged(h.Set)
}

func TestPolicyDropsClassEndToEnd(t *testing.T) {
	h, s, _ := problemFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s.StartProblems(ctx)
	time.Sleep(150 * time.Millisecond)
	if s.Subscribed("c1") {
		t.Fatal("subscribed while the class is off (off by default)")
	}
	if n := len(h.ByClass(ClassPolicyDrops)); n != 0 {
		t.Fatalf("problem while off: %d", n)
	}
	enable(h, ClassPolicyDrops, true)
	waitFor(t, "network.policy-drops", 5*time.Second, func() bool { return len(h.ByClass(ClassPolicyDrops)) == 1 })
	p := h.ByClass(ClassPolicyDrops)[0]
	if p.Subject.Name != "backend" || p.Subject.NS != "shop" || p.Count < policyDropsMin || p.Source != "hubble" || p.Family != "flows" {
		t.Fatalf("problem %+v", p)
	}
	if !s.Subscribed("c1") {
		t.Fatal("no subscription while enabled")
	}
	// disabling the class stops the subscription and withdraws the finding after the hold
	enable(h, ClassPolicyDrops, false)
	waitFor(t, "subscription stopped", 3*time.Second, func() bool { return !s.Subscribed("c1") })
	h.Advance(31 * time.Second) // the finding is withdrawn; the engine holds it for the 30 s hysteresis
	if n := len(h.ByClass(ClassPolicyDrops)); n != 0 {
		t.Fatalf("disabled class keeps its problem: %d", n)
	}
}

func TestSubscriptionNeedsAClient(t *testing.T) {
	h, s, clients := problemFixture(t)
	clients.Store(0)
	enable(h, ClassPolicyDrops, true)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s.StartProblems(ctx)
	time.Sleep(200 * time.Millisecond)
	if s.Subscribed("c1") || len(h.ByClass(ClassPolicyDrops)) != 0 {
		t.Fatal("subscription without a connected client")
	}
	clients.Store(1)
	waitFor(t, "subscription once a client connects", 3*time.Second, func() bool { return s.Subscribed("c1") })
	clients.Store(0)
	waitFor(t, "subscription stops with the last client", 3*time.Second, func() bool { return !s.Subscribed("c1") })
}

func TestRelayDownFinding(t *testing.T) {
	h := ptest.New(t)
	s := New(Config{Problems: h.Hub})
	s.relayDown("c1", &protocol.HubbleRelayInfo{Namespace: "kube-system"}, errors.New("no ready pod"))
	if ps := h.ByClass(ClassRelayDown); len(ps) != 1 || ps[0].Subject.Name != "hubble-relay" || ps[0].Message != "no ready pod" {
		t.Fatalf("relay-down %+v", ps)
	}
	s.relayUp("c1")
	h.Advance(31 * time.Second) // hysteresis hold
	if n := len(h.ByClass(ClassRelayDown)); n != 0 {
		t.Fatalf("relay-down not withdrawn: %d", n)
	}
}

// ---- flows stream -----------------------------------------------------------------------------

// lockedClock is a clock tests advance by hand, safe for the stream's goroutines.
type lockedClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *lockedClock) now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *lockedClock) advance(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

// shownTotals folds the frames into (flows delivered, latest reported rate).
func shownTotals(frames []protocol.FlowsFrame) (shown int, last *protocol.FlowRate) {
	for _, f := range frames {
		shown += len(f.Flows)
		if f.Rate != nil {
			last = f.Rate
		}
	}
	return
}

// The sampler runs on an injected clock, so the cap does not depend on how fast the machine is: with the clock
// held still the bucket holds MaxFlowsPerSec/10 tokens and no more; advancing it a second refills it. The
// relay keeps offering flows in real time and the test polls for the outcome instead of sleeping.
func TestFlowsStreamRateCapAndScope(t *testing.T) {
	r := NewFakeRelay()
	r.Rate = 3000
	clk := &lockedClock{t: time.Date(2026, 11, 17, 12, 0, 0, 0, time.UTC)}
	s := testService(t, r, func(c *Config) { c.Now = clk.now })
	const burst = 200 / 10 // the sampler's capacity at the default 200 flows/s
	var run func(context.Context, streams.Transport)
	res, err := s.OpenFlows(context.Background(), protocol.StreamOpenParams{Type: "flows", Flows: &protocol.FlowsStreamParams{
		Cluster: "c1", Scope: protocol.FlowScope{Namespace: "shop"}}}, func(f func(context.Context, streams.Transport)) string { run = f; return "sid" })
	if err != nil || res.StreamID != "sid" || res.UsedContext != "c1" {
		t.Fatalf("open: %+v %v", res, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	tr := newMemTransport()
	done := make(chan struct{})
	go func() { run(ctx, tr); close(done) }()
	waitFor := func(what string, ok func(shown int, last *protocol.FlowRate) bool) {
		t.Helper()
		deadline := time.Now().Add(20 * time.Second)
		for {
			if shown, last := shownTotals(tr.snapshot()); ok(shown, last) {
				return
			}
			if time.Now().After(deadline) {
				shown, last := shownTotals(tr.snapshot())
				t.Fatalf("timed out waiting for %s: shown=%d rate=%+v", what, shown, last)
			}
			time.Sleep(5 * time.Millisecond)
		}
	}
	// many more flows are offered than the cap lets through
	waitFor("the offered rate", func(shown int, last *protocol.FlowRate) bool { return last != nil && last.Total >= 5*burst })
	waitFor("the burst", func(shown int, _ *protocol.FlowRate) bool { return shown >= burst })
	time.Sleep(50 * time.Millisecond) // anything over the cap would arrive in the next batches
	shown, last := shownTotals(tr.snapshot())
	if shown != burst || !last.Sampled || last.Shown != burst || last.Total < 5*burst {
		t.Fatalf("with the clock held, shown=%d (want %d) rate=%+v", shown, burst, last)
	}
	// a second later the bucket is full again: exactly one more burst gets through
	clk.advance(time.Second)
	waitFor("the refill", func(shown int, _ *protocol.FlowRate) bool { return shown >= 2*burst })
	time.Sleep(50 * time.Millisecond)
	if shown, _ := shownTotals(tr.snapshot()); shown != 2*burst {
		t.Fatalf("after one refill shown=%d, want %d", shown, 2*burst)
	}
	cancel()
	<-done
	frames := tr.snapshot()
	if len(frames) < 3 || frames[0].Ready == nil || !frames[len(frames)-1].End {
		t.Fatalf("frames: first=%+v n=%d", frames[0], len(frames))
	}
	for _, f := range frames {
		for _, fl := range f.Flows {
			if fl.Src.Namespace != "shop" && fl.Dst.Namespace != "shop" {
				t.Fatalf("flow outside the scope reached the client: %+v", fl)
			}
		}
	}
}

func TestFlowsStreamPausedCancels(t *testing.T) {
	r := NewFakeRelay()
	clk := &fakeClock{t: time.Now()}
	var mu sync.Mutex
	s := testService(t, r, func(c *Config) {
		c.Now = func() time.Time { mu.Lock(); defer mu.Unlock(); return clk.t }
	})
	var run func(context.Context, streams.Transport)
	_, err := s.OpenFlows(context.Background(), protocol.StreamOpenParams{Type: "flows", Flows: &protocol.FlowsStreamParams{
		Cluster: "c1", Scope: protocol.FlowScope{Namespace: "shop"}}}, func(f func(context.Context, streams.Transport)) string { run = f; return "sid" })
	if err != nil {
		t.Fatal(err)
	}
	tr := newMemTransport()
	done := make(chan struct{})
	go func() { run(context.Background(), tr); close(done) }()
	tr.in <- []byte(`{"pause":true}`)
	time.Sleep(200 * time.Millisecond)
	mu.Lock()
	clk.advance(11 * time.Second)
	mu.Unlock()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("paused stream was not cancelled")
	}
	frames := tr.snapshot()
	if last := frames[len(frames)-1]; !last.End || last.Error == "" {
		t.Fatalf("last frame %+v", last)
	}
}

// ---- requests ---------------------------------------------------------------------------------

func TestStatusServiceMapAndDrops(t *testing.T) {
	r := NewFakeRelay()
	s := testService(t, r, func(c *Config) {
		c.Clusters = func() []string { return []string{"c1", "none"} }
		c.Policies = func(context.Context, string) PolicyLister {
			return staticPolicies{{Kind: "NetworkPolicy", Namespace: "shop", Name: "deny-all-ingress", Selector: map[string]string{"app": "backend"}, Types: []string{"Ingress"}}}
		}
		inner := c.Connect
		c.Connect = func(ctx context.Context, cl string) (*Link, error) {
			if cl == "none" {
				return nil, ops.NewError("not_found", "%s", ErrNoRelay.Error())
			}
			return inner(ctx, cl)
		}
	})
	ctx := context.Background()
	st := s.Status(ctx, "c1")
	if !st.Available || st.MaxFlows != 12288 || st.ConnectedNodes != 3 || len(st.Nodes) != 3 || st.Relay == nil || st.Relay.Service != "hubble-relay" {
		t.Fatalf("status %+v", st)
	}
	if bad := s.Status(ctx, "none"); bad.Available || bad.Error == "" {
		t.Fatalf("status without relay %+v", bad)
	}
	sm, err := s.ServiceMap(ctx, protocol.HubbleServiceMapParams{Cluster: "c1", Namespace: "shop", WindowSec: 60})
	if err != nil {
		t.Fatal(err)
	}
	found := map[string]protocol.ServiceMapEdge{}
	for _, e := range sm.Edges {
		found[e.From+">"+e.To] = e
	}
	if e := found["shop/frontend>shop/backend"]; e.Requests == 0 || e.Errors == 0 {
		t.Fatalf("frontend->backend %+v (edges %+v)", e, sm.Edges)
	}
	if e := found["shop/probe>shop/backend"]; e.Drops == 0 {
		t.Fatalf("probe->backend %+v", sm.Edges)
	}
	if _, err := s.ServiceMap(ctx, protocol.HubbleServiceMapParams{Cluster: "c1"}); err == nil {
		t.Fatal("service map without namespace accepted")
	}
	d := s.Drops(ctx, protocol.HubbleDropsParams{WindowSec: 60})
	if len(d.Groups) == 0 || len(d.Errors) != 1 || d.Errors[0].Cluster != "none" {
		t.Fatalf("drops %+v", d)
	}
	g := d.Groups[0]
	if g.Cluster != "c1" || g.Src.Workload != "probe" || g.Dst.Workload != "backend" || g.Reason != "POLICY_DENIED" || g.Count == 0 || g.LastSeen == 0 ||
		len(g.Policies) != 1 || g.Policies[0].Name != "deny-all-ingress" {
		t.Fatalf("group %+v", g)
	}
}

func TestFlowConversion(t *testing.T) {
	f := toFlow(gen(3, time.UnixMilli(1_700_000_000_000))) // frontend -> backend HTTP response
	if f.Protocol != "HTTP" || f.L7 != "GET /api/items 200" || f.Src.Workload != "frontend" || f.Dst.Workload != "backend" || !f.Reply || f.Time != 1_700_000_000_000 {
		t.Fatalf("http flow %+v", f)
	}
	d := toFlow(gen(8, time.Now()))
	if d.Verdict != "DROPPED" || d.DropReason != "POLICY_DENIED" || d.Protocol != "TCP" || d.Dst.Port != 8080 {
		t.Fatalf("drop flow %+v", d)
	}
	n := toFlow(gen(6+36, time.Now()))
	if n.Protocol != "DNS" || n.L7 != "DNS A backend.shop.svc.cluster.local NXDOMAIN" {
		t.Fatalf("dns flow %+v", n)
	}
	if w := toFlow(gen(7, time.Now())); w.Src.Workload != "world" || w.Src.Namespace != "" {
		t.Fatalf("world flow %+v", w)
	}
}

// A link rebuilt while older holders still use the previous one must still idle out when the last holder (of
// either generation) lets go.
func TestLinkIdlesOutWhenOlderGenerationReleasesLast(t *testing.T) {
	type gen struct{ dead, closed atomic.Bool }
	var mu sync.Mutex
	var gens []*gen
	s := New(Config{LinkIdle: 20 * time.Millisecond, Connect: func(context.Context, string) (*Link, error) {
		g := &gen{}
		mu.Lock()
		gens = append(gens, g)
		mu.Unlock()
		return &Link{Close: func() { g.closed.Store(true) }, Dead: g.dead.Load}, nil
	}})
	t.Cleanup(s.Close)
	ctx := context.Background()
	_, relA, err := s.acquire(ctx, "c")
	if err != nil {
		t.Fatal(err)
	}
	_, relB, _ := s.acquire(ctx, "c")
	mu.Lock()
	gens[0].dead.Store(true) // the forward died under A and B
	mu.Unlock()
	_, relC, err := s.acquire(ctx, "c") // rebuilds
	if err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	if len(gens) != 2 {
		t.Fatalf("want a rebuilt link, got %d connects", len(gens))
	}
	g2 := gens[1]
	mu.Unlock()
	relC()
	relA()
	relB() // the last holder belongs to the older generation
	deadline := time.Now().Add(2 * time.Second)
	for !g2.closed.Load() {
		if time.Now().After(deadline) {
			t.Fatal("the rebuilt link never idled out: leaked")
		}
		time.Sleep(10 * time.Millisecond)
	}
}
