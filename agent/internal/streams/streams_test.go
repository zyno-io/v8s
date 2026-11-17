package streams

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
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
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	k8sfake "k8s.io/client-go/kubernetes/fake"
	authv1 "k8s.io/client-go/kubernetes/typed/authorization/v1"
	"k8s.io/client-go/rest"
	utilexec "k8s.io/client-go/util/exec"
)

// ---- test doubles --------------------------------------------------------------------------

type msg struct {
	binary bool
	data   []byte
}

type memTransport struct {
	in     chan msg
	out    chan msg
	closed chan struct{}
	once   sync.Once
	code   atomic.Int32
}

func newMem() *memTransport {
	return &memTransport{in: make(chan msg, 256), out: make(chan msg, 4096), closed: make(chan struct{})}
}

func (t *memTransport) Read(ctx context.Context) (bool, []byte, error) {
	select {
	case m := <-t.in:
		return m.binary, m.data, nil
	case <-t.closed:
		return false, nil, io.EOF
	case <-ctx.Done():
		return false, nil, ctx.Err()
	}
}

func (t *memTransport) Write(ctx context.Context, binary bool, data []byte) error {
	select {
	case t.out <- msg{binary, append([]byte(nil), data...)}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	case <-t.closed:
		return io.ErrClosedPipe
	}
}

func (t *memTransport) Close(code int, _ string) {
	t.code.Store(int32(code))
	t.once.Do(func() { close(t.closed) })
}

func (t *memTransport) next(d time.Duration) (msg, bool) {
	select {
	case m := <-t.out:
		return m, true
	case <-time.After(d):
		return msg{}, false
	}
}

type stubConn struct {
	kube.Conn
	cs      *k8sfake.Clientset
	logs    func(ns, pod string, o *corev1.PodLogOptions) (io.ReadCloser, error)
	execFn  func(req kube.ExecRequest) error
	execCmd chan []string
	// execCtx, when set, replaces execFn and receives the context the runtime was called with.
	execCtx func(ctx context.Context, req kube.ExecRequest) error
}

func (c *stubConn) Clientset() kubernetes.Interface { return c.cs }
func (c *stubConn) RESTConfig() *rest.Config        { return nil }
func (c *stubConn) PodLogs(_ context.Context, ns, pod string, o *corev1.PodLogOptions) (io.ReadCloser, error) {
	return c.logs(ns, pod, o)
}
func (c *stubConn) Exec(ctx context.Context, r kube.ExecRequest) error {
	if c.execCmd != nil {
		c.execCmd <- r.Command
	}
	if c.execCtx != nil {
		return c.execCtx(ctx, r)
	}
	return c.execFn(r)
}
func (c *stubConn) DialPod(context.Context, string, string, int) (*kube.PodDialer, error) {
	return nil, errors.New("not supported")
}

func (c *stubConn) PortForward(context.Context, string, string, int, int) (*kube.Forward, error) {
	return nil, errors.New("no")
}
func (c *stubConn) Dynamic() dynamic.Interface                     { return nil }
func (c *stubConn) Authz() authv1.SelfSubjectAccessReviewInterface { return nil }

type router struct {
	conns    map[string]*stubConn
	viaWrite bool
	denied   bool
	last     atomic.Value
}

func (r *router) Route(ctx context.Context, cluster, verb, gvr, sub, ns, name string) (*ops.Routed, error) {
	r.last.Store(fmt.Sprintf("%s %s/%s", verb, gvr, sub))
	if r.denied {
		return nil, ops.NewError("forbidden", "denied")
	}
	id := cluster
	if r.viaWrite {
		id = cluster + "-write"
	}
	return &ops.Routed{ContextID: id, Conn: r.conns[id], ViaWrite: r.viaWrite}, nil
}

func pod(ns, name string, containers ...string) *corev1.Pod {
	p := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: name, Labels: map[string]string{"app": "web"}}, Status: corev1.PodStatus{Phase: corev1.PodRunning}}
	for _, c := range containers {
		p.Spec.Containers = append(p.Spec.Containers, corev1.Container{Name: c})
	}
	return p
}

