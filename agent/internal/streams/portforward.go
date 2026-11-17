package streams

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/zyno-io/v8s/agent/internal/kube"
	"github.com/zyno-io/v8s/agent/internal/ops"
	"github.com/zyno-io/v8s/agent/pkg/protocol"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/client-go/kubernetes"
)

const servicesGVR = "v1/services"

// forwards owns the running port-forwards (desktop only).
type forwards struct {
	m    *Manager
	mu   sync.Mutex
	byID map[string]*fwd
}

type fwd struct {
	info   protocol.PortForwardInfo
	cancel context.CancelFunc
	mu     sync.Mutex
}

func newForwards(m *Manager) *forwards { return &forwards{m: m, byID: map[string]*fwd{}} }

func (f *fwd) set(fn func(i *protocol.PortForwardInfo)) {
	f.mu.Lock()
	fn(&f.info)
	f.mu.Unlock()
}

func (f *fwd) snapshot() protocol.PortForwardInfo {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.info
}

// podReadyForForward reports a running, ready pod that is not terminating.
func podReadyForForward(p *corev1.Pod) bool {
	if p.DeletionTimestamp != nil || p.Status.Phase != corev1.PodRunning {
		return false
	}
	for _, c := range p.Status.Conditions {
		if c.Type == corev1.PodReady {
			return c.Status == corev1.ConditionTrue
		}
	}
	return false
}

// resolveServicePod picks a ready backing pod of a service and the pod port the service port maps to.
func resolveServicePod(ctx context.Context, cs kubernetes.Interface, ns, name string, port int) (pod string, target int, err error) {
	svc, err := cs.CoreV1().Services(ns).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return "", 0, err
	}
	if len(svc.Spec.Selector) == 0 {
		return "", 0, fmt.Errorf("service %s has no selector", name)
	}
	pods, err := cs.CoreV1().Pods(ns).List(ctx, metav1.ListOptions{LabelSelector: labels.SelectorFromSet(svc.Spec.Selector).String()})
	if err != nil {
		return "", 0, err
	}
	var ready []*corev1.Pod
	for i := range pods.Items {
		if podReadyForForward(&pods.Items[i]) {
			ready = append(ready, &pods.Items[i])
		}
	}
	if len(ready) == 0 {
		return "", 0, fmt.Errorf("service %s has no ready pod", name)
	}
	sort.Slice(ready, func(i, j int) bool { return ready[i].Name < ready[j].Name })
	p := ready[0]
	for _, sp := range svc.Spec.Ports {
		if int(sp.Port) != port {
			continue
		}
		return p.Name, targetPort(sp.TargetPort, p, port), nil
	}
	return "", 0, fmt.Errorf("service %s does not expose port %d", name, port)
}

// targetPort maps a service targetPort (number, or a named container port) to the pod's port.
func targetPort(tp intstr.IntOrString, p *corev1.Pod, def int) int {
	switch tp.Type {
	case intstr.Int:
		if tp.IntVal != 0 {
			return int(tp.IntVal)
		}
	case intstr.String:
		for _, c := range p.Spec.Containers {
			for _, cp := range c.Ports {
				if cp.Name == tp.StrVal {
					return int(cp.ContainerPort)
				}
			}
		}
	}
	return def
}

func (m *Manager) openPortForward(ctx context.Context, p protocol.StreamOpenParams) (protocol.StreamOpenResult, error) {
	var res protocol.StreamOpenResult
	if !m.cfg.AllowPortForward {
		return res, ops.NewError("forbidden", "port-forward is only available in the desktop agent")
	}
	if p.Ref == nil || p.Ref.Name == "" || p.Ref.Cluster == "" || p.Port <= 0 || p.Port > 65535 {
		return res, ops.NewError("bad_request", "portforward needs ref (pod or service) and port")
	}
	ref := *p.Ref
	if ref.GVR == "" {
		ref.GVR = podsGVR
	}
	if ref.GVR != podsGVR && ref.GVR != servicesGVR {
		return res, ops.NewError("bad_request", "ref must be a pod or a service")
	}
	routed, rt, err := m.route(ctx, ref.Cluster, "create", podsGVR, "portforward", ref.Namespace, "")
	if err != nil {
		return res, err
	}
	cs := rt.Clientset()
	resolve := func(ctx context.Context) (string, int, error) {
		if ref.GVR == podsGVR {
			return ref.Name, p.Port, nil
		}
		return resolveServicePod(ctx, cs, ref.Namespace, ref.Name, p.Port)
	}
	pod, remote, rerr := resolve(ctx)
	if rerr != nil {
		return res, m.mapRouted(ctx, rerr, routed, "get", podsGVR, "", ref.Namespace, ref.Name)
	}
	// the forward outlives the request: it runs until stopped (the 60 min cap applies through a write context)
	var fctx context.Context
	var cancel context.CancelFunc
	if routed.ViaWrite {
		var lim *limits
		fctx, lim = withLimits(context.Background(), 0, m.cfg.MaxDuration)
		cancel = lim.Stop
	} else {
		fctx, cancel = context.WithCancel(context.Background())
	}
	first, err := rt.PortForward(fctx, ref.Namespace, pod, remote, 0)
	if err != nil {
		cancel()
		return res, m.mapRouted(ctx, err, routed, "create", podsGVR, "portforward", ref.Namespace, pod)
	}
	id := newID()[:12]
	f := &fwd{cancel: cancel, info: protocol.PortForwardInfo{ID: id, Ref: ref, Port: p.Port, LocalPort: first.LocalPort, Pod: pod,
		State: "running", UsedContext: routed.ContextID}}
	m.pf.mu.Lock()
	m.pf.byID[id] = f
	m.pf.mu.Unlock()
	go m.pf.supervise(fctx, f, rt, first, resolve)
	res.LocalPort, res.ForwardID, res.UsedContext = first.LocalPort, id, routed.ContextID
	return res, nil
}

