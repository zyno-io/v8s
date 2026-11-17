package hubble

import (
	"context"
	"fmt"
	"net"
	"strings"
	"sync/atomic"
	"time"

	"github.com/zyno-io/v8s/agent/internal/hubble/pb"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/types/known/timestamppb"
	"google.golang.org/protobuf/types/known/wrapperspb"
)

// FakeRelay is an in-process hubble-relay serving synthetic flows for fake mode and tests: web traffic in a
// "shop" namespace (frontend -> backend over HTTP, backend -> db, DNS lookups with the odd NXDOMAIN) plus a
// probe pod denied by a NetworkPolicy (POLICY_DENIED drops). It honours the server-side filters a client sends.
type FakeRelay struct {
	pb.UnimplementedObserverServer
	// Rate is the flows per second of a follow stream (default 60).
	Rate int
	// Now is the clock (default time.Now).
	Now func() time.Time

	seq     atomic.Int64
	started time.Time
}

// NewFakeRelay builds a relay.
func NewFakeRelay() *FakeRelay { return &FakeRelay{Rate: 60, Now: time.Now, started: time.Now()} }

// Serve registers the relay on a new gRPC server and serves ln until the returned stop func is called.
func (r *FakeRelay) Serve(ln net.Listener) (stop func(), done <-chan struct{}) {
	srv := grpc.NewServer()
	pb.RegisterObserverServer(srv, r)
	d := make(chan struct{})
	go func() { _ = srv.Serve(ln); close(d) }()
	return srv.Stop, d
}

func ep(ns, workload, pod string, labels ...string) *pb.Endpoint {
	e := &pb.Endpoint{Namespace: ns, PodName: pod, Labels: labels}
	if workload != "" {
		e.Workloads = []*pb.Workload{{Name: workload, Kind: "Deployment"}}
	}
	return e
}

func world(label string) *pb.Endpoint { return &pb.Endpoint{Labels: []string{"reserved:" + label}} }

var (
	epFrontend = ep("shop", "frontend", "frontend-6d5f9c7b8-x2k4p", "k8s:app=frontend", "k8s:io.kubernetes.pod.namespace=shop")
	epBackend  = ep("shop", "backend", "backend-7c8d9f6b5-q9w8e", "k8s:app=backend", "k8s:io.kubernetes.pod.namespace=shop")
	epDB       = ep("shop", "db", "db-0", "k8s:app=db", "k8s:io.kubernetes.pod.namespace=shop")
	epProbe    = ep("shop", "probe", "probe-5b7c8d9f4-m3n2b", "k8s:app=probe", "k8s:io.kubernetes.pod.namespace=shop")
	epDNS      = ep("kube-system", "coredns", "coredns-66bc5c9577-abcde", "k8s:k8s-app=kube-dns", "k8s:io.kubernetes.pod.namespace=kube-system")
)

func tcp(sport, dport uint32) *pb.Layer4 {
	return &pb.Layer4{Protocol: &pb.Layer4_TCP{TCP: &pb.TCP{SourcePort: sport, DestinationPort: dport}}}
}

func udp(sport, dport uint32) *pb.Layer4 {
	return &pb.Layer4{Protocol: &pb.Layer4_UDP{UDP: &pb.UDP{SourcePort: sport, DestinationPort: dport}}}
}

