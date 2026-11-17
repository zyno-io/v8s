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
}

// connect builds a Link: discovery, a port-forward through the cluster's credentials to a ready relay pod and a
// plaintext gRPC client over it. There is no direct relay URL: the relay has no authorization of its own.
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
	fctx, cancel := context.WithCancel(context.Background())
	fw, err := rt.PortForward(fctx, info.Namespace, info.Pod, info.Port, 0)
	if err != nil {
		cancel()
		e := ops.MapError(err, false)
		if oe, ok := e.(*ops.Error); ok && oe.Body.Code == "forbidden" {
			return nil, ops.NewError("forbidden", "Hubble needs create pods/portforward in %s to reach hubble-relay: %s", info.Namespace, oe.Body.Message)
		}
		return nil, e
	}
	cc, err := grpc.NewClient(fmt.Sprintf("127.0.0.1:%d", fw.LocalPort), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		fw.Stop()
		cancel()
		return nil, ops.NewError("internal", "hubble client: %s", err.Error())
	}
	var once sync.Once
	closeFn := func() { once.Do(func() { _ = cc.Close(); fw.Stop(); cancel() }) }
	return &Link{Info: info, Client: pb.NewObserverClient(cc), Close: closeFn,
		Dead: func() bool {
			select {
			case <-fw.Done:
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

// acquire returns a Link for the cluster (shared, reference counted, closed after linkIdle without users).
func (s *Service) acquire(ctx context.Context, cluster string) (*Link, func(), error) {
	s.mu.Lock()
	e := s.links[cluster]
	if e == nil {
		e = &linkEntry{}
		s.links[cluster] = e
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
			if e.refs == 0 && e.link == l {
				e.idle = time.AfterFunc(s.cfg.LinkIdle, func() {
					e.mu.Lock()
					defer e.mu.Unlock()
					if e.refs == 0 && e.link == l {
						l.Close()
						e.link = nil
					}
				})
			}
		})
	}, nil
}

// invalidate closes the cluster's link after a failure so the next acquire reconnects.
func (s *Service) invalidate(cluster string, l *Link) {
	s.mu.Lock()
	e := s.links[cluster]
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
