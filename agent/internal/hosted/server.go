package hosted

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/zyno-io/v8s/agent/internal/hosted/config"
	"github.com/zyno-io/v8s/agent/internal/hosted/oidc"
	"github.com/zyno-io/v8s/agent/internal/settings"
	"github.com/zyno-io/v8s/agent/pkg/protocol"
)

const (
	// SessionCookie is the session cookie: host-only (__Host-), HttpOnly, Secure, SameSite=Strict, Path=/.
	SessionCookie = "__Host-v8s_session"
	// flowCookie binds the browser that started a login to its callback. It is SameSite=Lax because the IdP
	// returns the browser by a cross-site top-level navigation, which Strict cookies would not accompany. It
	// holds a random value only (no session), lives 10 minutes and is removed when the flow ends.
	flowCookie = "__Host-v8s_flow"
	flowTTL    = 10 * time.Minute
	maxFlows   = 10000
	// refreshSkew: refresh the cluster ID token when it expires within this long.
	refreshSkew   = 5 * time.Minute
	maintainEvery = 15 * time.Second
)

// Options configures the server.
type Options struct {
	Config *config.Config
	Log    *slog.Logger
	Clock  Clock
	// UI serves the embedded UI at / (nil: 404).
	UI http.Handler
	// HTTP is the client used for IdP calls (nil: a 15 s default).
	HTTP *http.Client
	// Factory builds cluster backends (nil: real Kubernetes clients). Tests inject fake clusters.
	Factory ClusterFactory
	// Tick is the per-subscription patch interval of the wire layer (default 100 ms).
	Tick time.Duration
	// CorrelationKey keys the HMAC of the session id (nil: the configured key, else random per start).
	CorrelationKey []byte
}

// Server is the `v8s server` HTTP handler.
type Server struct {
	cfg     *config.Config
	log     *slog.Logger
	clock   Clock
	origin  string
	https   bool
	ui      http.Handler
	factory ClusterFactory
	tick    time.Duration
	corrKey []byte

	store    *Store
	pool     *Pool
	login    *oidc.Provider
	cluster  *oidc.Provider // nil: no cluster app
	sameApp  bool           // the cluster app is the login app: the login tokens are the cluster tokens
	limiters *clusterLimiters

	baseCtx context.Context
	cancel  context.CancelFunc

	fmu    sync.Mutex
	flows  map[string]*flow
	byBind map[string]string // flow-cookie value -> its pending state (one pending flow per browser)

	smu    sync.Mutex
	stores map[string]*settings.Store

	mux *http.ServeMux
}

type flowKind int

const (
	flowLogin flowKind = iota
	flowCluster
)

// flow is a pending authorization-code flow, stored server-side and keyed by the state value.
type flow struct {
	kind        flowKind
	verifier    string
	nonce       string
	bind        string // must equal the flow cookie
	session     string // cluster flow: the session the tokens go to
	ret         string
	interactive bool
	exp         time.Time
}

