package hosted

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/zyno-io/v8s/agent/internal/hosted/config"
	"github.com/zyno-io/v8s/agent/internal/hosted/oidctest"
)

func TestLoginSetsHardenedSessionCookie(t *testing.T) {
	e := newEnv(t, nil)
	b := e.browser()
	hops := b.login("alice-sub")

	var sc *http.Cookie
	for _, r := range hops {
		for _, c := range r.Cookies() {
			if c.Name == SessionCookie {
				sc = c
			}
		}
	}
	if sc == nil {
		t.Fatal("no session cookie issued")
	}
	if !sc.HttpOnly || !sc.Secure || sc.SameSite != http.SameSiteStrictMode || sc.Path != "/" || sc.Domain != "" {
		t.Fatalf("cookie flags: %+v", sc)
	}
	if len(sc.Value) != 43 { // 256 random bits, base64url
		t.Fatalf("session id has %d chars, want 43 (256 bits)", len(sc.Value))
	}
	if sc.Expires.Before(e.clock.Now().Add(11*time.Hour)) || sc.Expires.After(e.clock.Now().Add(13*time.Hour)) {
		t.Fatalf("cookie expiry %v not the 12h absolute lifetime", sc.Expires)
	}
	last := hops[len(hops)-1]
	if last.StatusCode != 200 || !strings.HasPrefix(last.Header.Get("Content-Type"), "text/html") {
		t.Fatalf("final hop: %d %s", last.StatusCode, last.Header.Get("Content-Type"))
	}
	// The flow cookie is gone and a login-only cookie never carries the session.
	if c := b.cookie(flowCookie); c != nil {
		t.Fatalf("flow cookie not cleared: %+v", c)
	}
	// /api/me works with the cookie.
	resp, err := b.Get(e.ts.URL + "/api/me")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("/api/me: %d", resp.StatusCode)
	}
}

func TestSessionExpiryAbsoluteAndIdle(t *testing.T) {
	e := newEnv(t, nil)
	me := func(b *browser) int {
		resp, err := b.Get(e.ts.URL + "/api/me")
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	b := e.browser()
	b.login("alice-sub")
	sess := b.session()
	if me(b) != 200 {
		t.Fatal("not signed in")
	}
	// Idle: activity keeps the session alive past the idle timeout measured from the last request.
	for i := 0; i < 5; i++ {
		e.clock.Advance(20 * time.Minute)
		if me(b) != 200 {
			t.Fatalf("session ended early at step %d", i)
		}
	}
	e.clock.Advance(31 * time.Minute) // idle > 30 min
	if me(b) != 401 {
		t.Fatal("idle session still valid")
	}
	if _, _, err := sess.ClusterToken(e.clock.Now()); err == nil {
		t.Fatal("tokens survived idle expiry")
	}

	// Absolute lifetime: constant activity cannot extend 12 h.
	b = e.browser()
	b.login("alice-sub")
	sess = b.session()
	for i := 0; i < 28; i++ { // 11 h 40 m of activity every 25 min
		e.clock.Advance(25 * time.Minute)
		if me(b) != 200 {
			t.Fatalf("expired early at %d", i)
		}
	}
	e.clock.Advance(25 * time.Minute) // > 12h since login
	if me(b) != 401 {
		t.Fatal("session outlived its absolute lifetime")
	}
	if _, _, err := sess.ClusterToken(e.clock.Now()); err == nil {
		t.Fatal("tokens survived absolute expiry")
	}
	if e.srv.store.Len() != 0 {
		t.Fatalf("%d sessions left", e.srv.store.Len())
	}
}

func TestLogoutDeletesSessionAndTokens(t *testing.T) {
	e := newEnv(t, nil)
	b := e.browser()
	b.login("alice-sub")
	sess := b.session()
	if _, _, err := sess.ClusterToken(e.clock.Now()); err != nil {
		t.Fatalf("no cluster token after login: %v", err)
	}
	post := func(h map[string]string) *http.Response {
		req, _ := http.NewRequest(http.MethodPost, e.ts.URL+"/auth/logout", nil)
		for k, v := range h {
			req.Header.Set(k, v)
		}
		resp, err := b.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp
	}
	// CSRF: foreign origin, cross-site fetch, and no browser headers at all are refused; the session survives.
	for name, h := range map[string]map[string]string{
		"foreign origin": {"Origin": "https://evil.example"},
		"cross-site":     {"Origin": e.ts.URL, "Sec-Fetch-Site": "cross-site"},
		"no headers":     {},
	} {
		if r := post(h); r.StatusCode != http.StatusForbidden {
			t.Fatalf("%s: logout got %d, want 403", name, r.StatusCode)
		}
	}
	if b.session() == nil {
		t.Fatal("CSRF logout deleted the session")
	}
	// GET is not a logout.
	if resp, _ := b.Get(e.ts.URL + "/auth/logout"); resp.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("GET /auth/logout: %d", resp.StatusCode)
	}
	r := post(map[string]string{"Origin": e.ts.URL, "Sec-Fetch-Site": "same-origin"})
	if r.StatusCode != http.StatusNoContent {
		t.Fatalf("logout: %d", r.StatusCode)
	}
	var cleared bool
	for _, c := range r.Cookies() {
		cleared = cleared || (c.Name == SessionCookie && c.MaxAge < 0 && c.Secure && c.HttpOnly)
	}
	if !cleared {
		t.Fatal("session cookie not cleared")
	}
	if e.srv.store.Len() != 0 {
		t.Fatal("session still stored")
	}
	if _, _, err := sess.ClusterToken(e.clock.Now()); err == nil {
		t.Fatal("cluster token survived logout")
	}
	if sess.idToken != "" || sess.refresh != "" {
		t.Fatal("token strings not wiped from session memory")
	}
}

func TestMaxSessionsPerUserEvictsOldest(t *testing.T) {
	e := newEnv(t, func(c *config.Config, _ *oidctest.Provider) { c.Session.MaxPerUser = 2 })
	var bs []*browser
	for i := 0; i < 3; i++ {
		b := e.browser()
		b.login("alice-sub")
		bs = append(bs, b)
		e.clock.Advance(time.Second)
	}
	if e.srv.store.Len() != 2 {
		t.Fatalf("want 2 sessions, have %d", e.srv.store.Len())
	}
	if _, ok := e.srv.store.Lookup(bs[0].cookie(SessionCookie).Value); ok {
		t.Fatal("oldest session was not evicted")
	}
}