// gen builds the i-th synthetic flow at time t (deterministic in i).
func gen(i int64, t time.Time) *pb.Flow {
	f := &pb.Flow{Time: timestamppb.New(t), NodeName: fmt.Sprintf("node-%d", i%3), Verdict: pb.Verdict_FORWARDED, TrafficDirection: pb.TrafficDirection_INGRESS,
		IsReply: wrapperspb.Bool(false)}
	switch i % 12 {
	case 0, 1, 2, 3: // frontend -> backend HTTP request/response
		f.Source, f.Destination, f.L4 = epFrontend, epBackend, tcp(uint32(40000+i%1000), 8080)
		f.Ip = &pb.IP{Source: "10.0.1.11", Destination: "10.0.1.12"}
		code := uint32(200)
		if i%48 == 1 {
			code = 503
		}
		if i%2 == 0 {
			f.L7 = &pb.Layer7{Type: pb.L7FlowType_REQUEST, Record: &pb.Layer7_Http{Http: &pb.HTTP{Method: "GET", Url: "/api/items", Protocol: "HTTP/1.1"}}}
		} else {
			f.IsReply = wrapperspb.Bool(true)
			f.L7 = &pb.Layer7{Type: pb.L7FlowType_RESPONSE, LatencyNs: 1200000, Record: &pb.Layer7_Http{Http: &pb.HTTP{Method: "GET", Url: "/api/items", Code: code, Protocol: "HTTP/1.1"}}}
		}
	case 4, 5: // backend -> db
		f.Source, f.Destination, f.L4 = epBackend, epDB, tcp(uint32(50000+i%1000), 5432)
		f.Ip = &pb.IP{Source: "10.0.1.12", Destination: "10.0.1.13"}
		f.TrafficDirection = pb.TrafficDirection_EGRESS
	case 6: // DNS
		f.Source, f.Destination, f.L4 = epFrontend, epDNS, udp(uint32(30000+i%1000), 53)
		f.Ip = &pb.IP{Source: "10.0.1.11", Destination: "10.0.0.53"}
		rc := uint32(0)
		if i%36 == 6 {
			rc = 3
		}
		f.IsReply = wrapperspb.Bool(true)
		f.L7 = &pb.Layer7{Type: pb.L7FlowType_RESPONSE, Record: &pb.Layer7_Dns{Dns: &pb.DNS{Query: "backend.shop.svc.cluster.local.", Qtypes: []string{"A"}, Rcode: rc}}}
	case 7: // internet -> frontend
		f.Source, f.Destination, f.L4 = world("world"), epFrontend, tcp(uint32(52000+i%1000), 443)
		f.Ip = &pb.IP{Source: "203.0.113.7", Destination: "10.0.1.11"}
	case 8, 9: // probe -> backend, denied by policy
		f.Source, f.Destination, f.L4 = epProbe, epBackend, tcp(uint32(41000+i%1000), 8080)
		f.Ip = &pb.IP{Source: "10.0.1.14", Destination: "10.0.1.12"}
		f.Verdict, f.DropReasonDesc = pb.Verdict_DROPPED, reasonPolicyDenied
	default: // frontend -> backend plain TCP
		f.Source, f.Destination, f.L4 = epFrontend, epBackend, tcp(uint32(42000+i%1000), 8080)
		f.Ip = &pb.IP{Source: "10.0.1.11", Destination: "10.0.1.12"}
	}
	return f
}

func podMatch(prefixes []string, e *pb.Endpoint) bool {
	full := e.Namespace + "/" + e.PodName
	for _, p := range prefixes {
		if strings.HasPrefix(full, p) {
			return true
		}
	}
	return false
}

func wlMatch(ws []*pb.Workload, e *pb.Endpoint) bool {
	for _, w := range ws {
		for _, ew := range e.Workloads {
			if w.Name == ew.Name && (w.Kind == "" || w.Kind == ew.Kind) {
				return true
			}
		}
	}
	return false
}

func flowProtocol(f *pb.Flow) []string {
	var out []string
	switch f.GetL4().GetProtocol().(type) {
	case *pb.Layer4_TCP:
		out = append(out, "tcp")
	case *pb.Layer4_UDP:
		out = append(out, "udp")
	}
	if f.GetL7().GetHttp() != nil {
		out = append(out, "http")
	}
	if f.GetL7().GetDns() != nil {
		out = append(out, "dns")
	}
	return out
}

func anyOf(want []string, have []string) bool {
	for _, w := range want {
		for _, h := range have {
			if w == h {
				return true
			}
		}
	}
	return false
}

func statusMatch(patterns []string, f *pb.Flow) bool {
	h := f.GetL7().GetHttp()
	if h == nil || h.Code == 0 {
		return false
	}
	code := fmt.Sprint(h.Code)
	for _, p := range patterns {
		if p == code || (strings.HasSuffix(p, "+") && strings.HasPrefix(code, strings.TrimSuffix(p, "+"))) {
			return true
		}
	}
	return false
}

// matchesFilter applies one FlowFilter (all set fields must match).
func matchesFilter(ff *pb.FlowFilter, f *pb.Flow) bool {
	if len(ff.SourcePod) > 0 && !podMatch(ff.SourcePod, f.Source) {
		return false
	}
	if len(ff.DestinationPod) > 0 && !podMatch(ff.DestinationPod, f.Destination) {
		return false
	}
	if len(ff.SourceWorkload) > 0 && !wlMatch(ff.SourceWorkload, f.Source) {
		return false
	}
	if len(ff.DestinationWorkload) > 0 && !wlMatch(ff.DestinationWorkload, f.Destination) {
		return false
	}
	if len(ff.Verdict) > 0 {
		ok := false
		for _, v := range ff.Verdict {
			ok = ok || v == f.Verdict
		}
		if !ok {
			return false
		}
	}
	if len(ff.DropReasonDesc) > 0 {
		ok := false
		for _, d := range ff.DropReasonDesc {
			ok = ok || d == f.DropReasonDesc
		}
		if !ok {
			return false
		}
	}
	if len(ff.Protocol) > 0 && !anyOf(ff.Protocol, flowProtocol(f)) {
		return false
	}
	if len(ff.HttpStatusCode) > 0 && !statusMatch(ff.HttpStatusCode, f) {
		return false
	}
	if len(ff.DnsQuery) > 0 {
		q := f.GetL7().GetDns().GetQuery()
		ok := false
		for _, w := range ff.DnsQuery {
			ok = ok || strings.Contains(q, w)
		}
		if !ok {
			return false
		}
	}
	if len(ff.TrafficDirection) > 0 {
		ok := false
		for _, d := range ff.TrafficDirection {
			ok = ok || d == f.TrafficDirection
		}
		if !ok {
			return false
		}
	}
	return true
}

