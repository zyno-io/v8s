package hubble

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/zyno-io/v8s/agent/internal/hubble/pb"
	"github.com/zyno-io/v8s/agent/internal/kube"
	"github.com/zyno-io/v8s/agent/internal/ops"
	"github.com/zyno-io/v8s/agent/pkg/protocol"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
)

const (
	relayService     = "hubble-relay"
	defaultRelayPort = 4245
	linkIdle         = 30 * time.Second
)

var (
	servicesGVR = schema.GroupVersionResource{Version: "v1", Resource: "services"}
	slicesGVR   = schema.GroupVersionResource{Group: "discovery.k8s.io", Version: "v1", Resource: "endpointslices"}
)

// ErrNoRelay is returned when the cluster has no hubble-relay Service (Hubble is not installed or not enabled).
var ErrNoRelay = errors.New("no hubble-relay Service found in the cluster")

// NotReadyError says a hubble-relay Service exists but no relay pod is ready.
type NotReadyError struct{ Info protocol.HubbleRelayInfo }

func (e *NotReadyError) Error() string {
	return fmt.Sprintf("hubble-relay in %s has no ready pod", e.Info.Namespace)
}

// Discover finds the hubble-relay Service (any namespace, kube-system preferred) and a ready relay pod from its
// EndpointSlices. The pod port is the slice's resolved port, so a targetPort mapping is honoured.
func Discover(ctx context.Context, dyn dynamic.Interface) (protocol.HubbleRelayInfo, error) {
	svcs, err := dyn.Resource(servicesGVR).List(ctx, metav1.ListOptions{FieldSelector: "metadata.name=" + relayService})
	if err != nil {
		return protocol.HubbleRelayInfo{}, err
	}
	var cand []unstructured.Unstructured
	for _, s := range svcs.Items {
		if s.GetName() == relayService {
			cand = append(cand, s)
		}
	}
	var slices []unstructured.Unstructured
	if len(cand) > 0 {
		ns := preferredNS(cand)
		l, err := dyn.Resource(slicesGVR).Namespace(ns).List(ctx, metav1.ListOptions{LabelSelector: "kubernetes.io/service-name=" + relayService})
		if err != nil {
			return protocol.HubbleRelayInfo{}, err
		}
		slices = l.Items
	}
	return pickRelay(cand, slices)
}

func preferredNS(svcs []unstructured.Unstructured) string {
	for _, s := range svcs {
		if s.GetNamespace() == "kube-system" {
			return "kube-system"
		}
	}
	return svcs[0].GetNamespace()
}

// pickRelay chooses the relay endpoint from the discovered Services and the EndpointSlices of the chosen one.
func pickRelay(svcs, slices []unstructured.Unstructured) (protocol.HubbleRelayInfo, error) {
	if len(svcs) == 0 {
		return protocol.HubbleRelayInfo{}, ErrNoRelay
	}
	ns := preferredNS(svcs)
	info := protocol.HubbleRelayInfo{Namespace: ns, Service: relayService, Port: defaultRelayPort}
	for _, s := range svcs {
		if s.GetNamespace() != ns {
			continue
		}
		ports, _, _ := unstructured.NestedSlice(s.Object, "spec", "ports")
		if len(ports) > 0 {
			if m, ok := ports[0].(map[string]any); ok {
				switch t := m["targetPort"].(type) {
				case int64:
					info.Port = int(t)
				case float64:
					info.Port = int(t)
				}
			}
		}
	}
	for _, sl := range slices {
		if sl.GetNamespace() != ns || sl.GetLabels()["kubernetes.io/service-name"] != relayService {
			continue
		}
		port := info.Port
		if ps, _, _ := unstructured.NestedSlice(sl.Object, "ports"); len(ps) > 0 {
			if m, ok := ps[0].(map[string]any); ok {
				switch p := m["port"].(type) {
				case int64:
					port = int(p)
				case float64:
					port = int(p)
				}
			}
		}
		eps, _, _ := unstructured.NestedSlice(sl.Object, "endpoints")
		for _, e := range eps {
			m, _ := e.(map[string]any)
			cond, _ := m["conditions"].(map[string]any)
			if r, ok := cond["ready"].(bool); ok && !r {
				continue
			}
			if t, ok := cond["terminating"].(bool); ok && t {
				continue
			}
			ref, _ := m["targetRef"].(map[string]any)
			if kind, _ := ref["kind"].(string); kind != "Pod" {
				continue
			}
			name, _ := ref["name"].(string)
			if name == "" {
				continue
			}
			info.Pod, info.Port = name, port
			return info, nil
		}
	}
	return info, &NotReadyError{Info: info}
}

// Link is a usable connection to a cluster's relay.
type Link struct {
	Info   protocol.HubbleRelayInfo
	Client pb.ObserverClient
	// Close releases the port-forward and the gRPC connection.
	Close func()
	// Dead reports that the connection is gone (the forward ended): the next acquire builds a new one.
	Dead func() bool

	key string // the link-cache entry it was built for (cluster + the identity it runs as)
}

