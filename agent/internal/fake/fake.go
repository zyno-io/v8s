// Package fake is the --fake source: N synthetic clusters of realistic pods served through the same
// list/watch API the real client exposes, so they take the identical projection -> reflector ->
// store path. State is kept compact (about 50 bytes per pod); corev1.Pod objects are generated on
// demand for list pages and watch events.
package fake

import (
	"context"
	"fmt"
	"math/rand/v2"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/zyno-io/v8s/agent/internal/ingest"
	"github.com/zyno-io/v8s/agent/pkg/protocol"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/watch"
)

// Config is the parsed --fake flag.
type Config struct {
	Clusters int
	Pods     int // total across clusters
	Churn    int // pod events per second across all clusters
}

// ParseConfig parses "clusters=3,pods=50000,churn=200".
func ParseConfig(s string) (Config, error) {
	c := Config{Clusters: 3, Pods: 50000, Churn: 200}
	if s == "" || s == "true" {
		return c, nil
	}
	for _, kv := range strings.Split(s, ",") {
		k, v, ok := strings.Cut(strings.TrimSpace(kv), "=")
		n, err := strconv.Atoi(v)
		if !ok || err != nil || n < 0 {
			return c, fmt.Errorf("bad --fake option %q", kv)
		}
		switch k {
		case "clusters":
			c.Clusters = n
		case "pods":
			c.Pods = n
		case "churn":
			c.Churn = n
		default:
			return c, fmt.Errorf("unknown --fake option %q", k)
		}
	}
	if c.Clusters < 1 {
		return c, fmt.Errorf("--fake clusters must be >= 1")
	}
	return c, nil
}

var clusterNames = []string{"prod-us", "prod-eu", "staging", "dev", "prod-ap", "qa", "sandbox", "edge"}

// Name returns the i-th fake cluster name.
func Name(i int) string {
	if i < len(clusterNames) {
		return clusterNames[i]
	}
	return fmt.Sprintf("cluster-%d", i+1)
}

// Source implements cluster.Source.
type Source struct {
	cfg Config

	mu       sync.Mutex
	clusters map[string]*Cluster
}

func NewSource(cfg Config) *Source {
	return &Source{cfg: cfg, clusters: map[string]*Cluster{}}
}

func (s *Source) Mode() string { return "fake" }

func (s *Source) Clusters() []protocol.Cluster {
	out := make([]protocol.Cluster, s.cfg.Clusters)
	for i := range out {
		n := Name(i)
		out[i] = protocol.Cluster{ID: n, Name: n, Server: "https://" + n + ".fake.invalid:6443", Groups: []string{}}
	}
	return out
}

func (s *Source) DefaultSelection() []string {
	cs := s.Clusters()
	out := make([]string, len(cs))
	for i := range cs {
		out[i] = cs[i].ID
	}
	return out
}

func (s *Source) PodAPI(id string) (ingest.PodAPI, error) {
	for i := 0; i < s.cfg.Clusters; i++ {
		if Name(i) == id {
			s.mu.Lock()
			defer s.mu.Unlock()
			c := s.clusters[id]
			if c == nil {
				pods := s.cfg.Pods / s.cfg.Clusters
				rate := float64(s.cfg.Churn) / float64(s.cfg.Clusters)
				c = NewCluster(id, pods, rate, uint64(i)+1)
				s.clusters[id] = c
			}
			return c, nil
		}
	}
	return nil, fmt.Errorf("unknown fake cluster %q", id)
}

func (s *Source) Release(id string) {
	s.mu.Lock()
	c := s.clusters[id]
	delete(s.clusters, id)
	s.mu.Unlock()
	if c != nil {
		c.Stop()
	}
}

// ---- cluster --------------------------------------------------------------------------

const (
	stRunning uint8 = iota
	stCrash
	stPending
	stImagePull
	stOOM
	stCompleted
	stError
	stDead // tombstone
)

type fakePod struct {
	id       uint64
	h1       uint64
	h2       uint32
	restarts int32
	rv       uint64
	created  int64
	app      uint16
	ns       uint8
	node     uint8
	nc       uint8
	state    uint8
}

type logEvent struct {
	rv  uint64
	typ watch.EventType
	p   fakePod
}

const logSize = 20000

