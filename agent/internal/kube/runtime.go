package kube

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/httpstream"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/portforward"
	"k8s.io/client-go/tools/remotecommand"
	"k8s.io/client-go/transport/spdy"
	utilexec "k8s.io/client-go/util/exec"
)

// Runtime is what streams, drain and Helm need from one kubeconfig context beyond Conn: a typed
// clientset, pod logs, exec/attach, port-forward, and the rest config (Helm builds its own clients from it).
// The real ClientConn implements it with client-go; fake mode supplies synthetic pods and a fake clientset.
type Runtime interface {
	Clientset() kubernetes.Interface
	// RESTConfig is nil in fake mode.
	RESTConfig() *rest.Config
	PodLogs(ctx context.Context, ns, pod string, opts *corev1.PodLogOptions) (io.ReadCloser, error)
	Exec(ctx context.Context, req ExecRequest) error
	// PortForward binds 127.0.0.1:localPort (0 = pick a free port) and forwards to pod:remotePort until Stop.
	PortForward(ctx context.Context, ns, pod string, remotePort, localPort int) (*Forward, error)
	// DialPod opens the API server's port-forward tunnel to pod and returns a dialer for connections to
	// pod:remotePort carried over it. Nothing is bound on the machine: use it where a loopback listener would expose
	// an unauthenticated service to every local process (Hubble relay).
	DialPod(ctx context.Context, ns, pod string, remotePort int) (*PodDialer, error)
}

// PodDialer dials connections to one pod port over a single port-forward tunnel (see Runtime.DialPod).
type PodDialer struct {
	// DialContext opens one connection (one stream pair on the tunnel); its signature fits grpc.WithContextDialer
	// and net/http transports, which ignore the address.
	DialContext func(ctx context.Context, addr string) (net.Conn, error)
	// Done is closed when the tunnel ended; Close ends it (and every connection on it).
	Done  <-chan struct{}
	Close func()
}

// ExecRequest runs a command in (Attach: attaches to) a container. Stdin may be nil. With TTY, stderr is
// merged into stdout by the API server.
type ExecRequest struct {
	Namespace, Pod, Container string
	Command                   []string
	Attach                    bool
	TTY                       bool
	Stdin                     io.Reader
	Stdout, Stderr            io.Writer
	Resize                    remotecommand.TerminalSizeQueue
}

// Forward is a running local port-forward.
type Forward struct {
	LocalPort int
	// Done is closed when the forward ended; Err (valid after Done) is why (nil after Stop).
	Done <-chan struct{}
	Err  func() error
	Stop func()
}

// RuntimeOf returns the Runtime of a Conn (the real and the fake backends implement it).
func RuntimeOf(c Conn) (Runtime, bool) {
	r, ok := c.(Runtime)
	return r, ok
}

func (c *ClientConn) Clientset() kubernetes.Interface { return c.cs }
func (c *ClientConn) RESTConfig() *rest.Config        { return rest.CopyConfig(c.cfg) }

func (c *ClientConn) PodLogs(ctx context.Context, ns, pod string, opts *corev1.PodLogOptions) (io.ReadCloser, error) {
	return c.cs.CoreV1().Pods(ns).GetLogs(pod, opts).Stream(ctx)
}

// ExitCode extracts the exit code of a failed exec (ok false: not an exit error).
func ExitCode(err error) (int, bool) {
	var ce utilexec.CodeExitError
	if errors.As(err, &ce) {
		return ce.Code, true
	}
	var ee utilexec.ExitError
	if errors.As(err, &ee) && ee.Exited() {
		return ee.ExitStatus(), true
	}
	return 0, false
}

