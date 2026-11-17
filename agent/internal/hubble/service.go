package hubble

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"sort"
	"sync"
	"time"

	"github.com/zyno-io/v8s/agent/internal/hubble/pb"
	"github.com/zyno-io/v8s/agent/internal/kube"
	"github.com/zyno-io/v8s/agent/internal/ops"
	"github.com/zyno-io/v8s/agent/internal/problems"
	"github.com/zyno-io/v8s/agent/internal/rpc"
	"github.com/zyno-io/v8s/agent/internal/streams"
	"github.com/zyno-io/v8s/agent/pkg/protocol"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// Tunables (docs/performance.md §8).
const (
	// MaxFlowsPerSec is the rate cap of a flows stream to the client.
	MaxFlowsPerSec = 200
	// BatchEvery is how often a flows stream sends a batch.
	BatchEvery = 100 * time.Millisecond
	// PauseGrace is how long a paused flows stream lives before it is cancelled.
	PauseGrace = 10 * time.Second

	defaultWindow = 300
	maxWindow     = 3600
	maxReadFlows  = 100000
	readTimeout   = 10 * time.Second
)

// Router routes the port-forward to the relay (ops.Service: read context when allowed, else the linked write context).
type Router interface {
	Route(ctx context.Context, cluster, verb, gvr, sub, ns, name string) (*ops.Routed, error)
}

// Config wires the module to the rest of the agent.
type Config struct {
	// Conn returns the connection of a context id (read context id == cluster id).
	Conn func(contextID string) (kube.Conn, error)
	// Router, when set, routes the port-forward (needs create pods/portforward); default: the read context.
	Router Router
	// Clusters lists the selected clusters (hubble.drops without an explicit list, the problem monitor).
	Clusters func() []string
	// Problems receives the network problem classes' findings; nil disables them.
	Problems *problems.Hub
	// ClientCount reports how many UI clients are connected (the continuous subscriptions run only while > 0).
	ClientCount func() int
	// Connect replaces relay discovery and the port-forward (tests, the in-process fake relay).
	Connect func(ctx context.Context, cluster string) (*Link, error)
	// Policies replaces the policy lookup of the drops view (tests).
	Policies func(ctx context.Context, cluster string) PolicyLister
	Now      func() time.Time
	Log      *slog.Logger
	// LinkIdle is how long an unused relay link stays open (default 30 s).
	LinkIdle time.Duration
	// MaxFlowsPerSec overrides the stream cap (default 200).
	MaxFlowsPerSec int
	// MonitorEvery is the problem monitor's evaluation interval (default 10 s).
	MonitorEvery time.Duration
}

// Service is the Hubble module.
type Service struct {
	cfg Config
	log *slog.Logger

	mu    sync.Mutex
	links map[string]*linkEntry
	mon   *monitor
}

// New builds the service.
func New(cfg Config) *Service {
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.Log == nil {
		cfg.Log = slog.New(slog.DiscardHandler)
	}
	if cfg.LinkIdle == 0 {
		cfg.LinkIdle = linkIdle
	}
	if cfg.MaxFlowsPerSec == 0 {
		cfg.MaxFlowsPerSec = MaxFlowsPerSec
	}
	if cfg.MonitorEvery == 0 {
		cfg.MonitorEvery = 10 * time.Second
	}
	return &Service{cfg: cfg, log: cfg.Log, links: map[string]*linkEntry{}}
}

// Handlers returns the request methods this module serves.
func (s *Service) Handlers() map[string]rpc.Handler {
	return map[string]rpc.Handler{
		"hubble.status": func(ctx context.Context, decode func(any) error) (any, error) {
			var p protocol.HubbleStatusParams
			if err := decode(&p); err != nil || p.Cluster == "" {
				return nil, ops.NewError("bad_request", "cluster is required")
			}
			return s.Status(ctx, p.Cluster), nil
		},
		"hubble.serviceMap": func(ctx context.Context, decode func(any) error) (any, error) {
			var p protocol.HubbleServiceMapParams
			if err := decode(&p); err != nil {
				return nil, ops.NewError("bad_request", "%s", err.Error())
			}
			return s.ServiceMap(ctx, p)
		},
		"hubble.drops": func(ctx context.Context, decode func(any) error) (any, error) {
			var p protocol.HubbleDropsParams
			_ = decode(&p) // no params: every selected cluster
			return s.Drops(ctx, p), nil
		},
	}
}