// Reconnect tuning (variables so tests can shorten them): exponential backoff from forwardRetry up to
// forwardRetryMax, and the forward is given up after forwardMaxTries consecutive failed attempts.
var (
	forwardRetry    = time.Second
	forwardRetryMax = 30 * time.Second
	forwardMaxTries = 8
)

// supervise keeps the forward alive: when it drops it re-resolves the backing pod (a service target moves to
// another ready pod) and rebinds the same local port. A target that stays gone (deleted pod) ends the forward
// after forwardMaxTries attempts with state "stopped" and the last error.
func (fs *forwards) supervise(ctx context.Context, f *fwd, rt kube.Runtime, cur *kube.Forward, resolve func(context.Context) (string, int, error)) {
	defer func() {
		cur.Stop()
		f.cancel() // releases the forward's context and its write-context cap timer
		f.set(func(i *protocol.PortForwardInfo) { i.State = "stopped" })
	}()
	for {
		select {
		case <-ctx.Done():
			return
		case <-cur.Done:
		}
		if ctx.Err() != nil {
			return
		}
		f.set(func(i *protocol.PortForwardInfo) { i.State = "reconnecting" })
		delay := forwardRetry
		reconnected := false
		for try := 1; ctx.Err() == nil && !reconnected; try++ {
			select {
			case <-ctx.Done():
				return
			case <-time.After(delay):
			}
			delay = min(delay*2, forwardRetryMax)
			pod, remote, err := resolve(ctx)
			var nf *kube.Forward
			if err == nil {
				nf, err = rt.PortForward(ctx, f.snapshot().Ref.Namespace, pod, remote, f.snapshot().LocalPort)
			}
			if err != nil {
				if try >= forwardMaxTries {
					f.set(func(i *protocol.PortForwardInfo) {
						i.Error = fmt.Sprintf("gave up after %d reconnect attempts: %s", try, err.Error())
					})
					return
				}
				f.set(func(i *protocol.PortForwardInfo) { i.Error = err.Error() })
				continue
			}
			cur = nf
			reconnected = true
			f.set(func(i *protocol.PortForwardInfo) { i.State, i.Pod, i.Error = "running", pod, "" })
		}
	}
}

// ListForwards returns the running and recently stopped forwards.
func (m *Manager) ListForwards() protocol.PortForwardListResult {
	m.pf.mu.Lock()
	defer m.pf.mu.Unlock()
	out := protocol.PortForwardListResult{Forwards: []protocol.PortForwardInfo{}}
	for _, f := range m.pf.byID {
		out.Forwards = append(out.Forwards, f.snapshot())
	}
	sort.Slice(out.Forwards, func(i, j int) bool { return out.Forwards[i].ID < out.Forwards[j].ID })
	return out
}

// StopForward stops one forward.
func (m *Manager) StopForward(id string) error {
	m.pf.mu.Lock()
	f := m.pf.byID[id]
	delete(m.pf.byID, id)
	m.pf.mu.Unlock()
	if f == nil {
		return ops.NewError("not_found", "no such forward")
	}
	f.cancel()
	return nil
}

// Close stops every forward (agent shutdown).
func (m *Manager) Close() {
	m.pf.mu.Lock()
	fs := m.pf.byID
	m.pf.byID = map[string]*fwd{}
	m.pf.mu.Unlock()
	for _, f := range fs {
		f.cancel()
	}
}