func newMgr(t *testing.T, cs ...*corev1.Pod) (*Manager, *router, *stubConn) {
	t.Helper()
	var objs []runtime.Object
	for _, p := range cs {
		objs = append(objs, p)
	}
	rd := &stubConn{cs: k8sfake.NewSimpleClientset(objs...)}
	r := &router{conns: map[string]*stubConn{"c1": rd, "c1-write": rd}}
	m := New(Config{Router: r, Settings: func() protocol.Settings { return protocol.Settings{} }, AllowPortForward: true, User: "tester"})
	return m, r, rd
}

func serve(t *testing.T, m *Manager, id string) (*memTransport, <-chan struct{}) {
	t.Helper()
	tr := newMem()
	done := make(chan struct{})
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go func() { _ = m.Serve(ctx, id, tr); close(done) }()
	return tr, done
}

// ---- framing, credit, batching -------------------------------------------------------------

func TestBatcherFlushesBySizeAndDelay(t *testing.T) {
	in := make(chan []byte)
	var mu sync.Mutex
	var frames [][]byte
	b := &Batcher{MaxBytes: 100, MaxDelay: 40 * time.Millisecond, Flush: func(_ context.Context, f []byte) error {
		mu.Lock()
		frames = append(frames, f)
		mu.Unlock()
		return nil
	}}
	done := make(chan error, 1)
	go func() { done <- b.Run(context.Background(), in) }()
	line := []byte(strings.Repeat("x", 29) + "\n") // 30 bytes
	for i := 0; i < 7; i++ {
		in <- line
	}
	time.Sleep(10 * time.Millisecond)
	mu.Lock()
	if len(frames) != 2 || len(frames[0]) != 90 {
		t.Fatalf("size flush: %d frames %v", len(frames), lens(frames))
	}
	mu.Unlock()
	time.Sleep(120 * time.Millisecond) // the delay flushes the remainder: <= 100 ms (plus scheduling)
	mu.Lock()
	if len(frames) != 3 || len(frames[2]) != 30 {
		t.Fatalf("delay flush: %v", lens(frames))
	}
	mu.Unlock()
	// one line larger than a frame is split, nothing is lost
	in <- []byte(strings.Repeat("y", 250) + "\n")
	close(in)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	total := 0
	for _, f := range frames[3:] {
		if len(f) > 100 {
			t.Fatalf("frame of %d bytes", len(f))
		}
		total += len(f)
	}
	if total != 251 {
		t.Fatalf("total %d", total)
	}
}

func lens(fs [][]byte) []int {
	var o []int
	for _, f := range fs {
		o = append(o, len(f))
	}
	return o
}

func TestCreditGate(t *testing.T) {
	c := NewCredit(10)
	ctx := context.Background()
	if err := c.Take(ctx, 25); err != nil { // overshoot by one frame is allowed
		t.Fatal(err)
	}
	if c.Balance() != -15 {
		t.Fatalf("balance %d", c.Balance())
	}
	got := make(chan struct{})
	go func() { _ = c.Take(ctx, 1); close(got) }()
	select {
	case <-got:
		t.Fatal("took credit with a negative balance")
	case <-time.After(50 * time.Millisecond):
	}
	c.Add(10) // -5: still blocked
	select {
	case <-got:
		t.Fatal("took credit at -5")
	case <-time.After(50 * time.Millisecond):
	}
	c.Add(10)
	select {
	case <-got:
	case <-time.After(time.Second):
		t.Fatal("did not resume")
	}
	cctx, cancel := context.WithCancel(ctx)
	cancel()
	c2 := NewCredit(0)
	if err := c2.Take(cctx, 1); err == nil {
		t.Fatal("cancelled wait must fail")
	}
}

type endless struct {
	n      atomic.Int64
	closed atomic.Bool
}

func (e *endless) Read(p []byte) (int, error) {
	if e.closed.Load() {
		return 0, io.EOF
	}
	line := "2026-01-01T00:00:00.000000000Z level=info msg=\"a log line of some length to count bytes\"\n"
	n := copy(p, strings.Repeat(line, 4))
	e.n.Add(int64(n))
	return n, nil
}
func (e *endless) Close() error { e.closed.Store(true); return nil }

