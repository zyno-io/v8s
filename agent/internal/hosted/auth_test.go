package hosted

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/zyno-io/v8s/agent/internal/hosted/config"
	"github.com/zyno-io/v8s/agent/internal/hosted/oidc"
	"github.com/zyno-io/v8s/agent/internal/hosted/oidctest"
)

func audOf(t *testing.T, tok string) string {
	t.Helper()
	p := strings.Split(tok, ".")
	b, err := base64.RawURLEncoding.DecodeString(p[1])
	if err != nil {
		t.Fatal(err)
	}
	var c struct {
		Aud string `json:"aud"`
	}
	_ = json.Unmarshal(b, &c)
	return c.Aud
}

func TestLoginFlowParameters(t *testing.T) {
	e := newEnv(t, nil)
	b := e.browser()
	b.login("alice-sub")
	reqs := e.idp.Requests()
	if len(reqs) != 2 {
		t.Fatalf("want a login and a cluster authorize request, got %d", len(reqs))
	}
	login, clusterReq := reqs[0], reqs[1]
	if login.ClientID != "v8s-login" || clusterReq.ClientID != "k8s-app" {
		t.Fatalf("clients: %q %q", login.ClientID, clusterReq.ClientID)
	}
	for _, r := range reqs {
		if r.Method != "S256" || len(r.Challenge) < 43 || len(r.State) < 24 || len(r.Nonce) < 16 || !strings.Contains(r.Scope, "openid") {
			t.Fatalf("weak authorize request: %+v", r)
		}
	}
	if login.Prompt != "" || clusterReq.Prompt != "none" {
		t.Fatalf("prompts: %q / %q (cluster flow must be silent first)", login.Prompt, clusterReq.Prompt)
	}
	if login.State == clusterReq.State || login.Nonce == clusterReq.Nonce || login.Challenge == clusterReq.Challenge {
		t.Fatal("login and cluster flows share state, nonce or PKCE challenge")
	}
	if !strings.Contains(clusterReq.Scope, "offline_access") {
		t.Fatalf("cluster flow cannot get a refresh token: %q", clusterReq.Scope)
	}
	idTok, _, err := b.session().ClusterToken(e.clock.Now())
	if err != nil {
		t.Fatal(err)
	}
	if audOf(t, idTok) != "k8s-app" {
		t.Fatalf("cluster token audience %q, want the cluster app", audOf(t, idTok))
	}
}

func TestCallbackRejectsForgedOrReplayedFlows(t *testing.T) {
	e := newEnv(t, nil)
	e.idp.DefaultUser = "alice-sub"
	callback := func(b *browser, q string) *http.Response {
		resp, err := b.Get(e.ts.URL + "/auth/callback?" + q)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp
	}
	// Unknown state.
	b := e.browser()
	if r := callback(b, "code=x&state=forged"); r.StatusCode != 400 {
		t.Fatalf("forged state: %d", r.StatusCode)
	}
	// Start a real flow, capture state+code.
	start := func(b *browser) (state, code string) {
		resp, err := b.Get(e.ts.URL + "/auth/login")
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		az, err := b.Get(resp.Header.Get("Location"))
		if err != nil {
			t.Fatal(err)
		}
		az.Body.Close()
		loc, _ := url.Parse(az.Header.Get("Location"))
		return loc.Query().Get("state"), loc.Query().Get("code")
	}
	state, code := start(b)
	// Login CSRF: a different browser (no flow cookie) presents the victim's callback.
	attacker := e.browser()
	if r := callback(attacker, "code="+code+"&state="+state); r.StatusCode != 400 {
		t.Fatalf("callback without the flow cookie: %d", r.StatusCode)
	}
	// The state was single-use and is now burnt, even for the right browser.
	if r := callback(b, "code="+code+"&state="+state); r.StatusCode != 400 {
		t.Fatalf("replayed state: %d", r.StatusCode)
	}
	if e.srv.store.Len() != 0 {
		t.Fatal("a rejected callback created a session")
	}
	// Provider error.
	state, _ = start(b)
	if r := callback(b, "error=access_denied&state="+state); r.StatusCode != 401 {
		t.Fatalf("provider error: %d", r.StatusCode)
	}
	// Good flow still works afterwards.
	state, code = start(b)
	if r := callback(b, "code="+code+"&state="+state); r.StatusCode != 302 {
		t.Fatalf("good callback: %d", r.StatusCode)
	}
	if e.srv.store.Len() != 1 {
		t.Fatal("no session after a good callback")
	}
}