func (c *ClientConn) Exec(ctx context.Context, r ExecRequest) error {
	sub := "exec"
	if r.Attach {
		sub = "attach"
	}
	req := c.cs.CoreV1().RESTClient().Post().Resource("pods").Name(r.Pod).Namespace(r.Namespace).SubResource(sub)
	if r.Attach {
		req.VersionedParams(&corev1.PodAttachOptions{Container: r.Container, Stdin: r.Stdin != nil, Stdout: true, Stderr: !r.TTY, TTY: r.TTY}, scheme.ParameterCodec)
	} else {
		req.VersionedParams(&corev1.PodExecOptions{Container: r.Container, Command: r.Command, Stdin: r.Stdin != nil, Stdout: true, Stderr: !r.TTY, TTY: r.TTY}, scheme.ParameterCodec)
	}
	cfg := rest.CopyConfig(c.cfg)
	// WebSocket (v5.channel.k8s.io) first, SPDY on API servers that refuse the upgrade.
	wsExec, err := remotecommand.NewWebSocketExecutor(cfg, http.MethodGet, req.URL().String())
	if err != nil {
		return err
	}
	spdyExec, err := remotecommand.NewSPDYExecutor(cfg, http.MethodPost, req.URL())
	if err != nil {
		return err
	}
	ex, err := remotecommand.NewFallbackExecutor(wsExec, spdyExec, func(err error) bool {
		return httpstream.IsUpgradeFailure(err) || httpstream.IsHTTPSProxyError(err)
	})
	if err != nil {
		return err
	}
	opts := remotecommand.StreamOptions{Stdin: r.Stdin, Stdout: r.Stdout, Stderr: r.Stderr, Tty: r.TTY}
	if r.TTY {
		opts.Stderr = nil
		opts.TerminalSizeQueue = r.Resize
	}
	return ex.StreamWithContext(ctx, opts)
}

func (c *ClientConn) PortForward(ctx context.Context, ns, pod string, remotePort, localPort int) (*Forward, error) {
	cfg := rest.CopyConfig(c.cfg)
	u := c.cs.CoreV1().RESTClient().Post().Resource("pods").Namespace(ns).Name(pod).SubResource("portforward").URL()
	dialer, err := portForwardDialer(cfg, u)
	if err != nil {
		return nil, err
	}
	stop, ready := make(chan struct{}), make(chan struct{})
	pf, err := portforward.NewOnAddresses(dialer, []string{"127.0.0.1"}, []string{itoa(localPort) + ":" + itoa(remotePort)}, stop, ready, io.Discard, io.Discard)
	if err != nil {
		return nil, err
	}
	done := make(chan struct{})
	var ferr error
	go func() {
		ferr = pf.ForwardPorts()
		close(done)
	}()
	select {
	case <-ready:
	case <-done:
		return nil, ferr
	case <-ctx.Done():
		close(stop)
		return nil, ctx.Err()
	}
	ports, err := pf.GetPorts()
	if err != nil || len(ports) == 0 {
		close(stop)
		return nil, errors.New("port-forward: no local port bound")
	}
	var once = make(chan struct{}, 1)
	f := &Forward{LocalPort: int(ports[0].Local), Done: done, Err: func() error { <-done; return ferr },
		Stop: func() {
			select {
			case once <- struct{}{}:
				close(stop)
			default:
			}
		}}
	go func() {
		select {
		case <-ctx.Done():
			f.Stop()
		case <-done:
		}
	}()
	return f, nil
}

// DialPod implements Runtime: the portforward protocol (one error stream and one data stream per connection) over
// a single SPDY or WebSocket tunnel, without a local listener.
func (c *ClientConn) DialPod(ctx context.Context, ns, pod string, remotePort int) (*PodDialer, error) {
	cfg := rest.CopyConfig(c.cfg)
	u := c.cs.CoreV1().RESTClient().Post().Resource("pods").Namespace(ns).Name(pod).SubResource("portforward").URL()
	dialer, err := portForwardDialer(cfg, u)
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	conn, _, err := dialer.Dial(portforward.PortForwardProtocolV1Name)
	if err != nil {
		return nil, err
	}
	return newPodDialer(conn, ns, pod, remotePort), nil
}