// Credit-based flow control: with no client credit the agent stops reading from the API server stream.
func TestLogCreditStopsAndResumesReading(t *testing.T) {
	m, _, rd := newMgr(t, pod("ns", "p1", "app"))
	src := &endless{}
	rd.logs = func(string, string, *corev1.PodLogOptions) (io.ReadCloser, error) { return src, nil }
	res, err := m.Open(context.Background(), protocol.StreamOpenParams{Type: "logs", Ref: &protocol.ObjectRef{Cluster: "c1", GVR: "v1/pods", Namespace: "ns", Name: "p1"}, Follow: true})
	if err != nil {
		t.Fatal(err)
	}
	tr, _ := serve(t, m, res.StreamID)
	var received int64
	drain := func(d time.Duration) {
		for {
			f, ok := tr.next(d)
			if !ok {
				return
			}
			if f.binary {
				if len(f.data) > protocol.LogBatchBytes {
					t.Fatalf("frame of %d bytes", len(f.data))
				}
				received += int64(len(f.data))
			}
		}
	}
	drain(300 * time.Millisecond)
	if received < protocol.InitialLogCredit || received > protocol.InitialLogCredit+2*protocol.LogBatchBytes+8192 {
		t.Fatalf("sent %d bytes without further credit (initial %d)", received, protocol.InitialLogCredit)
	}
	stalled := src.n.Load()
	time.Sleep(200 * time.Millisecond)
	if grew := src.n.Load() - stalled; grew > 0 {
		t.Fatalf("upstream still read %d bytes while out of credit", grew)
	}
	before := received
	b, _ := json.Marshal(protocol.StreamClientMsg{Credit: 1 << 20})
	tr.in <- msg{false, b}
	drain(300 * time.Millisecond)
	if received-before < 512*1024 {
		t.Fatalf("credit did not resume the stream: +%d", received-before)
	}
}

func TestLogsSingleStreamEndsWithEndFrame(t *testing.T) {
	m, _, rd := newMgr(t, pod("ns", "p1", "app", "side"))
	var opts *corev1.PodLogOptions
	rd.logs = func(_, _ string, o *corev1.PodLogOptions) (io.ReadCloser, error) {
		opts = o
		return io.NopCloser(strings.NewReader("one\ntwo\nthree")), nil // no trailing newline
	}
	res, err := m.Open(context.Background(), protocol.StreamOpenParams{Type: "logs", Ref: &protocol.ObjectRef{Cluster: "c1", GVR: "v1/pods", Namespace: "ns", Name: "p1"}, Timestamps: true})
	if err != nil {
		t.Fatal(err)
	}
	tr, _ := serve(t, m, res.StreamID)
	var body bytes.Buffer
	var end protocol.StreamServerMsg
	for {
		f, ok := tr.next(2 * time.Second)
		if !ok {
			t.Fatal("no end frame")
		}
		if f.binary {
			body.Write(f.data)
			continue
		}
		_ = json.Unmarshal(f.data, &end)
		break
	}
	if body.String() != "one\ntwo\nthree\n" || !end.End || end.Error != "" {
		t.Fatalf("%q %+v", body.String(), end)
	}
	if opts.Container != "app" || opts.TailLines == nil || *opts.TailLines != protocol.DefaultLogTailLines || !opts.Timestamps {
		t.Fatalf("options %+v", opts)
	}
	// the id is single-use
	if err := m.Serve(context.Background(), res.StreamID, newMem()); !errors.Is(err, ErrBadStream) {
		t.Fatalf("reuse: %v", err)
	}
}

