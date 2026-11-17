package hubble

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/zyno-io/v8s/agent/internal/hubble/pb"
	"github.com/zyno-io/v8s/agent/internal/problems"
	"github.com/zyno-io/v8s/agent/pkg/protocol"
)

// Problem classes of the module (docs/problems.md): all external (no row inputs), fed with Engine.Inject.
const (
	ClassPolicyDrops = "network.policy-drops"
	ClassL7Errors    = "network.l7-errors"
	ClassDNSErrors   = "network.dns-errors"
	ClassRelayDown   = "hubble.relay-down"
)

// Thresholds of the network classes over a 5 minute window.
const (
	problemWindowMin = 5
	policyDropsMin   = 10
	l7ErrorsMin      = 20
	dnsErrorsMin     = 20
	findingSource    = "hubble"
)

func init() {
	problems.Register(&problems.Detector{Class: ClassPolicyDrops, Family: problems.FamFlows, Default: false, Severity: problems.SevWarning,
		Title: "Sustained network policy drops"})
	problems.Register(&problems.Detector{Class: ClassL7Errors, Family: problems.FamFlows, Default: false, Severity: problems.SevWarning,
		Title: "HTTP 5xx error spikes"})
	problems.Register(&problems.Detector{Class: ClassDNSErrors, Family: problems.FamFlows, Default: false, Severity: problems.SevWarning,
		Title: "DNS NXDOMAIN/SERVFAIL spikes"})
	problems.Register(&problems.Detector{Class: ClassRelayDown, Family: problems.FamFlows, Default: true, Severity: problems.SevWarning,
		Title: "Hubble relay unreachable"})
}

// classMask selects the network classes a subscription feeds.
type classMask uint8

const (
	maskDrops classMask = 1 << iota
	maskL7
	maskDNS
)

// filters is the narrow server-side whitelist of the continuous subscription: dropped flows, and only when the
// class is enabled HTTP 5xx and DNS records. Nothing else is requested.
func (m classMask) filters() []*pb.FlowFilter {
	var out []*pb.FlowFilter
	if m&maskDrops != 0 {
		out = append(out, &pb.FlowFilter{Verdict: []pb.Verdict{pb.Verdict_DROPPED}})
	}
	if m&maskL7 != 0 {
		out = append(out, &pb.FlowFilter{Protocol: []string{"http"}, HttpStatusCode: []string{"5+"}})
	}
	if m&maskDNS != 0 {
		out = append(out, &pb.FlowFilter{Protocol: []string{"dns"}})
	}
	return out
}

// observe counts one flow of the subscription into minute counters; it keeps nothing else.
func observe(c *counters, f *pb.Flow, mask classMask, nowMs int64) {
	at := nowMs
	if t := f.GetTime(); t != nil && t.AsTime().UnixMilli() > 0 {
		at = t.AsTime().UnixMilli()
	}
	sns, swl := workloadIdentity(f.Source)
	dns, dwl := workloadIdentity(f.Destination)
	switch {
	case mask&maskDrops != 0 && f.Verdict == pb.Verdict_DROPPED && isPolicyDrop(f.DropReasonDesc):
		c.add("pd|"+identityID(sns, swl)+">"+identityID(dns, dwl)+"|"+DropReasonName(f.DropReasonDesc), at, 1)
	case mask&maskL7 != 0 && f.GetL7().GetType() == pb.L7FlowType_RESPONSE && f.GetL7().GetHttp() != nil && f.GetL7().GetHttp().Code >= 500:
		c.add("l7|"+identityID(dns, dwl), at, 1)
	case mask&maskDNS != 0 && f.GetL7().GetType() == pb.L7FlowType_RESPONSE && f.GetL7().GetDns() != nil &&
		(f.GetL7().GetDns().Rcode == 2 || f.GetL7().GetDns().Rcode == 3):
		c.add("dns|"+identityID(sns, swl)+"|"+dnsRcodeName(f.GetL7().GetDns().Rcode), at, 1)
	}
}