// connect builds a Link: discovery, a port-forward tunnel through the cluster's credentials to a ready relay pod and a
// plaintext gRPC client dialing over it (in-process: no listening socket). There is no direct relay URL: the relay has no authorization of its own.
func (s *Service) connect(ctx context.Context, cluster string) (*Link, error) {
	if s.cfg.Connect != nil {
		return s.cfg.Connect(ctx, cluster)
	}
	c, err := s.cfg.Conn(cluster)
	if err != nil {
		return nil, ops.NewError("not_found", "cluster %q unavailable: %s", cluster, err.Error())
	}
	info, err := Discover(ctx, c.Dynamic())
	switch {
	case errors.Is(err, ErrNoRelay):
		return nil, ops.NewError("not_found", "%s", ErrNoRelay.Error())
	case err != nil:
		var nr *NotReadyError
		if errors.As(err, &nr) {
			s.relayDown(cluster, &info, err)
			return nil, ops.NewError("unavailable", "%s", err.Error())
		}
		return nil, ops.MapError(err, false)
	}
	fc := c
	if s.cfg.Router != nil {
		routed, rerr := s.cfg.Router.Route(ctx, cluster, "create", "v1/pods", "portforward", info.Namespace, info.Pod)
		if rerr != nil {
			return nil, rerr
		}
		fc = routed.Conn
	}
	rt, ok := kube.RuntimeOf(fc)
	if !ok {
		return nil, ops.NewError("unavailable", "context %q cannot port-forward", cluster)
	}
	// The relay has no authorization of its own, so it is never bound to a local port (every local process could
	// reach it): gRPC dials the port-forward stream in-process.
	dctx, cancel := context.WithCancel(context.Background())
	pd, err := rt.DialPod(dctx, info.Namespace, info.Pod, info.Port)
	if err != nil {
		cancel()
		e := ops.MapError(err, false)
		if oe, ok := e.(*ops.Error); ok && oe.Body.Code == "forbidden" {
			return nil, ops.NewError("forbidden", "Hubble needs create pods/portforward in %s to reach hubble-relay: %s", info.Namespace, oe.Body.Message)
		}
		return nil, e
	}
	cc, err := grpc.NewClient("passthrough:///hubble-relay", grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithContextDialer(pd.DialContext))
	if err != nil {
		pd.Close()
		cancel()
		return nil, ops.NewError("internal", "hubble client: %s", err.Error())
	}
	var once sync.Once
	closeFn := func() { once.Do(func() { _ = cc.Close(); pd.Close(); cancel() }) }
	return &Link{Info: info, Client: pb.NewObserverClient(cc), Close: closeFn,
		Dead: func() bool {
			select {
			case <-pd.Done:
				return true
			default:
				return false
			}
		}}, nil
}

type linkEntry struct {
	mu   sync.Mutex // serializes building
	link *Link
	refs int
	idle *time.Timer
}

// linkKey keys the link cache by cluster and effective context.
func (s *Service) linkKey(cluster string) string {
	if s.cfg.Effective == nil {
		return cluster
	}
	return cluster + "\x00" + s.cfg.Effective(cluster)
}

// acquire returns a Link for the cluster (shared, reference counted, closed after linkIdle without users).
func (s *Service) acquire(ctx context.Context, cluster string) (*Link, func(), error) {
	for range 3 {
		l, rel, err := s.acquireKey(ctx, cluster, s.linkKey(cluster))
		if !errors.Is(err, errIdentityChanged) {
			return l, rel, err
		}
	}
	return nil, nil, ops.NewError("unavailable", "cluster %q switched context while connecting to hubble-relay: retry", cluster)
}

// errIdentityChanged: the cluster was elevated or reverted while its link was being built.
var errIdentityChanged = errors.New("identity changed")

func (s *Service) acquireKey(ctx context.Context, cluster, key string) (*Link, func(), error) {
	s.mu.Lock()
	e := s.links[key]
	if e == nil {
		e = &linkEntry{}
		s.links[key] = e
	}
	s.mu.Unlock()
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.link != nil && e.link.Dead() {
		e.link.Close()
		e.link = nil
	}
	if e.link == nil {
		l, err := s.connect(ctx, cluster)
		if err != nil {
			return nil, nil, err
		}
		if s.linkKey(cluster) != key { // built (partly) as the other identity: never file it under this key
			l.Close()
			return nil, nil, errIdentityChanged
		}
		l.key = key
		e.link = l
	}
	if e.idle != nil {
		e.idle.Stop()
		e.idle = nil
	}
	e.refs++
	l := e.link
	var once sync.Once
	return l, func() {
		once.Do(func() {
			e.mu.Lock()
			defer e.mu.Unlock()
			e.refs--
			// refs counts the holders of every generation of the link: when the last one lets go, the link that is
			// current now (not necessarily the one this holder got) idles out. Holders of an older link that was
			// rebuilt meanwhile must not leave the newer one without a timer.
			if e.refs == 0 && e.link != nil {
				cur := e.link
				e.idle = time.AfterFunc(s.cfg.LinkIdle, func() {
					e.mu.Lock()
					defer e.mu.Unlock()
					if e.refs == 0 && e.link == cur {
						cur.Close()
						e.link = nil
					}
				})
			}
		})
	}, nil
}

// invalidate closes the cluster's link after a failure so the next acquire reconnects.
func (s *Service) invalidate(cluster string, l *Link) {
	key := l.key // the entry it was acquired under, not the cluster's current identity
	if key == "" {
		key = s.linkKey(cluster)
	}
	s.mu.Lock()
	e := s.links[key]
	s.mu.Unlock()
	if e == nil {
		return
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.link == l {
		l.Close()
		e.link = nil
	}
}

// Close releases every link.
func (s *Service) Close() {
	s.mu.Lock()
	es := make([]*linkEntry, 0, len(s.links))
	for _, e := range s.links {
		es = append(es, e)
	}
	s.mu.Unlock()
	for _, e := range es {
		e.mu.Lock()
		if e.idle != nil {
			e.idle.Stop()
		}
		if e.link != nil {
			e.link.Close()
			e.link = nil
		}
		e.mu.Unlock()
	}
}
