package hosted

import (
	"context"
	"crypto/subtle"
	"html"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/zyno-io/v8s/agent/internal/hosted/oidc"
)

// safeReturn accepts only same-site absolute paths ("/x"), never "//host" or "/\host".
func safeReturn(p string) string {
	if p == "" || p[0] != '/' || strings.HasPrefix(p, "//") || strings.ContainsAny(p, "\\\r\n") {
		return "/"
	}
	if u, err := url.Parse(p); err != nil || u.IsAbs() || u.Host != "" {
		return "/"
	}
	return p
}

func (s *Server) dropFlowLocked(state string, f *flow) {
	delete(s.flows, state)
	if s.byBind[f.bind] == state {
		delete(s.byBind, f.bind)
	}
}

// putFlow stores a pending flow. A browser has at most one pending flow (a new one replaces its old one), and at
// the global cap the oldest pending flow is evicted instead of refusing, so anonymous /auth/login requests cannot
// lock real users out.
func (s *Server) putFlow(state string, f *flow) {
	s.fmu.Lock()
	defer s.fmu.Unlock()
	if old, ok := s.byBind[f.bind]; ok {
		if of := s.flows[old]; of != nil {
			s.dropFlowLocked(old, of)
		}
	}
	if len(s.flows) >= maxFlows {
		var oldest string
		var oldestExp time.Time
		for k, of := range s.flows {
			if oldest == "" || of.exp.Before(oldestExp) {
				oldest, oldestExp = k, of.exp
			}
		}
		if of := s.flows[oldest]; of != nil {
			s.dropFlowLocked(oldest, of)
		}
	}
	f.exp = s.clock.Now().Add(flowTTL)
	s.flows[state] = f
	s.byBind[f.bind] = state
}

// takeFlow removes and returns a pending flow (single use).
func (s *Server) takeFlow(state string) *flow {
	if state == "" {
		return nil
	}
	s.fmu.Lock()
	defer s.fmu.Unlock()
	f := s.flows[state]
	if f == nil {
		return nil
	}
	s.dropFlowLocked(state, f)
	if s.clock.Now().After(f.exp) {
		return nil
	}
	return f
}

func (s *Server) setFlowCookie(w http.ResponseWriter, bind string) {
	http.SetCookie(w, &http.Cookie{Name: flowCookie, Value: bind, Path: "/", MaxAge: int(flowTTL.Seconds()),
		HttpOnly: true, Secure: true, SameSite: http.SameSiteLaxMode})
}

func (s *Server) clearFlowCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{Name: flowCookie, Value: "", Path: "/", MaxAge: -1, HttpOnly: true, Secure: true, SameSite: http.SameSiteLaxMode})
}

func bindOK(r *http.Request, f *flow) bool {
	c, err := r.Cookie(flowCookie)
	return err == nil && c.Value != "" && subtle.ConstantTimeCompare([]byte(c.Value), []byte(f.bind)) == 1
}

func (s *Server) fail(w http.ResponseWriter, code int, msg string) {
	s.clearFlowCookie(w)
	text(w, code, msg)
}

// startFlow redirects the browser to the provider for the login or cluster app.
func (s *Server) startFlow(w http.ResponseWriter, r *http.Request, p *oidc.Provider, f *flow, prompt string) {
	verifier, challenge := oidc.PKCE()
	state := oidc.Random(24)
	f.verifier, f.nonce, f.interactive = verifier, oidc.Random(16), prompt != "none"
	s.putFlow(state, f)
	u, err := p.AuthURL(r.Context(), oidc.AuthParams{State: state, Nonce: f.nonce, Challenge: challenge, Prompt: prompt})
	if err != nil {
		s.log.Error("identity provider unavailable", "err", err)
		s.fail(w, http.StatusBadGateway, "identity provider unavailable\n")
		return
	}
	http.Redirect(w, r, u, http.StatusFound)
}

// handleLogin: GET /auth/login?return=/path starts the OIDC login (code + PKCE + state + nonce).
func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		text(w, http.StatusMethodNotAllowed, "GET only\n")
		return
	}
	bind := oidc.Random(16)
	s.setFlowCookie(w, bind)
	s.startFlow(w, r, s.login, &flow{kind: flowLogin, bind: bind, ret: safeReturn(r.URL.Query().Get("return"))}, "")
}