func TestMergeOrdersByTimestamp(t *testing.T) {
	ts := func(s int) time.Time { return time.Date(2026, 1, 1, 0, 0, s, 0, time.UTC) }
	mkch := func(src string, secs ...int) <-chan Line {
		ch := make(chan Line, len(secs))
		for _, s := range secs {
			ch <- Line{TS: ts(s), Src: src, Text: []byte(fmt.Sprintf("%s@%d\n", src, s))}
		}
		close(ch)
		return ch
	}
	var got []string
	err := Merge(context.Background(), []<-chan Line{mkch("a", 1, 4, 5), mkch("b", 2, 3, 9), mkch("c", 0, 8)}, time.Second, func(l Line) error {
		got = append(got, strings.TrimSpace(string(l.Text)))
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	want := "c@0 a@1 b@2 b@3 a@4 a@5 c@8 b@9"
	if strings.Join(got, " ") != want {
		t.Fatalf("got %v want %s", got, want)
	}
}

func TestMergeDoesNotStallOnASilentSource(t *testing.T) {
	quiet := make(chan Line) // never produces
	busy := make(chan Line, 1)
	busy <- Line{TS: time.Now(), Src: "b", Text: []byte("x\n")}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	got := make(chan string, 1)
	go func() {
		_ = Merge(ctx, []<-chan Line{quiet, busy}, 50*time.Millisecond, func(l Line) error { got <- l.Src; return nil })
	}()
	select {
	case s := <-got:
		if s != "b" {
			t.Fatal(s)
		}
	case <-time.After(time.Second):
		t.Fatal("a silent source blocked the merge")
	}
}

func TestMultiPodLogsMergedWithPrefixAndTimestampStripped(t *testing.T) {
	m, _, rd := newMgr(t, pod("ns", "web-1", "app"), pod("ns", "web-2", "app"))
	rd.logs = func(_, p string, o *corev1.PodLogOptions) (io.ReadCloser, error) {
		if !o.Timestamps {
			t.Error("merging needs timestamps")
		}
		if p == "web-1" {
			return io.NopCloser(strings.NewReader("2026-01-01T00:00:01.000000000Z first\n2026-01-01T00:00:03.000000000Z third\n")), nil
		}
		return io.NopCloser(strings.NewReader("2026-01-01T00:00:02.000000000Z second\n")), nil
	}
	res, err := m.Open(context.Background(), protocol.StreamOpenParams{Type: "logs", Selector: &protocol.LogSelector{Cluster: "c1", Namespace: "ns", LabelSelector: "app=web"}})
	if err != nil {
		t.Fatal(err)
	}
	tr, _ := serve(t, m, res.StreamID)
	var body bytes.Buffer
	for {
		f, ok := tr.next(2 * time.Second)
		if !ok || !f.binary {
			break
		}
		body.Write(f.data)
	}
	if body.String() != "web-1/app\tfirst\nweb-2/app\tsecond\nweb-1/app\tthird\n" {
		t.Fatalf("%q", body.String())
	}
}

func TestLogDownloadStreamsWholeLog(t *testing.T) {
	m, _, rd := newMgr(t, pod("ns", "p1", "app"))
	var opts *corev1.PodLogOptions
	rd.logs = func(_, _ string, o *corev1.PodLogOptions) (io.ReadCloser, error) {
		opts = o
		return io.NopCloser(strings.NewReader(strings.Repeat("line\n", 1000))), nil
	}
	res, err := m.Open(context.Background(), protocol.StreamOpenParams{Type: "logs", Download: true, Follow: true, Ref: &protocol.ObjectRef{Cluster: "c1", Namespace: "ns", Name: "p1"}})
	if err != nil || res.DownloadURL == "" {
		t.Fatalf("%v %+v", err, res)
	}
	var buf bytes.Buffer
	if err := m.Download(context.Background(), res.StreamID, &buf); err != nil || buf.Len() != 5000 {
		t.Fatalf("%v %d", err, buf.Len())
	}
	if opts.TailLines != nil || opts.Follow {
		t.Fatalf("a download is the full log, not a follow: %+v", opts)
	}
	if err := m.Download(context.Background(), res.StreamID, &buf); !errors.Is(err, ErrBadStream) {
		t.Fatal("download id is one-shot")
	}
}

// ---- exec ----------------------------------------------------------------------------------

func TestNotFoundDetection(t *testing.T) {
	yes := []error{
		utilexec.CodeExitError{Err: errors.New("x"), Code: 127},
		utilexec.CodeExitError{Err: errors.New("x"), Code: 126},
		errors.New(`OCI runtime exec failed: exec failed: unable to start container process: exec: "/bin/bash": stat /bin/bash: no such file or directory: unknown`),
		errors.New(`exec: "/busybox/sh": executable file not found in $PATH`),
	}
	for _, e := range yes {
		if !notFound(e) {
			t.Errorf("should fall through: %v", e)
		}
	}
	for _, e := range []error{nil, errors.New("connection reset"), utilexec.CodeExitError{Err: errors.New("x"), Code: 1}} {
		if notFound(e) {
			t.Errorf("must not fall through: %v", e)
		}
	}
}

func TestShellDiscoveryOrder(t *testing.T) {
	var tried []string
	run := func(cmd []string) (bool, error) {
		tried = append(tried, cmd[0])
		if cmd[0] != "/busybox/sh" {
			return false, utilexec.CodeExitError{Err: errors.New("no such file or directory"), Code: 127}
		}
		return true, nil
	}
	got, err := DiscoverShell(context.Background(), shellCandidates(nil), run, nil)
	if err != nil || got[0] != "/busybox/sh" || strings.Join(tried, ",") != "/bin/bash,/bin/sh,/busybox/sh" {
		t.Fatalf("%v %v %v", got, err, tried)
	}
	// a shell that started (produced output) and then failed is not retried
	tried = nil
	_, err = DiscoverShell(context.Background(), shellCandidates(nil), func(cmd []string) (bool, error) {
		tried = append(tried, cmd[0])
		return true, utilexec.CodeExitError{Err: errors.New("x"), Code: 127}
	}, nil)
	if err == nil || len(tried) != 1 {
		t.Fatalf("%v %v", err, tried)
	}
	// all fail: the last error
	_, err = DiscoverShell(context.Background(), shellCandidates(nil), func([]string) (bool, error) {
		return false, utilexec.CodeExitError{Err: errors.New("none"), Code: 127}
	}, nil)
	if err == nil {
		t.Fatal("expected an error")
	}
	// an explicit command is run as given
	got, _ = DiscoverShell(context.Background(), shellCandidates([]string{"echo", "hi"}), func(cmd []string) (bool, error) { return true, nil }, nil)
	if strings.Join(got, " ") != "echo hi" {
		t.Fatal(got)
	}
}

func execEcho(req kube.ExecRequest) error {
	buf := make([]byte, 64)
	for {
		n, err := req.Stdin.Read(buf)
		if n > 0 {
			_, _ = req.Stdout.Write(append([]byte("out:"), buf[:n]...))
			_, _ = req.Stderr.Write([]byte("err"))
		}
		if err != nil {
			return utilexec.CodeExitError{Err: errors.New("exit"), Code: 3}
		}
	}
}

func TestExecFramesAndShellFallback(t *testing.T) {
	m, _, rd := newMgr(t, pod("ns", "p1", "app"))
	rd.execCmd = make(chan []string, 8)
	rd.execFn = func(req kube.ExecRequest) error {
		if req.Command[0] == "/bin/bash" {
			return utilexec.CodeExitError{Err: errors.New("no such file or directory"), Code: 126}
		}
		if req.Container != "app" || !req.TTY {
			t.Errorf("request %+v", req)
		}
		return execEcho(req)
	}
	res, err := m.Open(context.Background(), protocol.StreamOpenParams{Type: "exec", Ref: &protocol.ObjectRef{Cluster: "c1", Namespace: "ns", Name: "p1"}})
	if err != nil {
		t.Fatal(err)
	}
	tr, done := serve(t, m, res.StreamID)
	tr.in <- msg{true, append([]byte{protocol.StreamChanStdin}, "hello"...)}
	var gotOut, gotErr bool
	var shells []string
	deadline := time.After(3 * time.Second)
	for !(gotOut && gotErr) {
		select {
		case <-deadline:
			t.Fatalf("out=%v err=%v", gotOut, gotErr)
		default:
		}
		f, ok := tr.next(200 * time.Millisecond)
		if !ok {
			continue
		}
		switch {
		case f.binary && f.data[0] == protocol.StreamChanStdout && string(f.data[1:]) == "out:hello":
			gotOut = true
		case f.binary && f.data[0] == protocol.StreamChanStderr && string(f.data[1:]) == "err":
			gotErr = true
		case !f.binary:
			var sm protocol.StreamServerMsg
			_ = json.Unmarshal(f.data, &sm)
			if sm.Ready != nil {
				shells = append(shells, sm.Ready.Shell)
			}
		}
	}
	// closing the client side ends the command: the exit frame carries the code
	tr.Close(CloseNormal, "")
	<-done
	if len(shells) < 2 || shells[0] != "/bin/bash" || shells[1] != "/bin/sh" {
		t.Fatalf("ready frames %v", shells)
	}
}

func TestExecExitFrameAndResize(t *testing.T) {
	m, _, rd := newMgr(t, pod("ns", "p1", "app"))
	sizes := make(chan [2]uint16, 4)
	rd.execFn = func(req kube.ExecRequest) error {
		if s := req.Resize.Next(); s != nil {
			sizes <- [2]uint16{s.Width, s.Height}
		}
		return utilexec.CodeExitError{Err: errors.New("command terminated with exit code 7"), Code: 7}
	}
	res, _ := m.Open(context.Background(), protocol.StreamOpenParams{Type: "exec", Command: []string{"true"}, Ref: &protocol.ObjectRef{Cluster: "c1", Namespace: "ns", Name: "p1"}})
	tr, _ := serve(t, m, res.StreamID)
	b, _ := json.Marshal(protocol.StreamClientMsg{Resize: &protocol.StreamResize{Cols: 120, Rows: 40}})
	tr.in <- msg{false, b}
	select {
	case s := <-sizes:
		if s != [2]uint16{120, 40} {
			t.Fatal(s)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("resize not forwarded")
	}
	for {
		f, ok := tr.next(2 * time.Second)
		if !ok {
			t.Fatal("no exit frame")
		}
		if f.binary {
			continue
		}
		var sm protocol.StreamServerMsg
		_ = json.Unmarshal(f.data, &sm)
		if sm.Exit != nil {
			if *sm.Exit != 7 {
				t.Fatalf("exit %d", *sm.Exit)
			}
			return
		}
	}
}

// ---- write-context routing and limits ----------------------------------------------------------

func TestStreamsRouteWriteVerbsAndReportContext(t *testing.T) {
	m, r, rd := newMgr(t, pod("ns", "p1", "app"))
	rd.execFn = func(kube.ExecRequest) error { return nil }
	r.viaWrite = true
	ref := &protocol.ObjectRef{Cluster: "c1", Namespace: "ns", Name: "p1"}
	res, err := m.Open(context.Background(), protocol.StreamOpenParams{Type: "exec", Ref: ref})
	if err != nil || res.UsedContext != "c1-write" {
		t.Fatalf("%v %+v", err, res)
	}
	if r.last.Load() != "create v1/pods/exec" {
		t.Fatalf("routed %v", r.last.Load())
	}
	if _, err = m.Open(context.Background(), protocol.StreamOpenParams{Type: "logs", Ref: ref}); err != nil || r.last.Load() != "get v1/pods/log" {
		t.Fatalf("%v %v", err, r.last.Load())
	}
	if _, err = m.Open(context.Background(), protocol.StreamOpenParams{Type: "nodeshell", Cluster: "c1", Node: "n1"}); err != nil || r.last.Load() != "create v1/pods/" {
		t.Fatalf("%v %v", err, r.last.Load())
	}
	if _, err = m.Open(context.Background(), protocol.StreamOpenParams{Type: "debug", Ref: ref, Image: "busybox", Target: "app"}); err != nil || r.last.Load() != "patch v1/pods/ephemeralcontainers" {
		t.Fatalf("%v %v", err, r.last.Load())
	}
	r.denied = true
	if _, err = m.Open(context.Background(), protocol.StreamOpenParams{Type: "exec", Ref: ref}); err == nil || !strings.Contains(err.Error(), "forbidden") {
		t.Fatalf("denied: %v", err)
	}
	if _, err = m.Open(context.Background(), protocol.StreamOpenParams{Type: "bogus"}); err == nil {
		t.Fatal("unknown type")
	}
}

func TestWriteContextStreamsHaveIdleTimeoutAndCapReadOnesDoNot(t *testing.T) {
	m, r, rd := newMgr(t, pod("ns", "p1", "app"))
	m.cfg.IdleTimeout = 100 * time.Millisecond
	m.cfg.MaxDuration = 400 * time.Millisecond
	rd.execFn = func(req kube.ExecRequest) error { _, err := io.Copy(io.Discard, req.Stdin); return err }
	ref := &protocol.ObjectRef{Cluster: "c1", Namespace: "ns", Name: "p1"}
	exitOf := func(tr *memTransport, wait time.Duration) (protocol.StreamServerMsg, bool) {
		deadline := time.Now().Add(wait)
		for time.Now().Before(deadline) {
			f, ok := tr.next(50 * time.Millisecond)
			if ok && !f.binary {
				var sm protocol.StreamServerMsg
				_ = json.Unmarshal(f.data, &sm)
				if sm.Exit != nil {
					return sm, true
				}
			}
		}
		return protocol.StreamServerMsg{}, false
	}
	// through the write context: idle after 100 ms without traffic
	r.viaWrite = true
	res, _ := m.Open(context.Background(), protocol.StreamOpenParams{Type: "exec", Ref: ref, Command: []string{"sh"}})
	tr, _ := serve(t, m, res.StreamID)
	sm, ok := exitOf(tr, 2*time.Second)
	if !ok || !strings.Contains(sm.Error, "idle timeout") {
		t.Fatalf("idle: %+v %v", sm, ok)
	}
	// traffic keeps it alive, but the cap ends it
	res, _ = m.Open(context.Background(), protocol.StreamOpenParams{Type: "exec", Ref: ref, Command: []string{"sh"}})
	tr, _ = serve(t, m, res.StreamID)
	stop, fed := make(chan struct{}), make(chan struct{})
	go func(tr *memTransport) { // own copy: tr is reassigned below
		defer close(fed)
		for {
			select {
			case <-stop:
				return
			case <-time.After(30 * time.Millisecond):
				select {
				case tr.in <- msg{true, []byte{protocol.StreamChanStdin, 'x'}}:
				case <-stop:
					return
				}
			}
		}
	}(tr)
	sm, ok = exitOf(tr, 3*time.Second)
	close(stop)
	<-fed
	if !ok || !strings.Contains(sm.Error, "maximum session length") {
		t.Fatalf("cap: %+v %v", sm, ok)
	}
	// through the read context: no limits
	r.viaWrite = false
	res, _ = m.Open(context.Background(), protocol.StreamOpenParams{Type: "exec", Ref: ref, Command: []string{"sh"}})
	tr, _ = serve(t, m, res.StreamID)
	if _, ok := exitOf(tr, 700*time.Millisecond); ok {
		t.Fatal("a read-context stream must not time out")
	}
}

func TestStreamIDExpires(t *testing.T) {
	m, _, rd := newMgr(t, pod("ns", "p1", "app"))
	rd.execFn = func(kube.ExecRequest) error { return nil }
	now := time.Now()
	m.cfg.Now = func() time.Time { return now }
	res, _ := m.Open(context.Background(), protocol.StreamOpenParams{Type: "exec", Ref: &protocol.ObjectRef{Cluster: "c1", Namespace: "ns", Name: "p1"}})
	now = now.Add(31 * time.Second)
	tr := newMem()
	if err := m.Serve(context.Background(), res.StreamID, tr); !errors.Is(err, ErrBadStream) || tr.code.Load() != ClosePolicy {
		t.Fatalf("%v %d", err, tr.code.Load())
	}
}
