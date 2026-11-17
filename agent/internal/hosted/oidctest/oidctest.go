// Package oidctest is a tiny in-process OpenID Connect provider for tests and local development.
// It supports discovery, JWKS, the authorization-code flow with PKCE (S256), prompt=none, refresh tokens,
// and client authentication by secret or private-key JWT (signature checked when the client registered a key).
package oidctest

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"time"
)

// User is an identity the provider can sign in.
type User struct {
	Sub      string
	Username string
	Groups   []string
}

// Client is a registered relying party.
type Client struct {
	ID          string
	Secret      string
	RedirectURI string
	// PublicKey, when set, verifies private_key_jwt assertions.
	PublicKey *rsa.PublicKey
}

// AuthRequest is a recorded authorization request.
type AuthRequest struct {
	ClientID, RedirectURI, State, Nonce, Prompt, Challenge, Method, Scope string
}

type code struct {
	client    string
	user      User
	nonce     string
	challenge string
	redirect  string
	exp       time.Time
}

// Provider is the running mock.
type Provider struct {
	*httptest.Server
	key *rsa.PrivateKey
	kid string

	mu       sync.Mutex
	clients  map[string]Client
	users    map[string]User
	codes    map[string]code
	refresh  map[string]refreshGrant
	requests []AuthRequest
	// Now is the clock used for iat/exp; IDTokenTTL the ID token lifetime (default 1 h).
	Now        func() time.Time
	IDTokenTTL time.Duration
	// FailRefresh makes refresh grants answer invalid_grant.
	failRefresh bool
	// AudienceOverride, when set, replaces the aud claim of issued ID tokens (to test audience validation).
	AudienceOverride string
	// NonceOverride, when set, replaces the nonce claim.
	NonceOverride string
	// UsernameClaim and GroupsClaim name the claims (defaults preferred_username, groups).
	UsernameClaim, GroupsClaim string
	// DefaultUser signs in when no SSO cookie exists and the request is interactive.
	DefaultUser string
	// NoSSO stops the provider from remembering interactive sign-ins (no SSO cookie), so a following prompt=none
	// request answers login_required.
	NoSSO        bool
	refreshCalls int
	exchanges    int
}

type refreshGrant struct {
	client string
	user   User
}

// New starts a provider; Close it with Server.Close.
func New() *Provider {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		panic(err)
	}
	p := &Provider{key: key, kid: "k1", clients: map[string]Client{}, users: map[string]User{}, codes: map[string]code{},
		refresh: map[string]refreshGrant{}, Now: time.Now, IDTokenTTL: time.Hour,
		UsernameClaim: "preferred_username", GroupsClaim: "groups"}
	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/openid-configuration", p.discovery)
	mux.HandleFunc("/jwks", p.jwks)
	mux.HandleFunc("/authorize", p.authorize)
	mux.HandleFunc("/token", p.token)
	mux.HandleFunc("/mock/login", p.mockLogin)
	p.Server = httptest.NewServer(mux)
	return p
}

// Issuer is the issuer URL.
func (p *Provider) Issuer() string { return p.URL }

func (p *Provider) AddClient(c Client) { p.mu.Lock(); p.clients[c.ID] = c; p.mu.Unlock() }
func (p *Provider) AddUser(u User)     { p.mu.Lock(); p.users[u.Sub] = u; p.mu.Unlock() }

// SetFailRefresh toggles invalid_grant answers for refresh grants.
func (p *Provider) SetFailRefresh(v bool) { p.mu.Lock(); p.failRefresh = v; p.mu.Unlock() }

// Requests returns the recorded authorization requests.
func (p *Provider) Requests() []AuthRequest {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]AuthRequest(nil), p.requests...)
}

// RefreshCalls returns how many refresh grants were redeemed (successfully or not).
func (p *Provider) RefreshCalls() int { p.mu.Lock(); defer p.mu.Unlock(); return p.refreshCalls }