func splitID(id string) (ns, wl string) {
	if a, b, ok := strings.Cut(id, "/"); ok {
		return a, b
	}
	return "", id
}

// evalDrafts turns the counters into findings per class: a pair of workloads with at least policyDropsMin
// POLICY_DENIED/POLICY_DENY drops within 5 minutes, a service with l7ErrorsMin HTTP 5xx, a client with
// dnsErrorsMin NXDOMAIN/SERVFAIL.
func evalDrafts(c *counters, nowMs int64, mask classMask) map[string][]problems.Draft {
	out := map[string][]problems.Draft{}
	for _, class := range []string{ClassPolicyDrops, ClassL7Errors, ClassDNSErrors} {
		out[class] = nil
	}
	for _, key := range c.keys() {
		n, first := c.sum(key, nowMs, problemWindowMin)
		parts := strings.Split(key, "|")
		switch parts[0] {
		case "pd":
			if mask&maskDrops == 0 || n < policyDropsMin {
				continue
			}
			pair := strings.SplitN(parts[1], ">", 2)
			sns, swl := splitID(pair[0])
			dns, dwl := splitID(pair[1])
			out[ClassPolicyDrops] = append(out[ClassPolicyDrops], problems.Draft{
				Subject: problems.Subject{Kind: "Workload", NS: dns, Name: dwl}, Disc: pair[0] + ">" + parts[2],
				Title:   fmt.Sprintf("Policy drops: %s → %s (%d in %d min)", identityID(sns, swl), identityID(dns, dwl), n, problemWindowMin),
				Message: parts[2] + ": traffic is denied by a NetworkPolicy or CiliumNetworkPolicy", Count: n, Since: first, Source: findingSource})
		case "l7":
			if mask&maskL7 == 0 || n < l7ErrorsMin {
				continue
			}
			ns, wl := splitID(parts[1])
			out[ClassL7Errors] = append(out[ClassL7Errors], problems.Draft{Subject: problems.Subject{Kind: "Workload", NS: ns, Name: wl},
				Title: fmt.Sprintf("HTTP 5xx: %s (%d in %d min)", parts[1], n, problemWindowMin), Message: "responses with status 5xx observed by Hubble",
				Count: n, Since: first, Source: findingSource})
		case "dns":
			if mask&maskDNS == 0 || n < dnsErrorsMin {
				continue
			}
			ns, wl := splitID(parts[1])
			out[ClassDNSErrors] = append(out[ClassDNSErrors], problems.Draft{Subject: problems.Subject{Kind: "Workload", NS: ns, Name: wl}, Disc: parts[2],
				Title: fmt.Sprintf("DNS %s: %s (%d in %d min)", parts[2], parts[1], n, problemWindowMin), Message: "failing DNS lookups observed by Hubble",
				Count: n, Since: first, Source: findingSource})
		}
	}
	return out
}

// ---- monitor ------------------------------------------------------------------------------

type monitor struct {
	s    *Service
	mu   sync.Mutex
	subs map[string]*subscription
	last map[string]time.Time // last relay-down check per cluster
}

type subscription struct {
	cancel context.CancelFunc
	mask   classMask
	mu     sync.Mutex
	c      *counters
	done   chan struct{}
}

// StartProblems runs the network problem monitor until ctx ends. The continuous flow subscription of a cluster
// runs only while a network class is enabled for it and a UI client is connected (docs/integrations.md); with
// no client, or the classes off, nothing is subscribed and the findings are withdrawn.
func (s *Service) StartProblems(ctx context.Context) {
	if s.cfg.Problems == nil {
		return
	}
	m := &monitor{s: s, subs: map[string]*subscription{}, last: map[string]time.Time{}}
	s.mu.Lock()
	s.mon = m
	s.mu.Unlock()
	go func() {
		t := time.NewTicker(s.cfg.MonitorEvery)
		defer t.Stop()
		m.tick(ctx)
		for {
			select {
			case <-ctx.Done():
				m.stopAll()
				return
			case <-t.C:
				m.tick(ctx)
			}
		}
	}()
}