func newPodDialer(conn httpstream.Connection, ns, pod string, remotePort int) *PodDialer {
	done := make(chan struct{})
	go func() {
		<-conn.CloseChan()
		close(done)
	}()
	var reqID atomic.Int64
	addr := podAddr{ns + "/" + pod + ":" + itoa(remotePort)}
	return &PodDialer{
		Done:  done,
		Close: func() { _ = conn.Close() },
		DialContext: func(ctx context.Context, _ string) (net.Conn, error) {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			select {
			case <-done:
				return nil, errors.New("port-forward tunnel closed")
			default:
			}
			h := http.Header{}
			h.Set(corev1.PortHeader, itoa(remotePort))
			h.Set(corev1.PortForwardRequestIDHeader, strconv.FormatInt(reqID.Add(1)-1, 10))
			h.Set(corev1.StreamType, corev1.StreamTypeError)
			errStream, err := conn.CreateStream(h)
			if err != nil {
				return nil, fmt.Errorf("port-forward error stream: %w", err)
			}
			_ = errStream.Close() // nothing is written to it
			h.Set(corev1.StreamType, corev1.StreamTypeData)
			dataStream, err := conn.CreateStream(h)
			if err != nil {
				conn.RemoveStreams(errStream)
				return nil, fmt.Errorf("port-forward data stream: %w", err)
			}
			pc := &podConn{conn: conn, data: dataStream, errs: errStream, addr: addr}
			go pc.watchErrors()
			return pc, nil
		},
	}
}

type podAddr struct{ s string }

func (podAddr) Network() string  { return "k8s-portforward" }
func (a podAddr) String() string { return a.s }

// podConn is one connection over a port-forward tunnel: a net.Conn on its data stream. The remote end reports
// failures (connection refused inside the pod) on the error stream, which surfaces as the Read error.
type podConn struct {
	conn       httpstream.Connection
	data, errs httpstream.Stream
	addr       podAddr
	once       sync.Once
	mu         sync.Mutex
	remoteErr  error
}

func (p *podConn) watchErrors() {
	msg, err := io.ReadAll(p.errs)
	switch {
	case err != nil || len(msg) == 0:
		return
	}
	p.mu.Lock()
	p.remoteErr = fmt.Errorf("forwarding to %s: %s", p.addr, msg)
	p.mu.Unlock()
	_ = p.data.Reset() // fail the reader
}

func (p *podConn) fail(err error) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.remoteErr != nil {
		return p.remoteErr
	}
	return err
}

func (p *podConn) Read(b []byte) (int, error) {
	n, err := p.data.Read(b)
	if err != nil && n == 0 {
		err = p.fail(err)
	}
	return n, err
}

func (p *podConn) Write(b []byte) (int, error) {
	n, err := p.data.Write(b)
	if err != nil {
		err = p.fail(err)
	}
	return n, err
}

func (p *podConn) Close() error {
	p.once.Do(func() {
		_ = p.data.Reset() // discard unsent data so the tunnel is never left blocked
		p.conn.RemoveStreams(p.data, p.errs)
	})
	return nil
}

func (p *podConn) LocalAddr() net.Addr              { return p.addr }
func (p *podConn) RemoteAddr() net.Addr             { return p.addr }
func (p *podConn) SetDeadline(time.Time) error      { return nil }
func (p *podConn) SetReadDeadline(time.Time) error  { return nil }
func (p *podConn) SetWriteDeadline(time.Time) error { return nil }

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	for ; n > 0; n /= 10 {
		i--
		b[i] = byte('0' + n%10)
	}
	return string(b[i:])
}

func portForwardDialer(cfg *rest.Config, u *url.URL) (httpstream.Dialer, error) {
	rt, upgrader, err := spdy.RoundTripperFor(cfg)
	if err != nil {
		return nil, err
	}
	spdyDialer := spdy.NewDialer(upgrader, &http.Client{Transport: rt}, http.MethodPost, u)
	tunnel, err := portforward.NewSPDYOverWebsocketDialer(u, cfg)
	if err != nil {
		return nil, err
	}
	return portforward.NewFallbackDialer(tunnel, spdyDialer, func(err error) bool {
		return httpstream.IsUpgradeFailure(err) || httpstream.IsHTTPSProxyError(err)
	}), nil
}
