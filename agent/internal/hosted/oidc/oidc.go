package oidc

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

const (
	clockSkew      = time.Minute
	jwksTTL        = 10 * time.Minute
	jwksMinRefetch = 30 * time.Second
	maxBody        = 1 << 20
)

// Options configures one relying-party registration.
type Options struct {
	Issuer      string
	ClientID    string
	RedirectURL string
	Scopes      []string
	// At most one client authentication: Secret (client_secret_post) or Signer (private_key_jwt, RS256).
	// Neither is a public client: only client_id is sent, and PKCE protects the code.
	Secret string
	Signer crypto.Signer
	// KeyID and X5tS256 go in the client assertion header (Entra ID requires x5t#S256 of the certificate).
	KeyID   string
	X5tS256 string
	HTTP    *http.Client
	Now     func() time.Time
}

// Provider is a lazily discovered OIDC provider.
type Provider struct {
	o Options

	dmu sync.Mutex // serializes discovery
	jmu sync.Mutex // serializes JWKS refreshes

	mu      sync.Mutex // guards the fields below; never held across network I/O
	meta    *metadata
	keys    []jwk
	keysAt  time.Time
	fetchAt time.Time
}

type metadata struct {
	Issuer                string `json:"issuer"`
	AuthorizationEndpoint string `json:"authorization_endpoint"`
	TokenEndpoint         string `json:"token_endpoint"`
	JWKSURI               string `json:"jwks_uri"`
}

func New(o Options) *Provider {
	if o.HTTP == nil {
		o.HTTP = &http.Client{Timeout: 15 * time.Second}
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	hasOpenID := false
	for _, s := range o.Scopes {
		hasOpenID = hasOpenID || s == "openid"
	}
	if !hasOpenID {
		o.Scopes = append([]string{"openid"}, o.Scopes...)
	}
	return &Provider{o: o}
}

// ClientID returns the registered client id (the expected ID token audience).
func (p *Provider) ClientID() string { return p.o.ClientID }

// Issuer returns the issuer URL.
func (p *Provider) Issuer() string { return p.o.Issuer }

func (p *Provider) getJSON(ctx context.Context, u string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return err
	}
	resp, err := p.o.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("GET %s: %s", u, resp.Status)
	}
	return json.NewDecoder(io.LimitReader(resp.Body, maxBody)).Decode(out)
}

func (p *Provider) discover(ctx context.Context) (*metadata, error) {
	p.mu.Lock()
	m := p.meta
	p.mu.Unlock()
	if m != nil {
		return m, nil
	}
	p.dmu.Lock() // one discovery at a time; p.mu is never held across network I/O
	defer p.dmu.Unlock()
	p.mu.Lock()
	m = p.meta
	p.mu.Unlock()
	if m != nil {
		return m, nil
	}
	m, err := p.fetchMeta(ctx)
	if err != nil {
		return nil, err
	}
	p.mu.Lock()
	p.meta = m
	p.mu.Unlock()
	return m, nil
}

func (p *Provider) fetchMeta(ctx context.Context) (*metadata, error) {
	var m metadata
	if err := p.getJSON(ctx, strings.TrimRight(p.o.Issuer, "/")+"/.well-known/openid-configuration", &m); err != nil {
		return nil, fmt.Errorf("oidc discovery: %w", err)
	}
	if m.Issuer != p.o.Issuer {
		return nil, fmt.Errorf("oidc discovery: issuer mismatch (got %q)", m.Issuer)
	}
	if m.AuthorizationEndpoint == "" || m.TokenEndpoint == "" || m.JWKSURI == "" {
		return nil, errors.New("oidc discovery: incomplete metadata")
	}
	return &m, nil
}

// PKCE returns a new code verifier and its S256 challenge.
func PKCE() (verifier, challenge string) {
	verifier = Random(32)
	sum := sha256.Sum256([]byte(verifier))
	return verifier, base64.RawURLEncoding.EncodeToString(sum[:])
}