// MintIDToken signs an ID token directly (for tests that need tokens outside a flow).
func (p *Provider) MintIDToken(clientID string, u User, nonce string, ttl time.Duration) string {
	return p.mint(clientID, u, nonce, ttl)
}

func (p *Provider) discovery(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, map[string]any{"issuer": p.URL, "authorization_endpoint": p.URL + "/authorize", "token_endpoint": p.URL + "/token",
		"jwks_uri": p.URL + "/jwks", "response_types_supported": []string{"code"}, "id_token_signing_alg_values_supported": []string{"RS256"},
		"code_challenge_methods_supported": []string{"S256"}})
}

func (p *Provider) jwks(w http.ResponseWriter, r *http.Request) {
	e := big.NewInt(int64(p.key.E)).Bytes()
	writeJSON(w, map[string]any{"keys": []map[string]string{{"kty": "RSA", "use": "sig", "alg": "RS256", "kid": p.kid,
		"n": base64.RawURLEncoding.EncodeToString(p.key.N.Bytes()), "e": base64.RawURLEncoding.EncodeToString(e)}}})
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(v)
}

// mockLogin signs a browser in (SSO cookie): GET /mock/login?sub=alice.
func (p *Provider) mockLogin(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{Name: "mock_sso", Value: r.URL.Query().Get("sub"), Path: "/"})
	_, _ = w.Write([]byte("ok"))
}

func (p *Provider) authorize(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	req := AuthRequest{ClientID: q.Get("client_id"), RedirectURI: q.Get("redirect_uri"), State: q.Get("state"), Nonce: q.Get("nonce"),
		Prompt: q.Get("prompt"), Challenge: q.Get("code_challenge"), Method: q.Get("code_challenge_method"), Scope: q.Get("scope")}
	p.mu.Lock()
	p.requests = append(p.requests, req)
	cl, ok := p.clients[req.ClientID]
	p.mu.Unlock()
	if !ok || cl.RedirectURI != req.RedirectURI || q.Get("response_type") != "code" {
		http.Error(w, "invalid client or redirect_uri", http.StatusBadRequest)
		return
	}
	back := func(vals url.Values) {
		u, _ := url.Parse(req.RedirectURI)
		qq := u.Query()
		for k, v := range vals {
			qq[k] = v
		}
		if req.State != "" {
			qq.Set("state", req.State)
		}
		u.RawQuery = qq.Encode()
		http.Redirect(w, r, u.String(), http.StatusFound)
	}
	if req.Method != "S256" || req.Challenge == "" {
		back(url.Values{"error": {"invalid_request"}, "error_description": {"PKCE S256 required"}})
		return
	}
	sub := ""
	if c, err := r.Cookie("mock_sso"); err == nil {
		sub = c.Value
	}
	if sub == "" {
		if req.Prompt == "none" {
			back(url.Values{"error": {"login_required"}})
			return
		}
		sub = p.DefaultUser
		if sub != "" && !p.NoSSO {
			http.SetCookie(w, &http.Cookie{Name: "mock_sso", Value: sub, Path: "/"})
		}
	}
	p.mu.Lock()
	u, ok := p.users[sub]
	p.mu.Unlock()
	if !ok {
		http.Error(w, "no signed-in user", http.StatusUnauthorized)
		return
	}
	c := randToken()
	p.mu.Lock()
	p.codes[c] = code{client: req.ClientID, user: u, nonce: req.Nonce, challenge: req.Challenge, redirect: req.RedirectURI, exp: p.Now().Add(time.Minute)}
	p.mu.Unlock()
	back(url.Values{"code": {c}})
}

