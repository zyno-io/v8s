package oidc_test

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/zyno-io/v8s/agent/internal/hosted/oidc"
	"github.com/zyno-io/v8s/agent/internal/hosted/oidctest"
)

const redirect = "https://v8s.example/auth/callback"

func setup(t *testing.T, mut func(*oidctest.Provider)) (*oidctest.Provider, *oidc.Provider, oidctest.User) {
	t.Helper()
	idp := oidctest.New()
	t.Cleanup(idp.Close)
	u := oidctest.User{Sub: "u1", Username: "alice@example.com", Groups: []string{"devs"}}
	idp.AddUser(u)
	idp.AddClient(oidctest.Client{ID: "app", Secret: "secret-value-1", RedirectURI: redirect})
	if mut != nil {
		mut(idp)
	}
	p := oidc.New(oidc.Options{Issuer: idp.Issuer(), ClientID: "app", RedirectURL: redirect, Secret: "secret-value-1", Scopes: []string{"profile"}})
	return idp, p, u
}

// authorize drives the mock's authorize endpoint without a browser and returns the code.
func authorize(t *testing.T, idp *oidctest.Provider, p *oidc.Provider, nonce, challenge string) string {
	t.Helper()
	au, err := p.AuthURL(context.Background(), oidc.AuthParams{State: "st", Nonce: nonce, Challenge: challenge})
	if err != nil {
		t.Fatal(err)
	}
	c := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := c.Get(au)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	loc, err := url.Parse(resp.Header.Get("Location"))
	if err != nil || loc.Query().Get("code") == "" || loc.Query().Get("state") != "st" {
		t.Fatalf("authorize: %d %v", resp.StatusCode, resp.Header.Get("Location"))
	}
	return loc.Query().Get("code")
}

func asTokenError(err error, out **oidc.TokenError) bool { return errors.As(err, out) }

func TestExchangeVerifiesEverything(t *testing.T) {
	idp, p, u := setup(t, func(i *oidctest.Provider) { i.DefaultUser = "u1" })
	v, ch := oidc.PKCE()
	code := authorize(t, idp, p, "n1", ch)
	tok, err := p.Exchange(context.Background(), code, v, "n1")
	if err != nil {
		t.Fatal(err)
	}
	if tok.Claims.Subject != u.Sub || tok.Claims.String("preferred_username") != u.Username || len(tok.Claims.Strings("groups")) != 1 {
		t.Fatalf("claims: %+v", tok.Claims)
	}
	if tok.RefreshToken == "" {
		t.Fatal("no refresh token")
	}
	rq := idp.Requests()[0]
	if rq.Method != "S256" || rq.Challenge != ch || rq.Nonce != "n1" || !strings.Contains(rq.Scope, "openid") {
		t.Fatalf("authorize request: %+v", rq)
	}
	// Refresh.
	r, err := p.Refresh(context.Background(), tok.RefreshToken)
	if err != nil || r.Claims.Subject != u.Sub {
		t.Fatalf("refresh: %v", err)
	}
	idp.SetFailRefresh(true)
	_, err = p.Refresh(context.Background(), tok.RefreshToken)
	var te *oidc.TokenError
	if !asTokenError(err, &te) || !te.Permanent() {
		t.Fatalf("want permanent token error, got %v", err)
	}
}

func TestExchangeRejects(t *testing.T) {
	cases := []struct {
		name  string
		mut   func(*oidctest.Provider)
		nonce string // expected by the client
		pkce  bool   // use a wrong verifier
		want  string
	}{
		{"wrong audience", func(i *oidctest.Provider) { i.DefaultUser = "u1"; i.AudienceOverride = "someone-else" }, "n1", false, "audience"},
		{"wrong nonce in token", func(i *oidctest.Provider) { i.DefaultUser = "u1"; i.NonceOverride = "forged" }, "n1", false, "nonce"},
		{"wrong pkce verifier", func(i *oidctest.Provider) { i.DefaultUser = "u1" }, "n1", true, "invalid_grant"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			idp, p, _ := setup(t, tc.mut)
			v, ch := oidc.PKCE()
			code := authorize(t, idp, p, tc.nonce, ch)
			if tc.pkce {
				v, _ = oidc.PKCE()
			}
			_, err := p.Exchange(context.Background(), code, v, tc.nonce)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("want %q error, got %v", tc.want, err)
			}
		})
	}
}

