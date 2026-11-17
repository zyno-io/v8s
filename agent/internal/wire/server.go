package wire

import (
	"context"
	"crypto/subtle"
	"log/slog"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"
	"github.com/zyno-io/v8s/agent/internal/cluster"
	"github.com/zyno-io/v8s/agent/internal/drainer"
	"github.com/zyno-io/v8s/agent/internal/flux"
	"github.com/zyno-io/v8s/agent/internal/helm"
	"github.com/zyno-io/v8s/agent/internal/jobs"
	"github.com/zyno-io/v8s/agent/internal/ops"
	"github.com/zyno-io/v8s/agent/internal/problems"
	"github.com/zyno-io/v8s/agent/internal/rpc"
	"github.com/zyno-io/v8s/agent/internal/settings"
	"github.com/zyno-io/v8s/agent/internal/streams"
	"github.com/zyno-io/v8s/agent/internal/view"
	"github.com/zyno-io/v8s/agent/pkg/protocol"
)

const csp = "default-src 'self'; connect-src 'self'; img-src 'self' data:; style-src 'self' 'unsafe-inline' https://fonts.googleapis.com; font-src https://fonts.gstatic.com"

// Options configures the server.
type Options struct {
	Manager  *cluster.Manager
	Engine   *view.Engine
	Ops      *ops.Service
	Settings *settings.Store
	// Optional G2 services (nil: the corresponding requests answer "unavailable").
	Streams  *streams.Manager
	Jobs     *jobs.Manager
	Drain    *drainer.Service
	Flux     *flux.Service
	Helm     *helm.Service
	Problems *problems.Hub
	// Handlers adds request methods (security.*, hubble.*); built-in methods win.
	Handlers     map[string]rpc.Handler
	Token        string
	Mode         string // "desktop" | "serve" | "fake"
	AgentVersion string
	// Port is the listener port; the agent's own origin (http://127.0.0.1:<port>, http://localhost:<port>) is always allowed.
	Port           int
	AllowedOrigins []string
	// DevOrigins additionally allows http://localhost:* and http://127.0.0.1:* (fake mode with a dev token).
	DevOrigins bool
	StaticDir  string
	// PreAuthorized: an authenticating front door (hosted mode: session cookie, strict Origin/Sec-Fetch-Site)
	// already vetted the request, so the token subprotocol and Origin allowlist are skipped.
	PreAuthorized bool
	Log           *slog.Logger
	// Tick is the per-subscription patch interval (default 100 ms).
	Tick time.Duration
}

// Server is the HTTP handler for /ws, /healthz and the static UI.
type Server struct {
	opt     Options
	clients atomic.Int32
	log     *slog.Logger
	mux     *http.ServeMux
}

func NewServer(opt Options) *Server {
	if opt.Tick <= 0 {
		opt.Tick = 100 * time.Millisecond
	}
	if opt.Log == nil {
		opt.Log = slog.New(slog.DiscardHandler)
	}
	s := &Server{opt: opt, log: opt.Log, mux: http.NewServeMux()}
	s.mux.HandleFunc("/ws", s.handleWS)
	s.mux.HandleFunc("/stream", s.handleStream)
	s.mux.HandleFunc("/stream/download", s.handleDownload)
	s.mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write([]byte("ok\n"))
	})
	if opt.StaticDir != "" {
		s.mux.Handle("/", staticHandler(opt.StaticDir))
	}
	return s
}

func (s *Server) Handler() http.Handler { return s.mux }

// ClientCount is the number of connected data sockets (the Hubble problem subscriptions run only while > 0).
// A nil server has no clients.
func (s *Server) ClientCount() int {
	if s == nil {
		return 0
	}
	return int(s.clients.Load())
}

// SetPort records the listener port once known (own-origin allowance).
func (s *Server) SetPort(p int) { s.opt.Port = p }

func (s *Server) originAllowed(origin string) bool {
	if origin == "" {
		return true // non-browser client; the token is still required
	}
	if s.opt.Port != 0 {
		p := strconv.Itoa(s.opt.Port)
		switch origin {
		case "http://127.0.0.1:" + p, "http://localhost:" + p, "http://[::1]:" + p:
			return true
		}
	}
	for _, a := range s.opt.AllowedOrigins {
		if a == origin {
			return true
		}
	}
	if s.opt.DevOrigins {
		for _, pre := range []string{"http://localhost:", "http://127.0.0.1:"} {
			if rest, ok := strings.CutPrefix(origin, pre); ok && rest != "" && isDigits(rest) {
				return true
			}
		}
	}
	return false
}