func TestLoginRejectsWrongAudienceAndNonce(t *testing.T) {
	for name, mut := range map[string]func(*oidctest.Provider){
		"wrong audience": func(i *oidctest.Provider) { i.AudienceOverride = "some-other-client" },
		"wrong nonce":    func(i *oidctest.Provider) { i.NonceOverride = "forged-nonce" },
	} {
		t.Run(name, func(t *testing.T) {
			e := newEnv(t, nil)
			mut(e.idp)
			b := e.browser()
			b.ssoLogin("alice-sub")
			hops := b.follow(e.ts.URL + "/auth/login")
			last := hops[len(hops)-1]
			if last.StatusCode != http.StatusUnauthorized {
				t.Fatalf("got %d, want 401", last.StatusCode)
			}
			if e.srv.store.Len() != 0 || b.cookie(SessionCookie) != nil {
				t.Fatal("a session exists after a rejected ID token")
			}
		})
	}
}

func TestClusterFlowFallsBackToInteractive(t *testing.T) {
	e := newEnv(t, nil)
	e.idp.DefaultUser = "alice-sub"
	e.idp.NoSSO = true // the IdP will not remember the login: prompt=none answers login_required
	b := e.browser()
	b.follow(e.ts.URL + "/auth/login")
	reqs := e.idp.Requests()
	var prompts []string
	for _, r := range reqs {
		if r.ClientID == "k8s-app" {
			prompts = append(prompts, "["+r.Prompt+"]")
		}
	}
	if strings.Join(prompts, "") != "[none][]" {
		t.Fatalf("cluster flow prompts %v, want silent then interactive", prompts)
	}
	if !b.session().ClusterAuthOK(e.clock.Now()) {
		t.Fatal("no cluster token after the interactive fallback")
	}
}

func TestClusterTokenOfAnotherUserIsRejected(t *testing.T) {
	id := Identity{Sub: "alice-sub", OID: "oid-a", Username: "alice@example.com"}
	mk := func(sub, oid, user string) *oidc.Claims {
		return &oidc.Claims{Subject: sub, Raw: map[string]any{"oid": oid, "preferred_username": user}}
	}
	if !sameUser(id, mk("other-app-sub", "oid-a", ""), "preferred_username") {
		t.Fatal("same oid (Entra: sub differs per app) must match")
	}
	if !sameUser(id, mk("other-app-sub", "", "alice@example.com"), "preferred_username") {
		t.Fatal("same username must match")
	}
	if sameUser(id, mk("mallory-sub", "oid-m", "mallory@example.com"), "preferred_username") {
		t.Fatal("a token of another person was accepted")
	}
}

func TestClusterTokenRefreshAndFailure(t *testing.T) {
	e := newEnv(t, func(c *config.Config, i *oidctest.Provider) { i.IDTokenTTL = 10 * time.Minute })
	b := e.browser()
	b.login("alice-sub")
	sess := b.session()
	first, exp1, _ := sess.ClusterToken(e.clock.Now())

	// Far from expiry: no refresh.
	e.srv.Maintain(t.Context())
	if e.idp.RefreshCalls() != 0 {
		t.Fatal("refreshed too early")
	}
	// Within the skew: refreshed, new token with a later expiry.
	e.clock.Advance(6 * time.Minute)
	e.srv.Maintain(t.Context())
	if e.idp.RefreshCalls() != 1 {
		t.Fatalf("refresh calls: %d", e.idp.RefreshCalls())
	}
	second, exp2, err := sess.ClusterToken(e.clock.Now())
	if err != nil || second == first || !exp2.After(exp1) {
		t.Fatalf("token not refreshed: err=%v same=%v exp %v -> %v", err, second == first, exp1, exp2)
	}
	if audOf(t, second) != "k8s-app" {
		t.Fatal("refreshed token has the wrong audience")
	}

	// A failed refresh marks the clusters auth-required and drops the tokens.
	e.idp.SetFailRefresh(true)
	e.clock.Advance(6 * time.Minute)
	e.srv.Maintain(t.Context())
	if _, _, err := sess.ClusterToken(e.clock.Now()); err == nil || !strings.Contains(err.Error(), "getting credentials") {
		t.Fatalf("expected an auth-required error, got %v", err)
	}
	resp, err := b.Get(e.ts.URL + "/api/me")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var me meResponse
	_ = json.NewDecoder(resp.Body).Decode(&me)
	if resp.StatusCode != 200 || me.ClusterAuth != "required" || me.Username != "alice@example.com" {
		t.Fatalf("/api/me after failed refresh: %d %+v", resp.StatusCode, me)
	}
	// The session itself is still valid; re-running only the cluster flow recovers.
	e.idp.SetFailRefresh(false)
	b.follow(e.ts.URL + "/auth/cluster-login")
	if !sess.ClusterAuthOK(e.clock.Now()) {
		t.Fatal("cluster-login did not restore the cluster token")
	}
}