// RegisterStreams adds the "flows" stream type to stream.open.
func (s *Service) RegisterStreams(m *streams.Manager) { m.Register("flows", s.OpenFlows) }

func window(sec int) int {
	switch {
	case sec <= 0:
		return defaultWindow
	case sec > maxWindow:
		return maxWindow
	}
	return sec
}

func errMessage(err error) string {
	var oe *ops.Error
	if errors.As(err, &oe) {
		return oe.Body.Message
	}
	return err.Error()
}

// ---- status -----------------------------------------------------------------------------

// Status answers hubble.status.
func (s *Service) Status(ctx context.Context, cluster string) protocol.HubbleStatus {
	res := protocol.HubbleStatus{Cluster: cluster, Nodes: []protocol.HubbleNode{}}
	l, rel, err := s.acquire(ctx, cluster)
	if err != nil {
		res.Error = errMessage(err)
		return res
	}
	defer rel()
	info := l.Info
	res.Relay = &info
	cctx, cancel := context.WithTimeout(ctx, readTimeout)
	defer cancel()
	st, err := l.Client.ServerStatus(cctx, &pb.ServerStatusRequest{})
	if err != nil {
		s.invalidate(cluster, l)
		res.Error = "hubble-relay did not answer: " + err.Error()
		return res
	}
	res.Available = true
	res.Version = st.Version
	res.NumFlows, res.MaxFlows, res.SeenFlows = int(st.NumFlows), int(st.MaxFlows), int(st.SeenFlows)
	res.FlowsPerSec = st.FlowsRate
	res.UptimeSec = int(st.UptimeNs / 1e9)
	res.ConnectedNodes = int(st.GetNumConnectedNodes().GetValue())
	res.UnavailableNodes = int(st.GetNumUnavailableNodes().GetValue())
	if ns, err := l.Client.GetNodes(cctx, &pb.GetNodesRequest{}); err == nil {
		for _, n := range ns.Nodes {
			state := "unknown"
			switch n.State {
			case 1:
				state = "connected"
			case 2:
				state = "unavailable"
			}
			res.Nodes = append(res.Nodes, protocol.HubbleNode{Name: n.Name, Version: n.Version, State: state})
		}
	}
	return res
}

// ---- bounded reads (service map, drops) -------------------------------------------------

// readFlows reads a non-following GetFlows over [now-window, now] with a whitelist, calling fn per flow, until the
// relay ends the stream, maxReadFlows flows were read or readTimeout passed (truncated).
func (s *Service) readFlows(ctx context.Context, l *Link, filters []*pb.FlowFilter, windowSec int, fn func(*pb.Flow)) (truncated bool, err error) {
	rctx, cancel := context.WithTimeout(ctx, readTimeout)
	defer cancel()
	now := s.cfg.Now()
	st, err := l.Client.GetFlows(rctx, &pb.GetFlowsRequest{Whitelist: filters, Since: timestamppb.New(now.Add(-time.Duration(windowSec) * time.Second)), Until: timestamppb.New(now)})
	if err != nil {
		return false, err
	}
	n := 0
	for {
		resp, err := st.Recv()
		if errors.Is(err, io.EOF) {
			return false, nil
		}
		if err != nil {
			if rctx.Err() != nil && ctx.Err() == nil {
				return true, nil // the read budget ended: what was read is a lower bound
			}
			return n >= maxReadFlows, err
		}
		if f := resp.GetFlow(); f != nil {
			fn(f)
			if n++; n >= maxReadFlows {
				return true, nil
			}
		}
	}
}