func isDigits(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

func (s *Server) authorized(r *http.Request) bool {
	var hasProto bool
	var token string
	for _, h := range r.Header.Values("Sec-WebSocket-Protocol") {
		for _, p := range strings.Split(h, ",") {
			p = strings.TrimSpace(p)
			if p == protocol.Subprotocol {
				hasProto = true
			} else if t, ok := strings.CutPrefix(p, "token."); ok {
				token = t
			}
		}
	}
	return hasProto && subtle.ConstantTimeCompare([]byte(token), []byte(s.opt.Token)) == 1
}

func (s *Server) handleWS(w http.ResponseWriter, r *http.Request) {
	if !s.opt.PreAuthorized && (!s.originAllowed(r.Header.Get("Origin")) || !s.authorized(r)) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	ws, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		Subprotocols:       []string{protocol.Subprotocol},
		InsecureSkipVerify: true, // origin was checked above
		CompressionMode:    websocket.CompressionDisabled,
	})
	if err != nil {
		return
	}
	s.clients.Add(1)
	defer s.clients.Add(-1)
	newConn(s, ws).run(r.Context())
}

func staticHandler(dir string) http.Handler {
	fs := http.FileServer(http.Dir(dir))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Security-Policy", csp)
		p := path.Clean("/" + r.URL.Path)
		if path.Ext(p) == "" && p != "/" {
			if _, err := os.Stat(filepath.Join(dir, filepath.FromSlash(p))); err != nil {
				r2 := *r
				r2.URL.Path = "/"
				fs.ServeHTTP(w, &r2) // SPA fallback
				return
			}
		}
		fs.ServeHTTP(w, r)
	})
}

// wsTransport adapts a WebSocket to streams.Transport.
type wsTransport struct{ ws *websocket.Conn }

func (w wsTransport) Read(ctx context.Context) (bool, []byte, error) {
	typ, b, err := w.ws.Read(ctx)
	return typ == websocket.MessageBinary, b, err
}

func (w wsTransport) Write(ctx context.Context, binary bool, data []byte) error {
	typ := websocket.MessageText
	if binary {
		typ = websocket.MessageBinary
	}
	wctx, cancel := context.WithTimeout(ctx, writeTimeout)
	defer cancel()
	return w.ws.Write(wctx, typ, data)
}

func (w wsTransport) Close(code int, reason string) {
	_ = w.ws.Close(websocket.StatusCode(code), reason)
}

// streamID extracts the single-use id from the offered subprotocols ("stream.<id>").
func streamID(r *http.Request) string {
	for _, h := range r.Header.Values("Sec-WebSocket-Protocol") {
		for _, p := range strings.Split(h, ",") {
			if id, ok := strings.CutPrefix(strings.TrimSpace(p), protocol.StreamSubprotocolPrefix); ok {
				return id
			}
		}
	}
	return ""
}

// handleStream serves GET /stream: same auth as /ws, plus the stream id from stream.open as subprotocol.
func (s *Server) handleStream(w http.ResponseWriter, r *http.Request) {
	id := streamID(r)
	if s.opt.Streams == nil || id == "" || (!s.opt.PreAuthorized && (!s.originAllowed(r.Header.Get("Origin")) || !s.authorized(r))) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	ws, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		Subprotocols:       []string{protocol.Subprotocol},
		InsecureSkipVerify: true, // origin was checked above
		CompressionMode:    websocket.CompressionDisabled,
	})
	if err != nil {
		return
	}
	ws.SetReadLimit(1 << 20)
	defer ws.CloseNow()
	_ = s.opt.Streams.Serve(r.Context(), id, wsTransport{ws})
}

// handleDownload serves GET /stream/download?streamId=...: the whole log as one text response. The stream id
// (128 random bits, single use, valid 30 s, bound to a request the authenticated data socket made) is the
// one-shot token; the Origin allowlist applies.
func (s *Server) handleDownload(w http.ResponseWriter, r *http.Request) {
	id := r.URL.Query().Get("streamId")
	if s.opt.Streams == nil || id == "" || r.Method != http.MethodGet || (!s.opt.PreAuthorized && !s.originAllowed(r.Header.Get("Origin"))) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="logs.txt"`)
	w.Header().Set("Cache-Control", "no-store")
	fw := &flushWriter{w: w}
	if err := s.opt.Streams.Download(r.Context(), id, fw); err != nil && !fw.wrote {
		http.Error(w, "forbidden", http.StatusForbidden)
	}
}

type flushWriter struct {
	w     http.ResponseWriter
	wrote bool
}

func (f *flushWriter) Write(p []byte) (int, error) {
	f.wrote = true
	n, err := f.w.Write(p)
	if fl, ok := f.w.(http.Flusher); ok {
		fl.Flush()
	}
	return n, err
}