// New builds the server. Secrets are resolved now so a bad deployment fails at start.
func New(ctx context.Context, opt Options) (*Server, error) {
	cfg := opt.Config
	if opt.Clock == nil {
		opt.Clock = RealClock{}
	}
	if opt.Log == nil {
		opt.Log = slog.New(slog.DiscardHandler)
	}
	pu, err := url.Parse(cfg.PublicURL)
	if err != nil {
		return nil, err
	}
	s := &Server{cfg: cfg, log: opt.Log, clock: opt.Clock, origin: pu.Scheme + "://" + strings.ToLower(pu.Host),
		https: pu.Scheme == "https", ui: opt.UI, factory: opt.Factory, tick: opt.Tick, flows: map[string]*flow{}, byBind: map[string]string{},
		limiters: &clusterLimiters{qps: cfg.Limits.ClusterQPS, burst: cfg.Limits.ClusterBurst}}
	if s.factory == nil {
		s.factory = kubeFactory(opt.Clock.Now)
	}
	s.corrKey = opt.CorrelationKey
	if s.corrKey == nil {
		if !cfg.CorrelationKey.IsZero() {
			k, err := cfg.CorrelationKey.Resolve()
			if err != nil {
				return nil, fmt.Errorf("correlationKey: %w", err)
			}
			s.corrKey = []byte(k)
		} else {
			s.corrKey = make([]byte, 32)
			if _, err := rand.Read(s.corrKey); err != nil {
				return nil, err
			}
		}
	}
	s.store = NewStore(s.clock, cfg.Session.Lifetime.D(), cfg.Session.IdleTimeout.D(), cfg.Session.MaxPerUser)
	s.baseCtx, s.cancel = context.WithCancel(ctx)
	s.pool = newPool(s)

	loginScopes := cfg.OIDC.Scopes
	if ca := cfg.ClusterApp; ca != nil && ca.ClientID == cfg.OIDC.ClientID && (ca.Issuer == "" || ca.Issuer == cfg.OIDC.Issuer) {
		s.sameApp = true
		loginScopes = append(append([]string(nil), loginScopes...), "offline_access")
	}
	s.login, err = buildProvider(cfg.PublicURL+"/auth/callback", cfg.OIDC.Issuer, cfg.OIDC.ClientID, cfg.OIDC.PublicClient, cfg.OIDC.ClientSecret,
		cfg.OIDC.PrivateKey, cfg.OIDC.Certificate, loginScopes, opt.HTTP, s.clock.Now)
	if err != nil {
		return nil, fmt.Errorf("oidc: %w", err)
	}
	if ca := cfg.ClusterApp; ca != nil && !s.sameApp {
		iss := ca.Issuer
		if iss == "" {
			iss = cfg.OIDC.Issuer
		}
		s.cluster, err = buildProvider(cfg.PublicURL+"/auth/cluster-callback", iss, ca.ClientID, ca.PublicClient, ca.ClientSecret,
			ca.PrivateKey, ca.Certificate, ca.Scopes, opt.HTTP, s.clock.Now)
		if err != nil {
			return nil, fmt.Errorf("clusterApp: %w", err)
		}
	} else if s.sameApp {
		s.cluster = s.login
	}

	m := http.NewServeMux()
	m.HandleFunc("/auth/login", s.handleLogin)
	m.HandleFunc("/auth/callback", s.handleCallback)
	m.HandleFunc("/auth/cluster-login", s.handleClusterLogin)
	m.HandleFunc("/auth/cluster-callback", s.handleClusterCallback)
	m.HandleFunc("/auth/logout", s.handleLogout)
	m.HandleFunc("/api/me", s.handleMe)
	m.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) { text(w, http.StatusOK, "ok\n") })
	m.HandleFunc("/readyz", s.handleReady)
	m.HandleFunc("/ws", s.handleWS)
	m.HandleFunc("/stream", s.handleStream)
	m.HandleFunc("/stream/download", s.handleStream)
	m.HandleFunc("/", s.handleRoot)
	s.mux = m
	return s, nil
}

func buildProvider(redirect, issuer, clientID string, public bool, secret, key, cert config.SecretRef, scopes []string, hc *http.Client, now func() time.Time) (*oidc.Provider, error) {
	o := oidc.Options{Issuer: issuer, ClientID: clientID, RedirectURL: redirect, Scopes: scopes, HTTP: hc, Now: now}
	switch {
	case public:
		// No client authentication: the token requests carry client_id, and PKCE binds the code to this server.
	case !secret.IsZero():
		v, err := secret.Resolve()
		if err != nil {
			return nil, fmt.Errorf("clientSecret: %w", err)
		}
		o.Secret = v
	default:
		pemKey, err := key.Resolve()
		if err != nil {
			return nil, fmt.Errorf("privateKey: %w", err)
		}
		k, err := parseRSAKey(pemKey)
		if err != nil {
			return nil, fmt.Errorf("privateKey: %w", err)
		}
		o.Signer = k
		if !cert.IsZero() {
			cp, err := cert.Resolve()
			if err != nil {
				return nil, fmt.Errorf("certificate: %w", err)
			}
			blk, _ := pem.Decode([]byte(cp))
			if blk == nil {
				return nil, errors.New("certificate: not PEM")
			}
			sum := sha256.Sum256(blk.Bytes)
			o.X5tS256 = base64.RawURLEncoding.EncodeToString(sum[:])
		}
	}
	return oidc.New(o), nil
}

func parseRSAKey(p string) (*rsa.PrivateKey, error) {
	blk, _ := pem.Decode([]byte(p))
	if blk == nil {
		return nil, errors.New("not PEM")
	}
	if k, err := x509.ParsePKCS1PrivateKey(blk.Bytes); err == nil {
		return k, nil
	}
	k, err := x509.ParsePKCS8PrivateKey(blk.Bytes)
	if err != nil {
		return nil, errors.New("unsupported private key")
	}
	rk, ok := k.(*rsa.PrivateKey)
	if !ok {
		return nil, errors.New("private key must be RSA")
	}
	return rk, nil
}

// Handler returns the HTTP handler (security headers applied to everything).
func (s *Server) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "same-origin")
		if s.https {
			h.Set("Strict-Transport-Security", "max-age=31536000")
		}
		if strings.HasPrefix(r.URL.Path, "/auth/") || strings.HasPrefix(r.URL.Path, "/api/") {
			h.Set("Cache-Control", "no-store")
		}
		s.mux.ServeHTTP(w, r)
	})
}

// Store exposes the session store (tests).
func (s *Server) Store() *Store { return s.store }

// Pool exposes the engine pool (tests).
func (s *Server) Pool() *Pool { return s.pool }