// Cluster is one fake cluster; it implements ingest.PodAPI.
type Cluster struct {
	name string

	mu       sync.Mutex
	pods     []fakePod // slot-stable: deletes leave tombstones that creates reuse
	rv       uint64
	nextID   uint64
	rng      *rand.Rand
	log      []logEvent // ring
	logStart uint64     // rv of the oldest retained event (0 = none dropped)
	watchers map[*watcher]struct{}
	cancel   context.CancelFunc
}

// NewCluster generates a cluster of n pods and starts churn at rate events/s (0 = static).
func NewCluster(name string, n int, rate float64, seed uint64) *Cluster {
	c := &Cluster{name: name, rng: rand.New(rand.NewPCG(seed, 0x76387338)), watchers: map[*watcher]struct{}{}, rv: 1_000_000}
	c.pods = make([]fakePod, n)
	for i := range c.pods {
		c.pods[i] = c.newPod(i%400, c.rng.IntN(100000))
		c.pods[i].created = time.Now().UnixMilli() - int64(c.rng.IntN(30*86400))*1000
	}
	ctx, cancel := context.WithCancel(context.Background())
	c.cancel = cancel
	if rate > 0 {
		go c.churn(ctx, rate)
	}
	return c
}

func (c *Cluster) Stop() {
	c.cancel()
	c.mu.Lock()
	for w := range c.watchers {
		w.closeLocked()
	}
	c.watchers = map[*watcher]struct{}{}
	c.pods, c.log = nil, nil
	c.mu.Unlock()
}

func (c *Cluster) newPod(app, k int) fakePod {
	c.nextID++
	c.rv++
	p := fakePod{id: c.nextID, h1: c.rng.Uint64() & 0xffffffffff, h2: c.rng.Uint32() & 0xfffff, rv: c.rv,
		created: time.Now().UnixMilli(), app: uint16(app), ns: uint8(app % 37), node: uint8(c.rng.IntN(150)), nc: uint8(1 + app%3)}
	switch r := k % 1000; {
	case r < 5:
		p.state, p.restarts = stCrash, int32(3+c.rng.IntN(100))
	case r < 8:
		p.state = stPending
	case r < 10:
		p.state = stImagePull
	case r < 12:
		p.state, p.restarts = stOOM, int32(1+c.rng.IntN(5))
	case r < 17:
		p.state = stCompleted
	case r < 19:
		p.state = stError
	default:
		if r%53 == 0 {
			p.restarts = 14
		}
	}
	return p
}

func (p *fakePod) uid(cluster string) string {
	return fmt.Sprintf("%08x-%04x-4000-8000-%012x", p.id&0xffffffff, len(cluster), p.id)
}

func (p *fakePod) name() string {
	return fmt.Sprintf("svc-%d-%010x-%05x", p.app, p.h1, p.h2)
}

// pod builds the corev1.Pod the real API server would send (sans the fields the agent drops anyway).
func (c *Cluster) pod(p *fakePod) *corev1.Pod {
	ns := "team-" + strconv.Itoa(int(p.ns))
	app := "svc-" + strconv.Itoa(int(p.app))
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name: p.name(), Namespace: ns, UID: types.UID(p.uid(c.name)), ResourceVersion: strconv.FormatUint(p.rv, 10),
			CreationTimestamp: metav1.Unix(p.created/1000, 0),
			OwnerReferences: []metav1.OwnerReference{{APIVersion: "apps/v1", Kind: "ReplicaSet",
				Name: fmt.Sprintf("%s-%010x", app, p.h1), Controller: ptrTrue()}},
		},
		Spec: corev1.PodSpec{NodeName: "node-" + strconv.Itoa(int(p.node))},
	}
	if p.state == stPending {
		pod.Spec.NodeName = ""
	}
	for i := 0; i < int(p.nc); i++ {
		name := app
		if i > 0 {
			name = "sidecar-" + strconv.Itoa(i)
		}
		pod.Spec.Containers = append(pod.Spec.Containers, corev1.Container{Name: name, Image: "registry.example.com/" + ns + "/" + app + ":latest"})
		cs := corev1.ContainerStatus{Name: name, Ready: true, RestartCount: p.restarts / int32(p.nc), State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}}}
		if i == 0 {
			cs.RestartCount = p.restarts - (p.restarts/int32(p.nc))*int32(p.nc-1)
		}
		pod.Status.ContainerStatuses = append(pod.Status.ContainerStatuses, cs)
	}
	st := &pod.Status
	st.Phase = corev1.PodRunning
	ready := corev1.ConditionTrue
	setFirst := func(cs corev1.ContainerStatus) { st.ContainerStatuses[0] = cs }
	switch p.state {
	case stCrash:
		cs := st.ContainerStatuses[0]
		cs.Ready, cs.State = false, corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "CrashLoopBackOff"}}
		setFirst(cs)
		ready = corev1.ConditionFalse
	case stPending:
		st.Phase, st.ContainerStatuses, ready = corev1.PodPending, nil, corev1.ConditionFalse
	case stImagePull:
		st.Phase = corev1.PodPending
		for i := range st.ContainerStatuses {
			st.ContainerStatuses[i].Ready = false
			st.ContainerStatuses[i].State = corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "ImagePullBackOff"}}
		}
		ready = corev1.ConditionFalse
	case stOOM:
		cs := st.ContainerStatuses[0]
		cs.Ready, cs.State = false, corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{Reason: "OOMKilled", ExitCode: 137}}
		setFirst(cs)
		ready = corev1.ConditionFalse
	case stError:
		cs := st.ContainerStatuses[0]
		cs.Ready, cs.State = false, corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{Reason: "Error", ExitCode: 1}}
		setFirst(cs)
		ready = corev1.ConditionFalse
	case stCompleted:
		st.Phase, ready = corev1.PodSucceeded, corev1.ConditionFalse
		for i := range st.ContainerStatuses {
			st.ContainerStatuses[i].Ready = false
			st.ContainerStatuses[i].State = corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{Reason: "Completed"}}
		}
	}
	st.Conditions = []corev1.PodCondition{{Type: corev1.PodReady, Status: ready}}
	return pod
}

