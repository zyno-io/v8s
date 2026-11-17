package streams

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/zyno-io/v8s/agent/internal/kube"
	"github.com/zyno-io/v8s/agent/internal/ops"
	"github.com/zyno-io/v8s/agent/pkg/protocol"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	ktesting "k8s.io/client-go/testing"
	utilexec "k8s.io/client-go/util/exec"
)

func waitClosed(t *testing.T, done <-chan struct{}, what string) {
	t.Helper()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatalf("%s: stream still running after the client went away", what)
	}
}

// Regression: a hijacked WebSocket never cancels the request context, so every stream type must end when the
// client side fails (log follow parked in Credit.Take, exec waiting for the remote).
func TestClientDisconnectEndsStreams(t *testing.T) {
	t.Run("logs parked without credit", func(t *testing.T) {
		m, _, rd := newMgr(t, pod("ns", "p1", "app"))
		src := &endless{}
		rd.logs = func(string, string, *corev1.PodLogOptions) (io.ReadCloser, error) { return src, nil }
		res, err := m.Open(context.Background(), protocol.StreamOpenParams{Type: "logs", Ref: &protocol.ObjectRef{Cluster: "c1", GVR: "v1/pods", Namespace: "ns", Name: "p1"}, Follow: true})
		if err != nil {
			t.Fatal(err)
		}
		tr, done := serve(t, m, res.StreamID)
		time.Sleep(300 * time.Millisecond) // initial credit used up, sender parked
		tr.Close(CloseNormal, "")
		waitClosed(t, done, "logs")
		deadline := time.Now().Add(2 * time.Second)
		for !src.closed.Load() && time.Now().Before(deadline) {
			time.Sleep(10 * time.Millisecond)
		}
		if !src.closed.Load() {
			t.Fatal("upstream log stream was not closed")
		}
	})
	t.Run("exec waiting for the remote", func(t *testing.T) {
		m, _, rd := newMgr(t, pod("ns", "p1", "app"))
		cause := make(chan error, 1)
		rd.execCtx = func(ctx context.Context, _ kube.ExecRequest) error {
			<-ctx.Done()
			cause <- context.Cause(ctx)
			return ctx.Err()
		}
		res, err := m.Open(context.Background(), protocol.StreamOpenParams{Type: "exec", Ref: &protocol.ObjectRef{Cluster: "c1", Namespace: "ns", Name: "p1"}, Command: []string{"sh"}})
		if err != nil {
			t.Fatal(err)
		}
		tr, done := serve(t, m, res.StreamID)
		time.Sleep(100 * time.Millisecond)
		tr.Close(CloseNormal, "")
		waitClosed(t, done, "exec")
		select {
		case c := <-cause:
			if c == nil || !strings.Contains(c.Error(), "client disconnected") {
				t.Fatalf("cause %v", c)
			}
		case <-time.After(time.Second):
			t.Fatal("remote exec context was not cancelled")
		}
	})
}

// Regression: one idle source must not throttle the merged stream to one line per wait window.
func TestMergeThroughputWithOneIdleSource(t *testing.T) {
	const n = 200
	quiet := make(chan Line) // never produces, never closes
	busy := make(chan Line, n)
	base := time.Now()
	for i := 0; i < n; i++ {
		busy <- Line{TS: base.Add(time.Duration(i) * time.Millisecond), Src: "b", Text: []byte("x\n")}
	}
	close(busy)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var got atomic.Int32
	done := make(chan struct{})
	go func() {
		_ = Merge(ctx, []<-chan Line{quiet, busy}, 100*time.Millisecond, func(Line) error { got.Add(1); return nil })
		close(done)
	}()
	deadline := time.Now().Add(1500 * time.Millisecond) // sticky expiry: ~one wait window, not n of them (20 s)
	for got.Load() < n && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if got.Load() != n {
		t.Fatalf("only %d/%d lines in 1.5 s with an idle source", got.Load(), n)
	}
}