// Run runs the maintenance loop (session sweep, token refresh) until ctx ends, then stops every engine.
func (s *Server) Run(ctx context.Context) {
	t := time.NewTicker(maintainEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			s.Close()
			return
		case <-t.C:
			s.Maintain(ctx)
		}
	}
}

// Close ends every session and stops every engine.
func (s *Server) Close() {
	s.cancel()
	for _, sess := range s.store.Sweep() {
		s.store.Delete(sess.id)
	}
	s.pool.Close()
}

// Maintain sweeps expired sessions and pending flows and refreshes cluster tokens that are about to expire.
func (s *Server) Maintain(ctx context.Context) {
	live := s.store.Sweep()
	now := s.clock.Now()
	s.fmu.Lock()
	for k, f := range s.flows {
		if now.After(f.exp) {
			s.dropFlowLocked(k, f)
		}
	}
	s.fmu.Unlock()
	var wg sync.WaitGroup
	sem := make(chan struct{}, 8)
	for _, sess := range live {
		refresh, ok := sess.beginRefresh(now, refreshSkew)
		if !ok || s.cluster == nil {
			continue
		}
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			s.refresh(ctx, sess, refresh)
		}()
	}
	wg.Wait()
}

func (s *Server) refresh(ctx context.Context, sess *Session, refresh string) {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	tok, err := s.cluster.Refresh(ctx, refresh)
	if err == nil && !sameUser(sess.Identity, tok.Claims, s.cfg.OIDC.UsernameClaim) {
		err = errors.New("refreshed token belongs to a different user")
	}
	if err != nil {
		var te *oidc.TokenError
		if (errors.As(err, &te) && te.Permanent()) || err.Error() == "refreshed token belongs to a different user" {
			s.log.Warn("cluster token refresh failed; sign-in required", "user", sess.Identity.Username)
			sess.MarkAuthRequired()
		} else {
			// Transient (network, 5xx): keep the token until it expires and try again next tick.
			s.log.Warn("cluster token refresh failed; will retry", "user", sess.Identity.Username)
			sess.endRefresh()
		}
		return
	}
	sess.SetClusterTokens(tok.IDToken, tok.RefreshToken, tok.Claims.Expiry)
}

// ---- request checks ---------------------------------------------------------------------------------

func text(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(code)
	_, _ = w.Write([]byte(msg))
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

// mutationOK is the CSRF check of every state-changing HTTP request: the browser must say it came from our own
// origin (Origin, or Sec-Fetch-Site same-origin when Origin is absent) and never from another site.
func (s *Server) mutationOK(r *http.Request) bool {
	if sfs := r.Header.Get("Sec-Fetch-Site"); sfs != "" && sfs != "same-origin" {
		return false
	}
	if o := r.Header.Get("Origin"); o != "" {
		return o == s.origin
	}
	return r.Header.Get("Sec-Fetch-Site") == "same-origin"
}

// wsOK is the stricter check of the WebSocket endpoints: Origin must equal the public origin and the browser
// must report Sec-Fetch-Site: same-origin. Non-browser clients must send both headers.
func (s *Server) wsOK(r *http.Request) bool {
	return r.Header.Get("Origin") == s.origin && r.Header.Get("Sec-Fetch-Site") == "same-origin"
}

// readOK rejects cross-site subresource reads of the JSON API.
func readOK(r *http.Request) bool {
	switch r.Header.Get("Sec-Fetch-Site") {
	case "", "same-origin", "none":
		return true
	}
	return false
}

func (s *Server) sessionOf(r *http.Request) *Session {
	c, err := r.Cookie(SessionCookie)
	if err != nil {
		return nil
	}
	sess, _ := s.store.Lookup(c.Value)
	return sess
}

func (s *Server) setSessionCookie(w http.ResponseWriter, sess *Session) {
	http.SetCookie(w, &http.Cookie{Name: SessionCookie, Value: sess.id, Path: "/", Expires: sess.expires,
		MaxAge: int(sess.expires.Sub(s.clock.Now()).Seconds()), HttpOnly: true, Secure: true, SameSite: http.SameSiteStrictMode})
}

func (s *Server) clearSessionCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{Name: SessionCookie, Value: "", Path: "/", MaxAge: -1, HttpOnly: true, Secure: true, SameSite: http.SameSiteStrictMode})
}

// ---- API ----------------------------------------------------------------------------------------------

type meResponse struct {
	Username    string   `json:"username"`
	Subject     string   `json:"sub"`
	Groups      []string `json:"groups"`
	ExpiresAt   string   `json:"expiresAt"`
	Admin       bool     `json:"admin"`
	ClusterAuth string   `json:"clusterAuth"` // "ok" | "required": the cluster sign-in is missing or expired
}