// Random returns n random bytes, base64url-encoded.
func Random(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return base64.RawURLEncoding.EncodeToString(b)
}

// AuthParams are the per-request authorization parameters.
type AuthParams struct {
	State, Nonce, Challenge string
	// Prompt is "" or "none" or "login".
	Prompt    string
	LoginHint string
}

// AuthURL returns the authorization endpoint URL for a code flow with PKCE.
func (p *Provider) AuthURL(ctx context.Context, a AuthParams) (string, error) {
	m, err := p.discover(ctx)
	if err != nil {
		return "", err
	}
	u, err := url.Parse(m.AuthorizationEndpoint)
	if err != nil {
		return "", err
	}
	q := u.Query()
	q.Set("response_type", "code")
	q.Set("client_id", p.o.ClientID)
	q.Set("redirect_uri", p.o.RedirectURL)
	q.Set("scope", strings.Join(p.o.Scopes, " "))
	q.Set("state", a.State)
	q.Set("nonce", a.Nonce)
	q.Set("code_challenge", a.Challenge)
	q.Set("code_challenge_method", "S256")
	if a.Prompt != "" {
		q.Set("prompt", a.Prompt)
	}
	if a.LoginHint != "" {
		q.Set("login_hint", a.LoginHint)
	}
	u.RawQuery = q.Encode()
	return u.String(), nil
}

// Tokens are the result of a code exchange or refresh.
type Tokens struct {
	IDToken      string
	RefreshToken string
	Claims       *Claims
}

// TokenError is an error response of the token endpoint.
type TokenError struct {
	Code, Description string
	Status            int
}

func (e *TokenError) Error() string {
	if e.Description != "" {
		return "token endpoint: " + e.Code + ": " + e.Description
	}
	return "token endpoint: " + e.Code
}

// Permanent reports whether the grant is definitively unusable (retrying cannot help).
func (e *TokenError) Permanent() bool {
	switch e.Code {
	case "invalid_grant", "invalid_client", "unauthorized_client", "interaction_required", "login_required", "consent_required":
		return true
	}
	return false
}

func (p *Provider) clientAuth(form url.Values, tokenEndpoint string) error {
	form.Set("client_id", p.o.ClientID)
	switch {
	case p.o.Signer != nil:
		a, err := p.assertion(tokenEndpoint)
		if err != nil {
			return err
		}
		form.Set("client_assertion_type", "urn:ietf:params:oauth:client-assertion-type:jwt-bearer")
		form.Set("client_assertion", a)
	case p.o.Secret != "":
		form.Set("client_secret", p.o.Secret)
	}
	return nil
}

func (p *Provider) assertion(aud string) (string, error) {
	hdr := map[string]any{"alg": "RS256", "typ": "JWT"}
	if p.o.KeyID != "" {
		hdr["kid"] = p.o.KeyID
	}
	if p.o.X5tS256 != "" {
		hdr["x5t#S256"] = p.o.X5tS256
	}
	now := p.o.Now()
	claims := map[string]any{"iss": p.o.ClientID, "sub": p.o.ClientID, "aud": aud, "jti": Random(16),
		"iat": now.Unix(), "nbf": now.Unix(), "exp": now.Add(5 * time.Minute).Unix()}
	hb, _ := json.Marshal(hdr)
	cb, _ := json.Marshal(claims)
	in := base64.RawURLEncoding.EncodeToString(hb) + "." + base64.RawURLEncoding.EncodeToString(cb)
	sum := sha256.Sum256([]byte(in))
	sig, err := p.o.Signer.Sign(rand.Reader, sum[:], crypto.SHA256)
	if err != nil {
		return "", err
	}
	if _, ok := p.o.Signer.Public().(*rsa.PublicKey); !ok {
		return "", errors.New("client assertion key must be RSA")
	}
	return in + "." + base64.RawURLEncoding.EncodeToString(sig), nil
}

