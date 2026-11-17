package fake

// Fake Hubble: every fake cluster except the third ("staging", no Cilium) has a hubble-relay Service with a
// ready pod in kube-system. A port-forward to that pod serves an in-process gRPC relay with synthetic flows
// (incl. POLICY_DENIED drops), so the flows stream, service map, drops view and the network problem classes
// work in fake mode.

import (
	"context"
	"fmt"
	"net"
	"strings"
	"sync"

	"github.com/zyno-io/v8s/agent/internal/hubble"
	"github.com/zyno-io/v8s/agent/internal/kube"
	"google.golang.org/grpc/test/bufconn"
)

const fakeRelayPod = "hubble-relay-6c9f7b8d4-fk7x2"

func hasFakeCilium(cluster string) bool { return clusterIndex(cluster) != 2 }

func init() {
	registerGen(func(w *World, c *Cluster) {
		if !hasFakeCilium(c.name) {
			return
		}
		sel := map[string]any{"k8s-app": "hubble-relay"}
		w.putLocked("v1/services", map[string]any{"metadata": objMeta("kube-system", "hubble-relay", map[string]string{"k8s-app": "hubble-relay"}),
			"spec": map[string]any{"type": "ClusterIP", "clusterIP": "10.96.40.10", "selector": sel,
				"ports": []any{map[string]any{"port": int64(80), "targetPort": int64(4245), "protocol": "TCP"}}}})
		w.putLocked("discovery.k8s.io/v1/endpointslices", map[string]any{
			"metadata":    objMeta("kube-system", "hubble-relay-fk1", map[string]string{"kubernetes.io/service-name": "hubble-relay"}),
			"addressType": "IPv4",
			"endpoints": []any{map[string]any{"addresses": []any{"10.0.0.40"}, "conditions": map[string]any{"ready": true},
				"targetRef": map[string]any{"kind": "Pod", "namespace": "kube-system", "name": fakeRelayPod}}},
			"ports": []any{map[string]any{"name": "grpc", "port": int64(4245), "protocol": "TCP"}}})
	})
}

// fakeRelayDial serves the in-process relay over an in-memory listener (no socket); ok is false for any other pod.
func (b *Backend) fakeRelayDial(ctx context.Context, ns, pod string) (*kube.PodDialer, bool) {
	if ns != "kube-system" || !strings.HasPrefix(pod, "hubble-relay") || !hasFakeCilium(b.w.cluster) {
		return nil, false
	}
	ln := bufconn.Listen(1 << 20)
	stop, done := hubble.NewFakeRelay().Serve(ln)
	var once sync.Once
	st := func() { once.Do(func() { stop(); _ = ln.Close() }) }
	go func() {
		select {
		case <-ctx.Done():
			st()
		case <-done:
		}
	}()
	return &kube.PodDialer{DialContext: func(ctx context.Context, _ string) (net.Conn, error) { return ln.DialContext(ctx) }, Done: done, Close: st}, true
}

// fakeRelayForward serves the in-process relay for a port-forward to the fake relay pod; ok is false for any other pod.
func (b *Backend) fakeRelayForward(ctx context.Context, ns, pod string, localPort int) (*kube.Forward, bool, error) {
	if ns != "kube-system" || !strings.HasPrefix(pod, "hubble-relay") || !hasFakeCilium(b.w.cluster) {
		return nil, false, nil
	}
	ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", localPort))
	if err != nil {
		return nil, true, err
	}
	stop, done := hubble.NewFakeRelay().Serve(ln)
	var once sync.Once
	st := func() { once.Do(stop) }
	go func() {
		select {
		case <-ctx.Done():
			st()
		case <-done:
		}
	}()
	return &kube.Forward{LocalPort: ln.Addr().(*net.TCPAddr).Port, Done: done, Err: func() error { return nil }, Stop: st}, true, nil
}