func (s *Server) handleMe(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet || !readOK(r) {
		text(w, http.StatusForbidden, "forbidden\n")
		return
	}
	sess := s.sessionOf(r)
	if sess == nil {
		text(w, http.StatusUnauthorized, "sign in required\n")
		return
	}
	ca := "required"
	if s.cluster == nil || sess.ClusterAuthOK(s.clock.Now()) {
		ca = "ok"
	}
	admin := false
	for _, g := range sess.Identity.Groups {
		for _, a := range s.cfg.AdminGroups {
			admin = admin || g == a
		}
	}
	g := sess.Identity.Groups
	if g == nil {
		g = []string{}
	}
	writeJSON(w, meResponse{Username: sess.Identity.Username, Subject: sess.Identity.Sub, Groups: g,
		ExpiresAt: sess.expires.UTC().Format(time.RFC3339), Admin: admin, ClusterAuth: ca})
}

func (s *Server) handleReady(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	if err := s.login.Ping(ctx); err != nil {
		text(w, http.StatusServiceUnavailable, "identity provider unreachable\n")
		return
	}
	text(w, http.StatusOK, "ready\n")
}

func (s *Server) handleRoot(w http.ResponseWriter, r *http.Request) {
	if s.ui == nil {
		http.NotFound(w, r)
		return
	}
	s.ui.ServeHTTP(w, r)
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		text(w, http.StatusMethodNotAllowed, "POST only\n")
		return
	}
	if !s.mutationOK(r) {
		text(w, http.StatusForbidden, "forbidden\n")
		return
	}
	if c, err := r.Cookie(SessionCookie); err == nil {
		s.store.Delete(c.Value)
	}
	s.clearSessionCookie(w)
	w.WriteHeader(http.StatusNoContent)
}

// ---- WebSocket front door --------------------------------------------------------------------------------

func offersSubprotocol(r *http.Request) bool {
	for _, h := range r.Header.Values("Sec-WebSocket-Protocol") {
		for _, p := range strings.Split(h, ",") {
			if strings.TrimSpace(p) == protocol.Subprotocol {
				return true
			}
		}
	}
	return false
}

// handleWS authenticates /ws by the session cookie (instead of the token subprotocol), requires the v8s.v1
// subprotocol, a strict Origin and Sec-Fetch-Site: same-origin, then hands the socket to the user's own engine.
func (s *Server) handleWS(w http.ResponseWriter, r *http.Request) {
	if !s.wsOK(r) {
		text(w, http.StatusForbidden, "forbidden\n")
		return
	}
	sess := s.sessionOf(r)
	if sess == nil {
		text(w, http.StatusUnauthorized, "sign in required\n")
		return
	}
	if !offersSubprotocol(r) {
		text(w, http.StatusBadRequest, "subprotocol v8s.v1 required\n")
		return
	}
	ue, release, err := s.pool.Acquire(sess)
	if errors.Is(err, errSessionEnded) {
		text(w, http.StatusUnauthorized, "sign in required\n")
		return
	}
	if err != nil {
		s.log.Error("engine start failed", "err", err)
		text(w, http.StatusServiceUnavailable, "try again\n")
		return
	}
	defer release()
	h := ue.handler()
	if h == nil {
		text(w, http.StatusServiceUnavailable, "try again\n")
		return
	}
	s.serveBound(w, r, sess, h)
}

// handleStream serves /stream and /stream/download for a session whose engine is already running (the stream id
// came from a request on its data socket).
func (s *Server) handleStream(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/stream/download" {
		if r.Method != http.MethodGet || r.Header.Get("Sec-Fetch-Site") != "same-origin" {
			text(w, http.StatusForbidden, "forbidden\n")
			return
		}
	} else if !s.wsOK(r) {
		text(w, http.StatusForbidden, "forbidden\n")
		return
	}
	sess := s.sessionOf(r)
	if sess == nil {
		text(w, http.StatusUnauthorized, "sign in required\n")
		return
	}
	if r.URL.Path == "/stream" && !offersSubprotocol(r) {
		text(w, http.StatusBadRequest, "subprotocol v8s.v1 required\n")
		return
	}
	ue := s.pool.Get(sess)
	if ue == nil {
		text(w, http.StatusConflict, "no open data connection\n")
		return
	}
	h := ue.handler()
	if h == nil {
		text(w, http.StatusConflict, "no open data connection\n")
		return
	}
	s.serveBound(w, r, sess, h)
}

// serveBound runs h for the life of the connection, cancelling it when the session ends.
func (s *Server) serveBound(w http.ResponseWriter, r *http.Request, sess *Session, h http.Handler) {
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	remove := sess.OnClose(cancel)
	defer remove()
	sess.connOpened()
	defer func() { sess.connClosed(s.clock.Now()) }()
	h.ServeHTTP(w, r.WithContext(ctx))
}