func (p *Provider) token(ctx context.Context, form url.Values, nonce string, wantNonce bool) (*Tokens, error) {
	m, err := p.discover(ctx)
	if err != nil {
		return nil, err
	}
	if err := p.clientAuth(form, m.TokenEndpoint); err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, m.TokenEndpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	resp, err := p.o.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	if err != nil {
		return nil, err
	}
	var tr struct {
		IDToken      string `json:"id_token"`
		RefreshToken string `json:"refresh_token"`
		Error        string `json:"error"`
		ErrorDesc    string `json:"error_description"`
	}
	_ = json.Unmarshal(body, &tr)
	if resp.StatusCode != http.StatusOK || tr.Error != "" {
		code := tr.Error
		if code == "" {
			code = "http_" + resp.Status
		}
		// The description is the provider's own text; it carries no secrets of ours.
		return nil, &TokenError{Code: code, Description: tr.ErrorDesc, Status: resp.StatusCode}
	}
	if tr.IDToken == "" {
		return nil, errors.New("token endpoint returned no id_token")
	}
	claims, err := p.Verify(ctx, tr.IDToken, nonce, wantNonce)
	if err != nil {
		return nil, err
	}
	return &Tokens{IDToken: tr.IDToken, RefreshToken: tr.RefreshToken, Claims: claims}, nil
}

// Exchange redeems an authorization code and verifies the ID token, including the nonce.
func (p *Provider) Exchange(ctx context.Context, code, verifier, nonce string) (*Tokens, error) {
	return p.token(ctx, url.Values{"grant_type": {"authorization_code"}, "code": {code},
		"redirect_uri": {p.o.RedirectURL}, "code_verifier": {verifier}}, nonce, true)
}

// Refresh redeems a refresh token for a new ID token (and possibly a rotated refresh token).
func (p *Provider) Refresh(ctx context.Context, refreshToken string) (*Tokens, error) {
	t, err := p.token(ctx, url.Values{"grant_type": {"refresh_token"}, "refresh_token": {refreshToken},
		"scope": {strings.Join(p.o.Scopes, " ")}}, "", false)
	if err != nil {
		return nil, err
	}
	if t.RefreshToken == "" {
		t.RefreshToken = refreshToken
	}
	return t, nil
}

func (p *Provider) findKey(kid, kty string) *jwk {
	var cands []*jwk
	for i := range p.keys {
		k := &p.keys[i]
		if k.Use != "" && k.Use != "sig" || k.Kty != kty {
			continue
		}
		if kid != "" && k.Kid != kid {
			continue
		}
		cands = append(cands, k)
	}
	if len(cands) == 1 || (len(cands) > 0 && kid != "") {
		c := *cands[0]
		return &c
	}
	return nil
}

func (p *Provider) key(ctx context.Context, kid, kty string) (*jwk, error) {
	m, err := p.discover(ctx)
	if err != nil {
		return nil, err
	}
	now := p.o.Now()
	p.mu.Lock()
	fresh := p.keys != nil && now.Sub(p.keysAt) < jwksTTL
	if k := p.findKey(kid, kty); k != nil && fresh {
		p.mu.Unlock()
		return k, nil
	}
	recent := p.keys != nil && now.Sub(p.fetchAt) < jwksMinRefetch
	p.mu.Unlock()
	if recent {
		// Fetched moments ago: an unknown key id is not worth another round trip (it also bounds the refetch rate).
		p.mu.Lock()
		k := p.findKey(kid, kty)
		p.mu.Unlock()
		if k != nil {
			return k, nil
		}
		return nil, errors.New("unknown signing key")
	}
	// Refetch without holding p.mu across the network call; keep serving the cached keys if it fails.
	p.jmu.Lock()
	defer p.jmu.Unlock()
	p.mu.Lock()
	if p.keys != nil && p.o.Now().Sub(p.fetchAt) < jwksMinRefetch { // another caller just refreshed
		k := p.findKey(kid, kty)
		p.mu.Unlock()
		if k != nil {
			return k, nil
		}
		return nil, errors.New("unknown signing key")
	}
	p.fetchAt = now
	p.mu.Unlock()
	var set struct {
		Keys []jwk `json:"keys"`
	}
	if err := p.getJSON(ctx, m.JWKSURI, &set); err != nil {
		p.mu.Lock()
		k := p.findKey(kid, kty) // stale keys beat no keys
		p.mu.Unlock()
		if k != nil {
			return k, nil
		}
		return nil, fmt.Errorf("fetch jwks: %w", err)
	}
	p.mu.Lock()
	p.keys, p.keysAt = set.Keys, now
	k := p.findKey(kid, kty)
	p.mu.Unlock()
	if k != nil {
		return k, nil
	}
	return nil, errors.New("unknown signing key")
}