func (m *monitor) clients() bool { return m.s.cfg.ClientCount == nil || m.s.cfg.ClientCount() > 0 }

func (m *monitor) maskFor(cluster string) classMask {
	h := m.s.cfg.Problems
	var mask classMask
	if h.ClassEnabled(cluster, ClassPolicyDrops) {
		mask |= maskDrops
	}
	if h.ClassEnabled(cluster, ClassL7Errors) {
		mask |= maskL7
	}
	if h.ClassEnabled(cluster, ClassDNSErrors) {
		mask |= maskDNS
	}
	return mask
}

// Tick runs one monitor evaluation (exported for tests through TickForTest).
func (m *monitor) tick(ctx context.Context) {
	s := m.s
	var clusters []string
	if s.cfg.Clusters != nil {
		clusters = s.cfg.Clusters()
	}
	live := map[string]bool{}
	clients := m.clients()
	for _, cl := range clusters {
		live[cl] = true
		e := s.cfg.Problems.Engine(cl)
		if e == nil {
			continue
		}
		mask := m.maskFor(cl)
		if !clients {
			mask = 0
		}
		m.mu.Lock()
		sub := m.subs[cl]
		m.mu.Unlock()
		if sub != nil && sub.mask != mask {
			m.stop(cl)
			sub = nil
		}
		if sub == nil && mask != 0 {
			m.start(ctx, cl, mask)
		}
		if sub != nil {
			m.publish(e, sub)
		} else if mask == 0 {
			m.withdraw(e)
		}
		if clients && m.relayCheckDue(cl) {
			go m.checkRelay(ctx, cl)
		}
	}
	m.mu.Lock()
	var gone []string
	for cl := range m.subs {
		if !live[cl] {
			gone = append(gone, cl)
		}
	}
	m.mu.Unlock()
	for _, cl := range gone {
		m.stop(cl)
	}
}

func (m *monitor) relayCheckDue(cl string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.s.cfg.Now().Sub(m.last[cl]) < time.Minute {
		return false
	}
	m.last[cl] = m.s.cfg.Now()
	return true
}

// checkRelay raises hubble.relay-down when a relay Service exists without a ready pod; a missing Service is not
// a problem (Hubble is simply not installed). Connection failures are reported by the subscription and by use.
func (m *monitor) checkRelay(ctx context.Context, cluster string) {
	s := m.s
	if s.cfg.Conn == nil {
		return
	}
	c, err := s.cfg.Conn(cluster)
	if err != nil {
		return
	}
	cctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	info, err := Discover(cctx, c.Dynamic())
	var nr *NotReadyError
	switch {
	case errors.As(err, &nr):
		s.relayDown(cluster, &info, err)
	case err == nil || errors.Is(err, ErrNoRelay):
		s.relayUp(cluster)
	}
}

func (m *monitor) publish(e *problems.Engine, sub *subscription) {
	sub.mu.Lock()
	now := m.s.cfg.Now().UnixMilli()
	sub.c.prune(now, problemWindowMin+1)
	drafts := evalDrafts(sub.c, now, sub.mask)
	sub.mu.Unlock()
	for class, ds := range drafts {
		e.Inject(class, findingSource, "counters", ds)
	}
}

func (m *monitor) withdraw(e *problems.Engine) {
	for _, class := range []string{ClassPolicyDrops, ClassL7Errors, ClassDNSErrors} {
		e.Inject(class, findingSource, "counters", nil)
	}
}

func (m *monitor) start(ctx context.Context, cluster string, mask classMask) {
	sctx, cancel := context.WithCancel(ctx)
	sub := &subscription{cancel: cancel, mask: mask, c: newCounters(), done: make(chan struct{})}
	m.mu.Lock()
	m.subs[cluster] = sub
	m.mu.Unlock()
	go func() {
		defer close(sub.done)
		m.s.runSubscription(sctx, cluster, sub)
	}()
}

