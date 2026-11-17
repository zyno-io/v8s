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
	p.run(ctx, t)
	return nil
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
	return protocol.StreamOpenResult{}, ops.NewError("bad_request", "unknown stream type %q", p.Type)
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