func ptrTrue() *bool { b := true; return &b }

// ---- list / watch ---------------------------------------------------------------------

func (c *Cluster) List(ctx context.Context, opts metav1.ListOptions) (*corev1.PodList, error) {
	start, rv := 0, uint64(0)
	if opts.Continue != "" {
		a, b, ok := strings.Cut(opts.Continue, ":")
		i, err1 := strconv.Atoi(a)
		r, err2 := strconv.ParseUint(b, 10, 64)
		if !ok || err1 != nil || err2 != nil {
			return nil, apierrors.NewBadRequest("bad continue token")
		}
		start, rv = i, r
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if opts.Continue == "" {
		rv = c.rv
	}
	limit := int(opts.Limit)
	if limit <= 0 {
		limit = len(c.pods)
	}
	pl := &corev1.PodList{ListMeta: metav1.ListMeta{ResourceVersion: strconv.FormatUint(rv, 10)}}
	i := start
	for ; i < len(c.pods) && len(pl.Items) < limit; i++ {
		if c.pods[i].state != stDead {
			pl.Items = append(pl.Items, *c.pod(&c.pods[i]))
		}
	}
	if i < len(c.pods) {
		pl.Continue = fmt.Sprintf("%d:%d", i, rv)
	}
	return pl, nil
}

type watcher struct {
	c      *Cluster
	ch     chan watch.Event
	closed bool
}

func (w *watcher) ResultChan() <-chan watch.Event { return w.ch }
func (w *watcher) Stop() {
	w.c.mu.Lock()
	delete(w.c.watchers, w)
	w.closeLocked()
	w.c.mu.Unlock()
}
func (w *watcher) closeLocked() {
	if !w.closed {
		w.closed = true
		close(w.ch)
	}
}

// emitLocked records an event and fans it out; a watcher that cannot keep up is dropped (the
// reflector then re-establishes it).
func (c *Cluster) emitLocked(typ watch.EventType, p fakePod) {
	if len(c.log) == logSize {
		c.logStart = c.log[0].rv + 1
		copy(c.log, c.log[1:])
		c.log = c.log[:logSize-1]
	}
	c.log = append(c.log, logEvent{p.rv, typ, p})
	for w := range c.watchers {
		select {
		case w.ch <- watch.Event{Type: typ, Object: c.pod(&p)}:
		default:
			delete(c.watchers, w)
			w.closeLocked()
		}
	}
}

func (c *Cluster) Watch(ctx context.Context, opts metav1.ListOptions) (watch.Interface, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	w := &watcher{c: c, ch: make(chan watch.Event, 16384)}
	initial := opts.SendInitialEvents != nil && *opts.SendInitialEvents
	switch {
	case initial:
		snap := make([]fakePod, 0, len(c.pods))
		for i := range c.pods {
			if c.pods[i].state != stDead {
				snap = append(snap, c.pods[i])
			}
		}
		rv := c.rv
		// Live events queue in w.ch (registered here) while the snapshot streams out.
		out := make(chan watch.Event)
		sctx, cancel := context.WithCancel(ctx)
		c.watchers[w] = struct{}{}
		go func() {
			defer close(out)
			defer func() {
				c.mu.Lock()
				delete(c.watchers, w)
				c.mu.Unlock()
			}()
			send := func(ev watch.Event) bool {
				select {
				case out <- ev:
					return true
				case <-sctx.Done():
					return false
				}
			}
			for i := range snap {
				c.mu.Lock()
				pod := c.pod(&snap[i])
				c.mu.Unlock()
				if !send(watch.Event{Type: watch.Added, Object: pod}) {
					return
				}
			}
			bm := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{ResourceVersion: strconv.FormatUint(rv, 10),
				Annotations: map[string]string{metav1.InitialEventsAnnotationKey: "true"}}}
			if !send(watch.Event{Type: watch.Bookmark, Object: bm}) {
				return
			}
			for {
				select {
				case ev, ok := <-w.ch:
					if !ok {
						return
					}
					if rvOf(ev) <= rv {
						continue
					}
					if !send(ev) {
						return
					}
				case <-sctx.Done():
					return
				}
			}
		}()
		return &stopper{ch: out, cancel: cancel}, nil
	default:
		rv, _ := strconv.ParseUint(opts.ResourceVersion, 10, 64)
		if rv != 0 && rv+1 < c.logStart {
			w.ch <- watch.Event{Type: watch.Error, Object: &apierrors.NewResourceExpired("too old resource version").ErrStatus}
			return w, nil
		}
		if rv != 0 {
			for _, e := range c.log {
				if e.rv > rv {
					w.ch <- watch.Event{Type: e.typ, Object: c.pod(&e.p)}
				}
			}
		}
		c.watchers[w] = struct{}{}
		return w, nil
	}
}