// Verify checks signature, issuer, audience, expiry and (when wantNonce) the nonce of an ID token.
func (p *Provider) Verify(ctx context.Context, raw, nonce string, wantNonce bool) (*Claims, error) {
	h, input, sig, raws, err := splitJWT(raw)
	if err != nil {
		return nil, err
	}
	if _, ok := algHash[h.Alg]; !ok {
		return nil, fmt.Errorf("unsupported token alg %q", h.Alg)
	}
	kty := "RSA"
	if strings.HasPrefix(h.Alg, "ES") {
		kty = "EC"
	}
	k, err := p.key(ctx, h.Kid, kty)
	if err != nil {
		return nil, err
	}
	pub, err := k.publicKey()
	if err != nil {
		return nil, err
	}
	if err := verifySig(h.Alg, pub, input, sig); err != nil {
		return nil, errors.New("invalid token signature")
	}
	c := &Claims{Raw: raws}
	c.Issuer, _ = raws["iss"].(string)
	c.Subject, _ = raws["sub"].(string)
	c.Nonce, _ = raws["nonce"].(string)
	switch a := raws["aud"].(type) {
	case string:
		c.Audience = []string{a}
	case []any:
		for _, e := range a {
			if s, ok := e.(string); ok {
				c.Audience = append(c.Audience, s)
			}
		}
	}
	if c.Issuer != p.o.Issuer {
		return nil, errors.New("token issuer mismatch")
	}
	okAud := false
	for _, a := range c.Audience {
		okAud = okAud || a == p.o.ClientID
	}
	if !okAud {
		return nil, errors.New("token audience mismatch")
	}
	if len(c.Audience) > 1 {
		if azp, _ := raws["azp"].(string); azp != p.o.ClientID {
			return nil, errors.New("token authorized party mismatch")
		}
	}
	if c.Subject == "" {
		return nil, errors.New("token has no subject")
	}
	now := p.o.Now()
	exp, ok := numericDate(raws["exp"])
	if !ok {
		return nil, errors.New("token has no expiry")
	}
	c.Expiry = exp
	if !now.Before(exp.Add(clockSkew)) {
		return nil, errors.New("token expired")
	}
	if nbf, ok := numericDate(raws["nbf"]); ok && now.Add(clockSkew).Before(nbf) {
		return nil, errors.New("token not yet valid")
	}
	if wantNonce && (nonce == "" || c.Nonce != nonce) {
		return nil, errors.New("token nonce mismatch")
	}
	return c, nil
}

// Ping fetches the discovery document afresh: the readiness probe's check that the provider is reachable.
func (p *Provider) Ping(ctx context.Context) error {
	m, err := p.fetchMeta(ctx)
	if err == nil {
		p.mu.Lock()
		p.meta = m
		p.mu.Unlock()
	}
	return err
}

// Discover fetches the provider metadata (cached after the first success).
func (p *Provider) Discover(ctx context.Context) error {
	_, err := p.discover(ctx)
	return err
}