func TestMeJSON(t *testing.T) {
	e := newEnv(t, nil)
	b := e.browser()
	if resp, _ := b.Get(e.ts.URL + "/api/me"); resp.StatusCode != 401 {
		t.Fatalf("anonymous /api/me: %d", resp.StatusCode)
	}
	b.login("root-sub")
	req, _ := http.NewRequest("GET", e.ts.URL+"/api/me", nil)
	req.Header.Set("Sec-Fetch-Site", "cross-site")
	if resp, _ := b.Do(req); resp.StatusCode != 403 {
		t.Fatalf("cross-site /api/me: %d", resp.StatusCode)
	}
	resp, _ := b.Get(e.ts.URL + "/api/me")
	var me meResponse
	_ = json.NewDecoder(resp.Body).Decode(&me)
	if me.Username != "root@example.com" || !me.Admin || len(me.Groups) != 1 || me.ExpiresAt == "" || me.ClusterAuth != "ok" {
		t.Fatalf("/api/me: %+v", me)
	}
	if resp.Header.Get("Cache-Control") != "no-store" {
		t.Fatal("/api/me must not be cached")
	}
	for _, p := range []string{"/healthz", "/readyz"} {
		if resp, _ := b.Get(e.ts.URL + p); resp.StatusCode != 200 {
			t.Fatalf("%s: %d", p, resp.StatusCode)
		}
	}
}

func TestSafeReturn(t *testing.T) {
	for in, want := range map[string]string{"/a/b?c=1": "/a/b?c=1", "": "/", "//evil.com": "/", "https://evil.com": "/", "/\\evil.com": "/", "evil": "/", "/x\r\ny": "/"} {
		if got := safeReturn(in); got != want {
			t.Errorf("safeReturn(%q)=%q want %q", in, got, want)
		}
	}
}

func TestUsernameMapping(t *testing.T) {
	mk := func(raw map[string]any) *oidc.Claims { return &oidc.Claims{Subject: "sub-1", Raw: raw} }
	cases := []struct {
		name string
		raw  map[string]any
		want string
	}{
		{"claim wins", map[string]any{"preferred_username": "alice", "email": "a@x"}, "alice"},
		{"verified email", map[string]any{"email": "a@x", "email_verified": true}, "a@x"},
		{"email without email_verified (Entra)", map[string]any{"email": "a@x"}, "a@x"},
		{"unverified email is not an identity", map[string]any{"email": "victim@x", "email_verified": false}, "sub-1"},
		{"nothing", map[string]any{}, "sub-1"},
	}
	for _, tc := range cases {
		if got := usernameOf(mk(tc.raw), "preferred_username"); got != tc.want {
			t.Errorf("%s: got %q want %q", tc.name, got, tc.want)
		}
	}
}

// A public login app that is also the cluster app (Entra "Mobile and desktop" redirect, no secret): login and
// refresh send client_id only, and the login tokens are the cluster tokens.
func TestPublicClientSameApp(t *testing.T) {
	e := newEnv(t, func(c *config.Config, idp *oidctest.Provider) {
		c.OIDC.ClientSecret = config.SecretRef{}
		c.OIDC.PublicClient = true
		c.ClusterApp = &config.ClusterApp{ClientID: c.OIDC.ClientID}
		idp.AddClient(oidctest.Client{ID: c.OIDC.ClientID, Public: true, RedirectURI: c.PublicURL + "/auth/callback"})
	})
	b := e.browser()
	b.login("alice-sub")
	resp, _ := b.Get(e.ts.URL + "/api/me")
	var me meResponse
	_ = json.NewDecoder(resp.Body).Decode(&me)
	if me.Username != "alice@example.com" || me.ClusterAuth != "ok" {
		t.Fatalf("/api/me: %+v", me)
	}
	if n := len(e.idp.Requests()); n != 1 {
		t.Fatalf("same app must not run a second authorization flow: %d requests", n)
	}
}