// ServiceMap answers hubble.serviceMap.
func (s *Service) ServiceMap(ctx context.Context, p protocol.HubbleServiceMapParams) (protocol.HubbleServiceMap, error) {
	if p.Cluster == "" || p.Namespace == "" {
		return protocol.HubbleServiceMap{}, ops.NewError("bad_request", "cluster and namespace are required")
	}
	filters, err := requestFilters(protocol.FlowScope{Namespace: p.Namespace}, protocol.FlowFilter{})
	if err != nil {
		return protocol.HubbleServiceMap{}, err
	}
	l, rel, err := s.acquire(ctx, p.Cluster)
	if err != nil {
		return protocol.HubbleServiceMap{}, err
	}
	defer rel()
	w := window(p.WindowSec)
	agg := newMapAgg(p.Namespace)
	trunc, err := s.readFlows(ctx, l, filters, w, agg.add)
	if err != nil {
		s.invalidate(p.Cluster, l)
		return protocol.HubbleServiceMap{}, ops.NewError("unavailable", "hubble flows: %s", err.Error())
	}
	return agg.result(p.Cluster, w, trunc), nil
}

// Drops answers hubble.drops: dropped flows over the window across clusters, grouped by (source workload,
// destination workload, reason) with the policies likely responsible.
func (s *Service) Drops(ctx context.Context, p protocol.HubbleDropsParams) protocol.HubbleDrops {
	clusters := p.Clusters
	if len(clusters) == 0 && s.cfg.Clusters != nil {
		clusters = s.cfg.Clusters()
	}
	w := window(p.WindowSec)
	res := protocol.HubbleDrops{WindowSec: w, Groups: []protocol.DropGroup{}, Errors: []protocol.DropsError{}}
	var mu sync.Mutex
	var wg sync.WaitGroup
	sem := make(chan struct{}, 4)
	for _, cl := range clusters {
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			gs, err := s.clusterDrops(ctx, cl, w)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				res.Errors = append(res.Errors, protocol.DropsError{Cluster: cl, Error: errMessage(err)})
				return
			}
			res.Groups = append(res.Groups, gs...)
		}()
	}
	wg.Wait()
	sort.Slice(res.Groups, func(i, j int) bool {
		if res.Groups[i].Count != res.Groups[j].Count {
			return res.Groups[i].Count > res.Groups[j].Count
		}
		return res.Groups[i].Cluster < res.Groups[j].Cluster
	})
	sort.Slice(res.Errors, func(i, j int) bool { return res.Errors[i].Cluster < res.Errors[j].Cluster })
	return res
}

func (s *Service) clusterDrops(ctx context.Context, cluster string, w int) ([]protocol.DropGroup, error) {
	l, rel, err := s.acquire(ctx, cluster)
	if err != nil {
		return nil, err
	}
	defer rel()
	agg := newDropAgg()
	if _, err := s.readFlows(ctx, l, droppedFilter(), w, agg.add); err != nil {
		s.invalidate(cluster, l)
		return nil, ops.NewError("unavailable", "hubble flows: %s", err.Error())
	}
	var pol PolicyLister
	if len(agg.m) > 0 {
		pol = s.policyLister(ctx, cluster)
	}
	return agg.groups(cluster, pol), nil
}

// ---- flows stream ------------------------------------------------------------------------

// OpenFlows is the stream.open handler of type "flows" (streams.ExtraOpener).
func (s *Service) OpenFlows(ctx context.Context, p protocol.StreamOpenParams, claim func(run func(ctx context.Context, t streams.Transport)) string) (protocol.StreamOpenResult, error) {
	var res protocol.StreamOpenResult
	fp := p.Flows
	if fp == nil {
		return res, ops.NewError("bad_request", "flows needs params (cluster, scope, filter)")
	}
	cluster := fp.Cluster
	if cluster == "" {
		cluster = p.Cluster
	}
	if cluster == "" {
		return res, ops.NewError("bad_request", "flows needs a cluster")
	}
	filters, err := requestFilters(fp.Scope, fp.Filter)
	if err != nil {
		return res, err
	}
	l, rel, err := s.acquire(ctx, cluster) // fail now (no relay, no port-forward permission), not on the socket
	if err != nil {
		return res, err
	}
	rel()
	_ = l
	res.UsedContext = cluster
	res.StreamID = claim(func(ctx context.Context, t streams.Transport) { s.runFlows(ctx, t, cluster, filters) })
	return res, nil
}

