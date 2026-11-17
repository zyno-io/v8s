package streams

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"sync"
	"time"

	"github.com/zyno-io/v8s/agent/internal/kube"
	"github.com/zyno-io/v8s/agent/internal/ops"
	"github.com/zyno-io/v8s/agent/pkg/protocol"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// Router picks the context a stream runs through (ops.Service: read context when allowed, else the linked
// write context, the same routing as object.action).
type Router interface {
	Route(ctx context.Context, cluster, verb, gvr, sub, ns, name string) (*ops.Routed, error)
}

// Config wires the manager to the rest of the agent.
type Config struct {
	Router Router
	// Conn returns the connection of a context id (read context id == cluster id).
	Conn     func(contextID string) (kube.Conn, error)
	Settings func() protocol.Settings
	// AllowPortForward is false in hosted mode (not built yet): a local port is meaningless there.
	AllowPortForward bool
	User             string
	Log              *slog.Logger
	Now              func() time.Time
	// IdleTimeout and MaxDuration are the limits of streams that run through a write context
	// (defaults 15 min and 60 min); tests shorten them.
	IdleTimeout, MaxDuration time.Duration
	// NodeShellTTL is activeDeadlineSeconds of node shell pods (default 3600).
	NodeShellTTL time.Duration
}

// Manager issues single-use stream ids and runs the streams behind them.
type Manager struct {
	cfg Config
	log *slog.Logger

	mu      sync.Mutex
	pending map[string]*pendingStream

	ns *nodeShells
	pf *forwards

	xmu   sync.Mutex
	extra map[string]ExtraOpener
}

type pendingStream struct {
	typ      string
	exp      time.Time
	run      func(ctx context.Context, t Transport) // interactive and log streams
	download func(ctx context.Context, w io.Writer) error
	abort    func() // releases what open acquired when the stream is never claimed
}

// New builds a Manager.
func New(cfg Config) *Manager {
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.Log == nil {
		cfg.Log = slog.New(slog.DiscardHandler)
	}
	if cfg.IdleTimeout == 0 {
		cfg.IdleTimeout = 15 * time.Minute
	}
	if cfg.MaxDuration == 0 {
		cfg.MaxDuration = 60 * time.Minute
	}
	if cfg.NodeShellTTL == 0 {
		cfg.NodeShellTTL = time.Hour
	}
	m := &Manager{cfg: cfg, log: cfg.Log, pending: map[string]*pendingStream{}}
	m.ns = newNodeShells(m)
	m.pf = newForwards(m)
	return m
}

func newID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func (m *Manager) add(p *pendingStream) string {
	id := newID()
	p.exp = m.cfg.Now().Add(protocol.StreamIDTTLSeconds * time.Second)
	m.mu.Lock()
	now := m.cfg.Now()
	for k, v := range m.pending {
		if now.After(v.exp) {
			delete(m.pending, k)
			if v.abort != nil {
				go v.abort()
			}
		}
	}
	m.pending[id] = p
	m.mu.Unlock()
	return id
}

func (m *Manager) take(id string) *pendingStream {
	m.mu.Lock()
	defer m.mu.Unlock()
	p := m.pending[id]
	delete(m.pending, id)
	if p == nil || m.cfg.Now().After(p.exp) {
		if p != nil && p.abort != nil {
			go p.abort()
		}
		return nil
	}
	return p
}

// ErrBadStream is returned for an unknown, expired or already used stream id.
var ErrBadStream = errors.New("unknown, expired or used stream id")

// Serve runs the stream behind id on t (the id is consumed). It returns when the stream ended.
func (m *Manager) Serve(ctx context.Context, id string, t Transport) error {
	p := m.take(id)
	if p == nil || p.run == nil {
		t.Close(ClosePolicy, "unknown stream")
		return ErrBadStream
	}
	// A hijacked WebSocket never cancels the request context: derive one that ends when the client goes away
	// (a failed read or write), so parked credit waits, remote execs and node shell pods are released.
	sctx, cancel := context.WithCancelCause(ctx)
	defer cancel(context.Canceled)
	p.run(sctx, &cancelOnError{Transport: t, cancel: cancel})
	return nil
}

// cancelOnError cancels the stream context with the cause when the client side of the transport fails.
type cancelOnError struct {
	Transport
	cancel context.CancelCauseFunc
}

func (c *cancelOnError) Read(ctx context.Context) (bool, []byte, error) {
	bin, data, err := c.Transport.Read(ctx)
	if err != nil {
		c.cancel(clientGone(err))
	}
	return bin, data, err
}

func (c *cancelOnError) Write(ctx context.Context, binary bool, data []byte) error {
	err := c.Transport.Write(ctx, binary, data)
	if err != nil && ctx.Err() == nil {
		c.cancel(clientGone(err))
	}
	return err
}

func clientGone(err error) error {
	if errors.Is(err, context.Canceled) {
		return context.Canceled
	}
	return fmt.Errorf("client disconnected: %w", err)
}

// Download runs the one-shot download behind id, writing the log to w.
func (m *Manager) Download(ctx context.Context, id string, w io.Writer) error {
	p := m.take(id)
	if p == nil || p.download == nil {
		return ErrBadStream
	}
	return p.download(ctx, w)
}

