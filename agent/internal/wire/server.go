package wire

import (
	"crypto/subtle"
	"log/slog"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/coder/websocket"
	"github.com/zyno-io/v8s/agent/internal/cluster"
	"github.com/zyno-io/v8s/agent/internal/view"
	"github.com/zyno-io/v8s/agent/pkg/protocol"
)

const csp = "default-src 'self'; connect-src 'self'; style-src 'self' 'unsafe-inline' https://fonts.googleapis.com; font-src https://fonts.gstatic.com"

// Options configures the server.
type Options struct {
	Manager      *cluster.Manager
	Engine       *view.Engine
	Token        string
	Mode         string // "desktop" | "serve" | "fake"
	AgentVersion string
	// Port is the listener port; the agent's own origin (http://127.0.0.1:<port>, http://localhost:<port>) is always allowed.
	Port           int
	AllowedOrigins []string
	// DevOrigins additionally allows http://localhost:* and http://127.0.0.1:* (fake mode with a dev token).
	DevOrigins bool
	StaticDir  string
	Log        *slog.Logger
	// Tick is the per-subscription patch interval (default 100 ms).
	Tick time.Duration
}

// Server is the HTTP handler for /ws, /healthz and the static UI.
type Server struct {
	opt Options
	log *slog.Logger
	mux *http.ServeMux
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
	if !s.originAllowed(r.Header.Get("Origin")) || !s.authorized(r) {
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