func (s *Service) sendFrame(ctx context.Context, t streams.Transport, f protocol.FlowsFrame) error {
	b, err := json.Marshal(f)
	if err != nil {
		return err
	}
	return t.Write(ctx, false, b)
}

func (s *Service) runFlows(ctx context.Context, t streams.Transport, cluster string, filters []*pb.FlowFilter) {
	sctx, cancel := context.WithCancel(ctx)
	defer cancel()
	finish := func(err error) {
		f := protocol.FlowsFrame{End: true}
		if err != nil && !errors.Is(err, context.Canceled) && ctx.Err() == nil {
			f.Error = errMessage(err)
		}
		wctx, c := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		_ = s.sendFrame(wctx, t, f)
		c()
		t.Close(streams.CloseNormal, "")
	}
	l, rel, err := s.acquire(sctx, cluster)
	if err != nil {
		finish(err)
		return
	}
	defer rel()
	st, err := l.Client.GetFlows(sctx, &pb.GetFlowsRequest{Follow: true, Number: 20, Whitelist: filters})
	if err != nil {
		s.invalidate(cluster, l)
		finish(err)
		return
	}
	if err := s.sendFrame(sctx, t, protocol.FlowsFrame{Ready: &protocol.StreamReady{UsedContext: cluster}}); err != nil {
		return
	}
	sam := NewSampler(s.cfg.MaxFlowsPerSec, s.cfg.Now)
	in := make(chan *pb.Flow, 512)
	recvErr := make(chan error, 1)
	go func() { // relay -> sampler
		for {
			resp, err := st.Recv()
			if err != nil {
				recvErr <- err
				return
			}
			if f := resp.GetFlow(); f != nil && sam.Offer() {
				select {
				case in <- f:
				default: // the batcher is behind: sampled out (the reported rate still counts it)
				}
			}
		}
	}()
	var pausedAt atomicTime
	go func() { // client -> pause / resume; also ends the stream when the client goes away
		for {
			_, data, err := t.Read(sctx)
			if err != nil {
				cancel()
				return
			}
			var m protocol.FlowsClientMsg
			if json.Unmarshal(data, &m) == nil {
				switch {
				case m.Pause:
					pausedAt.set(s.cfg.Now())
				case m.Resume:
					pausedAt.set(time.Time{})
				}
			}
		}
	}()
	tick := time.NewTicker(BatchEvery)
	defer tick.Stop()
	var lastRate protocol.FlowRate
	for {
		select {
		case <-sctx.Done():
			finish(sctx.Err())
			return
		case err := <-recvErr:
			if sctx.Err() == nil {
				s.invalidate(cluster, l)
			}
			finish(err)
			return
		case <-tick.C:
		}
		paused := pausedAt.get()
		if !paused.IsZero() && s.cfg.Now().Sub(paused) >= PauseGrace {
			finish(errors.New("stream cancelled: no consumer for 10 s"))
			return
		}
		var batch []protocol.Flow
	drain:
		for len(batch) < 1024 {
			select {
			case f := <-in:
				if paused.IsZero() {
					batch = append(batch, toFlow(f))
				}
			default:
				break drain
			}
		}
		if !paused.IsZero() {
			continue
		}
		shown, total := sam.Rates()
		rate := protocol.FlowRate{Shown: shown, Total: total, Sampled: total > shown}
		if len(batch) == 0 && rate == lastRate {
			continue
		}
		frame := protocol.FlowsFrame{Flows: batch}
		if rate != lastRate || len(batch) > 0 {
			frame.Rate = &rate
			lastRate = rate
		}
		if err := s.sendFrame(sctx, t, frame); err != nil {
			cancel()
			finish(err)
			return
		}
	}
}

type atomicTime struct {
	mu sync.Mutex
	t  time.Time
}

func (a *atomicTime) set(t time.Time) { a.mu.Lock(); a.t = t; a.mu.Unlock() }
func (a *atomicTime) get() time.Time  { a.mu.Lock(); defer a.mu.Unlock(); return a.t }

func tsOf(t time.Time) *timestamppb.Timestamp { return timestamppb.New(t) }