// Open validates a stream.open request, routes it (so a denied stream fails here, with the context it will use)
// and returns the id to claim, or for port-forwards the bound local port.
func (m *Manager) Open(ctx context.Context, p protocol.StreamOpenParams) (protocol.StreamOpenResult, error) {
	ctx = ops.WithUseWrite(ctx, p.UseWriteContext)
	if err := m.checkUID(ctx, p.Ref); err != nil {
		return protocol.StreamOpenResult{}, err
	}
	switch p.Type {
	case "logs":
		return m.openLogs(ctx, p)
	case "exec":
		return m.openExec(ctx, p)
	case "debug":
		return m.openDebug(ctx, p)
	case "nodeshell":
		return m.openNodeShell(ctx, p)
	case "portforward":
		return m.openPortForward(ctx, p)
	}
	m.xmu.Lock()
	x := m.extra[p.Type]
	m.xmu.Unlock()
	if x != nil {
		return x(ctx, p, func(run func(ctx context.Context, t Transport)) string {
			return m.add(&pendingStream{typ: p.Type, run: run})
		})
	}
	return protocol.StreamOpenResult{}, ops.NewError("bad_request", "unknown stream type %q", p.Type)
}

// ExtraOpener opens a stream type provided by a module (flows). claim registers the stream body and returns
// the single-use stream id to put in the result; run gets the client's transport and must return when the
// stream ends or ctx is cancelled (the client went away).
type ExtraOpener func(ctx context.Context, p protocol.StreamOpenParams, claim func(run func(ctx context.Context, t Transport)) string) (protocol.StreamOpenResult, error)

// Register adds a stream type to stream.open.
func (m *Manager) Register(typ string, x ExtraOpener) {
	m.xmu.Lock()
	defer m.xmu.Unlock()
	if m.extra == nil {
		m.extra = map[string]ExtraOpener{}
	}
	m.extra[typ] = x
}

// mapRouted maps a client error of a call made through a routed context: a 403 through the write context that
// the write context itself would deny is write_unavailable (ops.Service.MapRouted), not a plain forbidden.
func (m *Manager) mapRouted(ctx context.Context, err error, r *ops.Routed, verb, gvr, sub, ns, name string) error {
	if mm, ok := m.cfg.Router.(interface {
		MapRouted(ctx context.Context, err error, r *ops.Routed, verb, gvr, sub, ns, name string) error
	}); ok {
		return mm.MapRouted(ctx, err, r, verb, gvr, sub, ns, name)
	}
	return ops.MapError(err, r != nil && r.ViaWrite)
}

// runtimeOf returns the Runtime behind a routed context.
func runtimeOf(r *ops.Routed) (kube.Runtime, error) {
	rt, ok := kube.RuntimeOf(r.Conn)
	if !ok {
		return nil, ops.NewError("unavailable", "context %q does not support streams", r.ContextID)
	}
	return rt, nil
}

func (m *Manager) route(ctx context.Context, cluster, verb, gvr, sub, ns, name string) (*ops.Routed, kube.Runtime, error) {
	r, err := m.cfg.Router.Route(ctx, cluster, verb, gvr, sub, ns, name)
	if err != nil {
		return nil, nil, err
	}
	rt, err := runtimeOf(r)
	if err != nil {
		return nil, nil, err
	}
	return r, rt, nil
}

// limitsFor applies the write-context limits to ctx when the stream runs through a write context.
func (m *Manager) limitsFor(ctx context.Context, viaWrite bool) (context.Context, *limits) {
	if !viaWrite {
		return ctx, nil
	}
	return withLimits(ctx, m.cfg.IdleTimeout, m.cfg.MaxDuration)
}

// sendEnd ends a logs stream.
func sendEnd(ctx context.Context, t Transport, err error) {
	msg := protocol.StreamServerMsg{End: true}
	if err != nil && !errors.Is(err, context.Canceled) {
		msg.Error = err.Error()
	}
	b, _ := json.Marshal(msg)
	wctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	_ = t.Write(wctx, false, b)
	t.Close(CloseNormal, "")
}

func errf(code, format string, a ...any) error { return ops.NewError(code, format, a...) }

var _ = fmt.Sprint

// checkUID verifies a uid-pinned ref (ObjectRef.UID) against the live object: a stream must not attach to a
// recreated object (a pod replaced under the same name).
func (m *Manager) checkUID(ctx context.Context, ref *protocol.ObjectRef) error {
	if ref == nil || ref.UID == "" {
		return nil
	}
	gvr := ref.GVR
	if gvr == "" {
		gvr = podsGVR
	}
	g, err := parseRefGVR(gvr)
	if err != nil {
		return err
	}
	routed, err := m.cfg.Router.Route(ctx, ref.Cluster, "get", gvr, "", ref.Namespace, ref.Name)
	if err != nil {
		return err
	}
	if g.Group == "" && g.Resource == "pods" { // the common case reads through the runtime's typed client
		if rt, rerr := runtimeOf(routed); rerr == nil {
			pod, gerr := rt.Clientset().CoreV1().Pods(ref.Namespace).Get(ctx, ref.Name, metav1.GetOptions{})
			if gerr != nil {
				return ops.MapError(gerr, routed.ViaWrite)
			}
			if string(pod.UID) != ref.UID {
				return ops.NewError("conflict", "%s", ops.ErrReplaced)
			}
			return nil
		}
	}
	return ops.VerifyUID(ctx, routed.Conn, g, ref.Namespace, ref.Name, ref.UID)
}