// A late source is waited for again once it delivers: order across sources is kept after it recovers.
func TestMergeLateSourceRejoins(t *testing.T) {
	a, b := make(chan Line, 4), make(chan Line, 4)
	base := time.Now()
	var mu sync.Mutex
	var got []string
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		_ = Merge(ctx, []<-chan Line{a, b}, 50*time.Millisecond, func(l Line) error { mu.Lock(); got = append(got, l.Src); mu.Unlock(); return nil })
	}()
	a <- Line{TS: base, Src: "a1"}
	time.Sleep(200 * time.Millisecond) // b expired: late
	b <- Line{TS: base.Add(time.Second), Src: "b1"}
	time.Sleep(50 * time.Millisecond) // b delivered: no longer late, a's next line waits for b's next
	a <- Line{TS: base.Add(2 * time.Second), Src: "a2"}
	b <- Line{TS: base.Add(1500 * time.Millisecond), Src: "b2"}
	time.Sleep(300 * time.Millisecond)
	mu.Lock()
	defer mu.Unlock()
	if strings.Join(got, ",") != "a1,b1,b2,a2" {
		t.Fatalf("%v", got)
	}
}

// Regression: a leftover stdin reader of a failed shell attempt must not eat the keystrokes of the next one.
func TestShellDiscoveryUsesAFreshStdinPerAttempt(t *testing.T) {
	m, _, rd := newMgr(t, pod("ns", "p1", "app"))
	leaked := make(chan string, 8)
	var bashes atomic.Int32
	rd.execFn = func(req kube.ExecRequest) error {
		if req.Command[0] == "/bin/bash" {
			bashes.Add(1)
			go func() { // like client-go's stdin copier outliving a failed attempt
				buf := make([]byte, 16)
				for {
					n, err := req.Stdin.Read(buf)
					if n > 0 {
						leaked <- string(buf[:n])
					}
					if err != nil {
						return
					}
				}
			}()
			return utilexec.CodeExitError{Err: errors.New("no such file or directory"), Code: 126}
		}
		return execEcho(req)
	}
	res, err := m.Open(context.Background(), protocol.StreamOpenParams{Type: "exec", Ref: &protocol.ObjectRef{Cluster: "c1", Namespace: "ns", Name: "p1"}})
	if err != nil {
		t.Fatal(err)
	}
	tr, _ := serve(t, m, res.StreamID)
	// type as soon as the /bin/sh ready frame shows up
	sent := false
	deadline := time.After(3 * time.Second)
	for {
		select {
		case <-deadline:
			t.Fatal("no echo from the second shell")
		default:
		}
		f, ok := tr.next(100 * time.Millisecond)
		if !ok {
			continue
		}
		if f.binary {
			if f.data[0] == protocol.StreamChanStdout && string(f.data[1:]) == "out:hello" {
				break
			}
			continue
		}
		var sm protocol.StreamServerMsg
		_ = json.Unmarshal(f.data, &sm)
		if sm.Ready != nil && sm.Ready.Shell == "/bin/sh" && !sent {
			sent = true
			tr.in <- msg{true, append([]byte{protocol.StreamChanStdin}, "hello"...)}
		}
	}
	select {
	case s := <-leaked:
		t.Fatalf("the failed attempt's reader stole %q", s)
	default:
	}
}

type mapRouterStub struct {
	*router
	called atomic.Int32
}

func (r *mapRouterStub) MapRouted(_ context.Context, err error, rt *ops.Routed, verb, gvr, sub, _, _ string) error {
	r.called.Add(1)
	if rt.ViaWrite && apierrors.IsForbidden(err) {
		return ops.NewError("write_unavailable", "%s %s/%s", verb, gvr, sub)
	}
	return ops.MapError(err, rt.ViaWrite)
}

// Regression: stream errors through the write context are classified by the router (403 -> write_unavailable).
func TestStreamErrorsMappedThroughRouter(t *testing.T) {
	m, r, rd := newMgr(t, pod("ns", "p1", "app"))
	rs := &mapRouterStub{router: r}
	m.cfg.Router = rs
	r.viaWrite = true
	rd.cs.PrependReactor("get", "pods", func(ktesting.Action) (bool, runtime.Object, error) {
		return true, nil, apierrors.NewForbidden(schema.GroupResource{Resource: "pods"}, "p1", errors.New("no"))
	})
	_, err := m.Open(context.Background(), protocol.StreamOpenParams{Type: "exec", Ref: &protocol.ObjectRef{Cluster: "c1", Namespace: "ns", Name: "p1"}})
	var oe *ops.Error
	if !errors.As(err, &oe) || oe.Body.Code != "write_unavailable" || rs.called.Load() != 1 {
		t.Fatalf("%v", err)
	}
}