func matchesAny(wl, bl []*pb.FlowFilter, f *pb.Flow) bool {
	for _, b := range bl {
		if matchesFilter(b, f) {
			return false
		}
	}
	if len(wl) == 0 {
		return true
	}
	for _, w := range wl {
		if matchesFilter(w, f) {
			return true
		}
	}
	return false
}

func (r *FakeRelay) rate() int {
	if r.Rate <= 0 {
		return 60
	}
	return r.Rate
}

func (r *FakeRelay) now() time.Time {
	if r.Now != nil {
		return r.Now()
	}
	return time.Now()
}

func send(st pb.Observer_GetFlowsServer, f *pb.Flow) error {
	return st.Send(&pb.GetFlowsResponse{ResponseTypes: &pb.GetFlowsResponse_Flow{Flow: f}, NodeName: f.NodeName, Time: f.Time})
}

// GetFlows implements the Observer service: history for since/until or number, then (follow) live flows.
func (r *FakeRelay) GetFlows(req *pb.GetFlowsRequest, st pb.Observer_GetFlowsServer) error {
	now := r.now()
	const maxHistory = 5000
	switch {
	case req.Since != nil:
		from := req.Since.AsTime()
		to := now
		if req.Until != nil {
			to = req.Until.AsTime()
		}
		n := int(to.Sub(from).Seconds()) * r.rate()
		n = max(1, min(n, maxHistory))
		step := to.Sub(from) / time.Duration(n)
		for i := 0; i < n; i++ {
			f := gen(r.seq.Add(1), from.Add(time.Duration(i)*step))
			if matchesAny(req.Whitelist, req.Blacklist, f) {
				if err := send(st, f); err != nil {
					return err
				}
			}
		}
	case req.Number > 0:
		n := int(min(req.Number, maxHistory))
		for i := 0; i < n; i++ {
			f := gen(r.seq.Add(1), now.Add(-time.Duration(n-i)*time.Second/time.Duration(r.rate())))
			if matchesAny(req.Whitelist, req.Blacklist, f) {
				if err := send(st, f); err != nil {
					return err
				}
			}
		}
	}
	if !req.Follow {
		return nil
	}
	tick := time.NewTicker(50 * time.Millisecond)
	defer tick.Stop()
	last := time.Now()
	var carry float64
	for {
		select {
		case <-st.Context().Done():
			return nil
		case <-tick.C:
		}
		cur := time.Now()
		carry += cur.Sub(last).Seconds() * float64(r.rate())
		last = cur
		for ; carry >= 1; carry-- {
			f := gen(r.seq.Add(1), r.now())
			if matchesAny(req.Whitelist, req.Blacklist, f) {
				if err := send(st, f); err != nil {
					return err
				}
			}
		}
	}
}

// ServerStatus implements the Observer service.
func (r *FakeRelay) ServerStatus(context.Context, *pb.ServerStatusRequest) (*pb.ServerStatusResponse, error) {
	return &pb.ServerStatusResponse{NumFlows: 8190, MaxFlows: 12288, SeenFlows: uint64(r.seq.Load()), UptimeNs: uint64(time.Since(r.started)),
		NumConnectedNodes: wrapperspb.UInt32(3), NumUnavailableNodes: wrapperspb.UInt32(0), Version: "v1.17.1-fake", FlowsRate: float64(r.rate())}, nil
}

// GetNodes implements the Observer service.
func (r *FakeRelay) GetNodes(context.Context, *pb.GetNodesRequest) (*pb.GetNodesResponse, error) {
	return &pb.GetNodesResponse{Nodes: []*pb.Node{{Name: "node-0", Version: "v1.17.1", State: 1}, {Name: "node-1", Version: "v1.17.1", State: 1}, {Name: "node-2", Version: "v1.17.1", State: 1}}}, nil
}