func TestVerify(t *testing.T) {
	var clock = time.Now()
	idp, p, u := setup(t, func(i *oidctest.Provider) { i.Now = func() time.Time { return clock } })
	ctx := context.Background()
	good := idp.MintIDToken("app", u, "n", time.Hour)
	if _, err := p.Verify(ctx, good, "n", true); err != nil {
		t.Fatalf("good token: %v", err)
	}
	if _, err := p.Verify(ctx, good, "other", true); err == nil {
		t.Fatal("nonce mismatch accepted")
	}
	if _, err := p.Verify(ctx, idp.MintIDToken("other-app", u, "n", time.Hour), "n", true); err == nil {
		t.Fatal("wrong audience accepted")
	}
	if _, err := p.Verify(ctx, idp.MintIDToken("app", u, "n", -time.Hour), "n", true); err == nil {
		t.Fatal("expired token accepted")
	}
	// Tampered payload.
	parts := strings.Split(good, ".")
	pl, _ := base64.RawURLEncoding.DecodeString(parts[1])
	var m map[string]any
	_ = json.Unmarshal(pl, &m)
	m["sub"] = "admin"
	nb, _ := json.Marshal(m)
	if _, err := p.Verify(ctx, parts[0]+"."+base64.RawURLEncoding.EncodeToString(nb)+"."+parts[2], "n", true); err == nil {
		t.Fatal("tampered token accepted")
	}
	// alg=none.
	hb, _ := json.Marshal(map[string]string{"alg": "none", "typ": "JWT"})
	if _, err := p.Verify(ctx, base64.RawURLEncoding.EncodeToString(hb)+"."+parts[1]+".", "n", true); err == nil {
		t.Fatal("alg none accepted")
	}
	// Bad signature.
	forged := idp.MintIDToken("app", u, "n", time.Hour)
	fp := strings.Split(forged, ".")
	if _, err := p.Verify(ctx, fp[0]+"."+fp[1]+"."+base64.RawURLEncoding.EncodeToString(make([]byte, 256)), "n", true); err == nil {
		t.Fatal("bad signature accepted")
	}
}

func TestPrivateKeyJWTClientAuth(t *testing.T) {
	idp, _, _ := setup(t, func(i *oidctest.Provider) { i.DefaultUser = "u1" })
	key, _ := rsa.GenerateKey(rand.Reader, 2048)
	idp.AddClient(oidctest.Client{ID: "cert-app", RedirectURI: redirect, PublicKey: &key.PublicKey})
	mk := func(k *rsa.PrivateKey) *oidc.Provider {
		return oidc.New(oidc.Options{Issuer: idp.Issuer(), ClientID: "cert-app", RedirectURL: redirect, Signer: k, X5tS256: "thumb"})
	}
	p := mk(key)
	v, ch := oidc.PKCE()
	if _, err := p.Exchange(context.Background(), authorize(t, idp, p, "n", ch), v, "n"); err != nil {
		t.Fatalf("private_key_jwt: %v", err)
	}
	wrong, _ := rsa.GenerateKey(rand.Reader, 2048)
	p = mk(wrong)
	v, ch = oidc.PKCE()
	if _, err := p.Exchange(context.Background(), authorize(t, idp, p, "n", ch), v, "n"); err == nil {
		t.Fatal("assertion signed by the wrong key accepted")
	}
}

func TestStaleKeysServeWhenJWKSFetchFails(t *testing.T) {
	now := time.Now()
	idp, p, u := setup(t, func(i *oidctest.Provider) { i.Now = func() time.Time { return now } })
	p = oidc.New(oidc.Options{Issuer: idp.Issuer(), ClientID: "app", RedirectURL: redirect, Secret: "secret-value-1", Now: func() time.Time { return now }})
	ctx := context.Background()
	tok := idp.MintIDToken("app", u, "n", 48*time.Hour)
	if _, err := p.Verify(ctx, tok, "n", true); err != nil {
		t.Fatal(err)
	}
	idp.Close()              // the provider (and its JWKS endpoint) goes away
	now = now.Add(time.Hour) // the 10 minute cache is stale now
	if _, err := p.Verify(ctx, tok, "n", true); err != nil {
		t.Fatalf("logins must survive a JWKS outage with the cached keys: %v", err)
	}
}