// handleCallback: GET /auth/callback finishes the login, creates the session, then starts the cluster flow.
func (s *Server) handleCallback(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	f := s.takeFlow(q.Get("state"))
	if f == nil || f.kind != flowLogin || !bindOK(r, f) {
		s.fail(w, http.StatusBadRequest, "invalid or expired sign-in; start again at /auth/login\n")
		return
	}
	if e := q.Get("error"); e != "" || q.Get("code") == "" {
		s.fail(w, http.StatusUnauthorized, "sign-in was not completed\n")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	tok, err := s.login.Exchange(ctx, q.Get("code"), f.verifier, f.nonce)
	if err != nil {
		s.log.Warn("login rejected", "reason", err.Error())
		s.fail(w, http.StatusUnauthorized, "sign-in failed\n")
		return
	}
	c := tok.Claims
	username := usernameOf(c, s.cfg.OIDC.UsernameClaim)
	groups := c.Strings(s.cfg.OIDC.GroupsClaim)
	if groups == nil {
		groups = []string{}
	}
	sess := s.store.Create(Identity{Sub: c.Subject, OID: c.String("oid"), Username: username, Groups: groups})
	s.setSessionCookie(w, sess)
	s.log.Info("login", "user", username)
	switch {
	case s.sameApp:
		sess.SetClusterTokens(tok.IDToken, tok.RefreshToken, c.Expiry)
		s.finish(w, f.ret)
	case s.cluster != nil:
		s.startFlow(w, r, s.cluster, &flow{kind: flowCluster, bind: f.bind, session: sess.id, ret: f.ret}, "none")
	default:
		s.finish(w, f.ret)
	}
}

// handleClusterLogin: GET /auth/cluster-login (with a session) re-runs only the cluster app flow, for
// "auth-required" clusters; silent first, interactive when the provider needs the user.
func (s *Server) handleClusterLogin(w http.ResponseWriter, r *http.Request) {
	sess := s.sessionOf(r)
	if sess == nil || s.cluster == nil || s.sameApp {
		http.Redirect(w, r, "/auth/login?return="+url.QueryEscape(safeReturn(r.URL.Query().Get("return"))), http.StatusFound)
		return
	}
	bind := oidc.Random(16)
	s.setFlowCookie(w, bind)
	s.startFlow(w, r, s.cluster, &flow{kind: flowCluster, bind: bind, session: sess.id, ret: safeReturn(r.URL.Query().Get("return"))}, "none")
}

// handleClusterCallback finishes the cluster app flow and stores the ID and refresh tokens in the session.
func (s *Server) handleClusterCallback(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	f := s.takeFlow(q.Get("state"))
	if f == nil || f.kind != flowCluster || !bindOK(r, f) {
		s.fail(w, http.StatusBadRequest, "invalid or expired sign-in; start again at /auth/login\n")
		return
	}
	sess, ok := s.store.Lookup(f.session)
	if !ok {
		s.fail(w, http.StatusUnauthorized, "session ended; sign in again\n")
		return
	}
	if e := q.Get("error"); e != "" {
		switch e {
		case "login_required", "interaction_required", "consent_required", "account_selection_required":
			if !f.interactive { // silent attempt failed: fall back to the interactive flow, once
				s.startFlow(w, r, s.cluster, &flow{kind: flowCluster, bind: f.bind, session: f.session, ret: f.ret}, "")
				return
			}
		}
		s.log.Warn("cluster sign-in not completed", "user", sess.Identity.Username, "error", e)
		sess.MarkAuthRequired()
		s.finish(w, f.ret)
		return
	}
	if q.Get("code") == "" {
		s.fail(w, http.StatusBadRequest, "invalid sign-in response\n")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	tok, err := s.cluster.Exchange(ctx, q.Get("code"), f.verifier, f.nonce)
	if err != nil || !sameUser(sess.Identity, tok.Claims, s.cfg.OIDC.UsernameClaim) {
		reason := "different user"
		if err != nil {
			reason = err.Error()
		}
		s.log.Warn("cluster token rejected", "user", sess.Identity.Username, "reason", reason)
		sess.MarkAuthRequired()
		s.finish(w, f.ret)
		return
	}
	sess.SetClusterTokens(tok.IDToken, tok.RefreshToken, tok.Claims.Expiry)
	s.finish(w, f.ret)
}

// usernameOf maps the ID token to the user name: the configured claim, else a verified email, else the subject.
// Like Kubernetes' own OIDC authenticator, an email the IdP says is unverified is not an identity. Pick a claim
// that is unique and not user-editable at your IdP (docs/auth.md): it becomes the impersonated user name.
func usernameOf(c *oidc.Claims, claim string) string {
	if u := c.String(claim); u != "" {
		return u
	}
	if v, ok := c.Raw["email_verified"].(bool); !ok || v {
		if e := c.String("email"); e != "" {
			return e
		}
	}
	return c.Subject
}

// sameUser checks that the cluster app's token is for the person who logged in. Entra's sub differs per app
// registration, so the provider-wide oid and the username claim count too.
func sameUser(id Identity, c *oidc.Claims, usernameClaim string) bool {
	if c.Subject != "" && c.Subject == id.Sub {
		return true
	}
	if oid := c.String("oid"); oid != "" && oid == id.OID {
		return true
	}
	u := c.String(usernameClaim)
	if u == "" {
		u = c.String("email")
	}
	return u != "" && u == id.Username
}

// finish ends a flow: the flow cookie goes, and the browser continues to the app by a client-side navigation
// (an HTML page, not a 302), because a redirect chain that began at the provider is cross-site and would not
// carry the SameSite=Strict session cookie to the app's first request.
func (s *Server) finish(w http.ResponseWriter, ret string) {
	s.clearFlowCookie(w)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Content-Security-Policy", "default-src 'none'")
	e := html.EscapeString(safeReturn(ret))
	_, _ = w.Write([]byte(`<!doctype html><meta charset="utf-8"><meta http-equiv="refresh" content="0;url=` + e +
		`"><title>Signed in</title><p>Signed in. <a href="` + e + `">Continue</a></p>`))
}