func randToken() string {
	b := make([]byte, 24)
	_, _ = rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

func tokenErr(w http.ResponseWriter, status int, code string) {
	w.WriteHeader(status)
	writeJSON(w, map[string]string{"error": code})
}

func (p *Provider) authClient(r *http.Request) (Client, bool) {
	id := r.PostForm.Get("client_id")
	p.mu.Lock()
	cl, ok := p.clients[id]
	p.mu.Unlock()
	if !ok {
		return cl, false
	}
	if a := r.PostForm.Get("client_assertion"); a != "" {
		return cl, cl.PublicKey != nil && verifyAssertion(a, cl, p.URL+"/token")
	}
	return cl, cl.Secret != "" && r.PostForm.Get("client_secret") == cl.Secret
}

func verifyAssertion(a string, cl Client, aud string) bool {
	parts := strings.Split(a, ".")
	if len(parts) != 3 {
		return false
	}
	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return false
	}
	sum := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if rsa.VerifyPKCS1v15(cl.PublicKey, crypto.SHA256, sum[:], sig) != nil {
		return false
	}
	cb, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return false
	}
	var c map[string]any
	if json.Unmarshal(cb, &c) != nil {
		return false
	}
	return c["iss"] == cl.ID && c["sub"] == cl.ID && c["aud"] == aud
}

func (p *Provider) token(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost || r.ParseForm() != nil {
		tokenErr(w, 400, "invalid_request")
		return
	}
	cl, ok := p.authClient(r)
	if !ok {
		tokenErr(w, 401, "invalid_client")
		return
	}
	switch r.PostForm.Get("grant_type") {
	case "authorization_code":
		p.mu.Lock()
		c, ok := p.codes[r.PostForm.Get("code")]
		delete(p.codes, r.PostForm.Get("code")) // single use
		p.mu.Unlock()
		if !ok || c.client != cl.ID || c.redirect != r.PostForm.Get("redirect_uri") || p.Now().After(c.exp) {
			tokenErr(w, 400, "invalid_grant")
			return
		}
		sum := sha256.Sum256([]byte(r.PostForm.Get("code_verifier")))
		if base64.RawURLEncoding.EncodeToString(sum[:]) != c.challenge {
			tokenErr(w, 400, "invalid_grant")
			return
		}
		rt := randToken()
		p.mu.Lock()
		p.refresh[rt] = refreshGrant{client: cl.ID, user: c.user}
		p.exchanges++
		p.mu.Unlock()
		writeJSON(w, map[string]any{"id_token": p.mint(cl.ID, c.user, c.nonce, p.IDTokenTTL), "access_token": "opaque", "token_type": "Bearer", "refresh_token": rt})
	case "refresh_token":
		p.mu.Lock()
		p.refreshCalls++
		g, ok := p.refresh[r.PostForm.Get("refresh_token")]
		fail := p.failRefresh
		p.mu.Unlock()
		if !ok || fail || g.client != cl.ID {
			tokenErr(w, 400, "invalid_grant")
			return
		}
		writeJSON(w, map[string]any{"id_token": p.mint(cl.ID, g.user, "", p.IDTokenTTL), "token_type": "Bearer", "refresh_token": r.PostForm.Get("refresh_token")})
	default:
		tokenErr(w, 400, "unsupported_grant_type")
	}
}

func (p *Provider) mint(clientID string, u User, nonce string, ttl time.Duration) string {
	now := p.Now()
	aud := clientID
	if p.AudienceOverride != "" {
		aud = p.AudienceOverride
	}
	if p.NonceOverride != "" {
		nonce = p.NonceOverride
	}
	claims := map[string]any{"iss": p.URL, "sub": u.Sub, "aud": aud, "iat": now.Unix(), "exp": now.Add(ttl).Unix(),
		p.UsernameClaim: u.Username, p.GroupsClaim: u.Groups}
	if nonce != "" {
		claims["nonce"] = nonce
	}
	hb, _ := json.Marshal(map[string]string{"alg": "RS256", "typ": "JWT", "kid": p.kid})
	cb, _ := json.Marshal(claims)
	in := base64.RawURLEncoding.EncodeToString(hb) + "." + base64.RawURLEncoding.EncodeToString(cb)
	sum := sha256.Sum256([]byte(in))
	sig, err := rsa.SignPKCS1v15(rand.Reader, p.key, crypto.SHA256, sum[:])
	if err != nil {
		panic(fmt.Sprint(err))
	}
	return in + "." + base64.RawURLEncoding.EncodeToString(sig)
}