func (m *monitor) stop(cluster string) {
	m.mu.Lock()
	sub := m.subs[cluster]
	delete(m.subs, cluster)
	m.mu.Unlock()
	if sub != nil {
		sub.cancel()
		<-sub.done
	}
	if e := m.s.cfg.Problems.Engine(cluster); e != nil {
		m.withdraw(e)
	}
}

func (m *monitor) stopAll() {
	m.mu.Lock()
	var cls []string
	for cl := range m.subs {
		cls = append(cls, cl)
	}
	m.mu.Unlock()
	for _, cl := range cls {
		m.stop(cl)
	}
}

// Subscribed reports whether a continuous subscription runs for the cluster (tests, diagnostics).
func (s *Service) Subscribed(cluster string) bool {
	s.mu.Lock()
	m := s.mon
	s.mu.Unlock()
	if m == nil {
		return false
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.subs[cluster] != nil
}

// TickForTest runs one monitor tick synchronously.
func (s *Service) TickForTest(ctx context.Context) {
	s.mu.Lock()
	m := s.mon
	s.mu.Unlock()
	if m != nil {
		m.tick(ctx)
	}
}

// runSubscription feeds sub's counters from a follow stream with the narrow server-side filter, reconnecting
// with a backoff; the first connection replays the last window so a problem is found at once.
func (s *Service) runSubscription(ctx context.Context, cluster string, sub *subscription) {
	backoff := time.Second
	since := s.cfg.Now().Add(-problemWindowMin * time.Minute)
	for ctx.Err() == nil {
		l, rel, err := s.acquire(ctx, cluster)
		if err == nil {
			var st pb.Observer_GetFlowsClient
			st, err = l.Client.GetFlows(ctx, &pb.GetFlowsRequest{Follow: true, Whitelist: sub.mask.filters(), Since: tsOf(since)})
			if err == nil {
				s.relayUp(cluster)
				backoff = time.Second
				for {
					resp, rerr := st.Recv()
					if rerr != nil {
						err = rerr
						break
					}
					if f := resp.GetFlow(); f != nil {
						sub.mu.Lock()
						observe(sub.c, f, sub.mask, s.cfg.Now().UnixMilli())
						sub.mu.Unlock()
					}
				}
				since = s.cfg.Now().Add(-time.Minute)
			}
			rel()
			if ctx.Err() == nil {
				s.invalidate(cluster, l)
				info := l.Info
				s.relayDown(cluster, &info, err)
			}
		} else if ctx.Err() == nil {
			if !errors.Is(err, ErrNoRelay) && !isNotFound(err) {
				s.relayDown(cluster, nil, err)
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		backoff = min(backoff*2, time.Minute)
	}
}

func isNotFound(err error) bool {
	return strings.Contains(errMessage(err), ErrNoRelay.Error())
}

// relayDown injects hubble.relay-down for a cluster whose relay Service exists but cannot be reached.
func (s *Service) relayDown(cluster string, info *protocol.HubbleRelayInfo, err error) {
	if s.cfg.Problems == nil {
		return
	}
	e := s.cfg.Problems.Engine(cluster)
	if e == nil {
		return
	}
	ns := "kube-system"
	if info != nil && info.Namespace != "" {
		ns = info.Namespace
	}
	e.Inject(ClassRelayDown, findingSource, "relay", []problems.Draft{{
		Subject: problems.Subject{Kind: "Service", NS: ns, Name: relayService, GVR: "v1/services"},
		Title:   "hubble-relay is unreachable in " + cluster, Message: errMessage(err), Source: findingSource}})
}

func (s *Service) relayUp(cluster string) {
	if s.cfg.Problems == nil {
		return
	}
	if e := s.cfg.Problems.Engine(cluster); e != nil {
		e.Inject(ClassRelayDown, findingSource, "relay", nil)
	}
}