// Regression: a debug container attaches to the ephemeral container's own process (kubectl debug) instead of
// exec'ing a second shell inside it.
func TestDebugAttachesToEphemeralContainer(t *testing.T) {
	m, _, rd := newMgr(t, pod("ns", "p1", "app"))
	var mu sync.Mutex
	name := ""
	rd.cs.PrependReactor("patch", "pods", func(a ktesting.Action) (bool, runtime.Object, error) {
		var body struct {
			Spec struct {
				EphemeralContainers []struct{ Name string } `json:"ephemeralContainers"`
			} `json:"spec"`
		}
		_ = json.Unmarshal(a.(ktesting.PatchAction).GetPatch(), &body)
		mu.Lock()
		name = body.Spec.EphemeralContainers[0].Name
		mu.Unlock()
		return true, pod("ns", "p1", "app"), nil
	})
	rd.cs.PrependReactor("get", "pods", func(ktesting.Action) (bool, runtime.Object, error) {
		mu.Lock()
		defer mu.Unlock()
		p := pod("ns", "p1", "app")
		if name != "" {
			p.Status.EphemeralContainerStatuses = []corev1.ContainerStatus{{Name: name, State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}}}}
		}
		return true, p, nil
	})
	reqs := make(chan kube.ExecRequest, 4)
	rd.execFn = func(req kube.ExecRequest) error { reqs <- req; return nil }
	res, err := m.Open(context.Background(), protocol.StreamOpenParams{Type: "debug", Ref: &protocol.ObjectRef{Cluster: "c1", Namespace: "ns", Name: "p1"}, Image: "busybox", Target: "app"})
	if err != nil {
		t.Fatal(err)
	}
	_, done := serve(t, m, res.StreamID)
	select {
	case req := <-reqs:
		mu.Lock()
		defer mu.Unlock()
		if !req.Attach || !req.TTY || req.Stdin == nil || req.Container != name || name == "" {
			t.Fatalf("not an attach to the ephemeral container: %+v (name %q)", req, name)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("no runtime call")
	}
	waitClosed(t, done, "debug")
	select {
	case req := <-reqs:
		t.Fatalf("a second command ran: %+v", req)
	default:
	}
}

// Regression: a forward whose pod was deleted backs off exponentially and gives up (state stopped + error)
// instead of retrying every second forever.
func TestPortForwardGivesUpWithBackoff(t *testing.T) {
	oldR, oldM, oldN := forwardRetry, forwardRetryMax, forwardMaxTries
	forwardRetry, forwardRetryMax, forwardMaxTries = 4*time.Millisecond, 16*time.Millisecond, 8
	defer func() { forwardRetry, forwardRetryMax, forwardMaxTries = oldR, oldM, oldN }()

	m, _, rd := newMgr(t)
	closed := make(chan struct{})
	close(closed)
	cur := &kube.Forward{LocalPort: 4000, Done: closed, Err: func() error { return nil }, Stop: func() {}}
	f := &fwd{cancel: func() {}, info: protocol.PortForwardInfo{ID: "f1", Ref: protocol.ObjectRef{Namespace: "ns", Name: "gone"}, LocalPort: 4000, State: "running"}}
	var stamps []time.Time
	resolve := func(context.Context) (string, int, error) {
		stamps = append(stamps, time.Now())
		return "", 0, apierrors.NewNotFound(schema.GroupResource{Resource: "pods"}, "gone")
	}
	done := make(chan struct{})
	go func() { m.pf.supervise(context.Background(), f, rd, cur, resolve); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("supervisor never gave up")
	}
	info := f.snapshot()
	if info.State != "stopped" || !strings.Contains(info.Error, "gave up after 8") || !strings.Contains(info.Error, "not found") {
		t.Fatalf("%+v", info)
	}
	if len(stamps) != 8 {
		t.Fatalf("%d attempts, want 8", len(stamps))
	}
	// the gap before the last attempts is the capped delay, clearly longer than the first one
	first, last := stamps[1].Sub(stamps[0]), stamps[7].Sub(stamps[6])
	if last < first || last < 12*time.Millisecond {
		t.Fatalf("no backoff: first gap %v, last gap %v", first, last)
	}
}