func rvOf(ev watch.Event) uint64 {
	if p, ok := ev.Object.(*corev1.Pod); ok {
		v, _ := strconv.ParseUint(p.ResourceVersion, 10, 64)
		return v
	}
	return 0
}

// stopper stops the streaming-list goroutine.
type stopper struct {
	ch     chan watch.Event
	cancel context.CancelFunc
}

func (s *stopper) ResultChan() <-chan watch.Event { return s.ch }
func (s *stopper) Stop()                          { s.cancel() }

var _ runtime.Object = (*corev1.Pod)(nil)

// ---- churn ----------------------------------------------------------------------------

func (c *Cluster) churn(ctx context.Context, rate float64) {
	const tick = 50 * time.Millisecond
	t := time.NewTicker(tick)
	defer t.Stop()
	acc := 0.0
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		acc += rate * tick.Seconds()
		n := int(acc)
		acc -= float64(n)
		if n > 0 {
			c.Churn(n)
		}
	}
}

// Churn applies n events: ~70% modifications, the rest delete+create pairs.
func (c *Cluster) Churn(n int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.pods) == 0 {
		return
	}
	pick := func() int {
		for try := 0; try < 20; try++ {
			if i := c.rng.IntN(len(c.pods)); c.pods[i].state != stDead {
				return i
			}
		}
		return -1
	}
	for ev := 0; ev < n; ev++ {
		i := pick()
		if i < 0 {
			return
		}
		p := &c.pods[i]
		if c.rng.IntN(100) < 70 || ev == n-1 {
			c.rv++
			p.rv = c.rv
			switch {
			case p.state == stRunning && c.rng.IntN(3) == 0:
				p.state, p.restarts = stCrash, p.restarts+1
			case p.state == stRunning:
				p.restarts++
			case p.state == stCrash && c.rng.IntN(2) == 0:
				p.state = stRunning
			case p.state == stCrash:
				p.restarts++
			case p.state == stPending || p.state == stImagePull:
				p.state = stRunning
			default:
				p.state = stRunning
			}
			c.emitLocked(watch.Modified, *p)
			continue
		}
		// delete + create (a replaced pod gets a new uid and name)
		c.rv++
		dead := *p
		dead.rv = c.rv
		c.emitLocked(watch.Deleted, dead)
		np := c.newPod(int(dead.app), c.rng.IntN(100000))
		c.pods[i] = np
		c.emitLocked(watch.Added, np)
		ev++
	}
}

// Len returns the live pod count.
func (c *Cluster) Len() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	n := 0
	for i := range c.pods {
		if c.pods[i].state != stDead {
			n++
		}
	}
	return n
}
