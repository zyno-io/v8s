package hosted

import (
	"bytes"
	"context"
	"crypto/tls"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/zyno-io/v8s/agent/internal/cluster"
	"github.com/zyno-io/v8s/agent/internal/fake"
	"github.com/zyno-io/v8s/agent/internal/hosted/config"
	"github.com/zyno-io/v8s/agent/internal/hosted/oidctest"
)

// fakeClock is a manual clock; Advance fires due timers.
type fakeClock struct {
	mu     sync.Mutex
	now    time.Time
	timers []*fakeTimer
}

type fakeTimer struct {
	at      time.Time
	f       func()
	stopped bool
	fired   bool
}

func newFakeClock() *fakeClock { return &fakeClock{now: time.Now()} }

func (c *fakeClock) Now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.now }

func (c *fakeClock) AfterFunc(d time.Duration, f func()) func() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	t := &fakeTimer{at: c.now.Add(d), f: f}
	c.timers = append(c.timers, t)
	return func() bool {
		c.mu.Lock()
		defer c.mu.Unlock()
		was := !t.stopped && !t.fired
		t.stopped = true
		return was
	}
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(d)
	var due []*fakeTimer
	for _, t := range c.timers {
		if !t.stopped && !t.fired && !t.at.After(c.now) {
			t.fired = true
			due = append(due, t)
		}
	}
	c.mu.Unlock()
	sort.Slice(due, func(i, j int) bool { return due[i].at.Before(due[j].at) })
	for _, t := range due {
		t.f()
	}
}

const (
	loginSecret   = "login-secret-AAAA1111"
	clusterSecret = "cluster-secret-BBBB2222"
)

// env is a hosted server behind a TLS test listener, with a mock IdP and fake clusters.
type env struct {
	t      *testing.T
	idp    *oidctest.Provider
	srv    *Server
	ts     *httptest.Server
	clock  *fakeClock
	cfg    *config.Config
	logs   *syncBuf
	fsrc   *fake.Source
	mu     sync.Mutex
	creds  map[string]Creds // last creds per "<user>/<cluster>"
	builds int
}

type syncBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuf) Write(p []byte) (int, error) { s.mu.Lock(); defer s.mu.Unlock(); return s.b.Write(p) }
func (s *syncBuf) String() string              { s.mu.Lock(); defer s.mu.Unlock(); return s.b.String() }

func testConfig(issuer, public, dataDir string) *config.Config {
	return &config.Config{
		PublicURL: public, DataDir: dataDir,
		OIDC:        config.OIDC{Issuer: issuer, ClientID: "v8s-login", ClientSecret: config.SecretRef{Env: "V8S_TEST_LOGIN_SECRET"}},
		ClusterApp:  &config.ClusterApp{ClientID: "k8s-app", ClientSecret: config.SecretRef{Env: "V8S_TEST_CLUSTER_SECRET"}},
		AdminGroups: []string{"admins"},
		Clusters: []config.Cluster{
			{ID: "cluster-1", Server: "https://c1.example", Groups: []string{"devs"}, DisplayGroups: []string{"Dev"}},
			{ID: "cluster-2", Server: "https://c2.example", Groups: []string{"ops"}, Strategy: config.StrategyImpersonation,
				Impersonation: &config.Impersonation{Token: config.SecretRef{Env: "V8S_TEST_IMP"}}},
			{ID: "cluster-3", Server: "https://c3.example", Groups: []string{"*"}, Strategy: config.StrategyKiosk,
				Kiosk: &config.KioskCred{Token: config.SecretRef{Env: "V8S_TEST_KIOSK"}}},
		},
	}
}

func newEnv(t *testing.T, mut func(*config.Config, *oidctest.Provider)) *env {
	t.Helper()
	t.Setenv("V8S_TEST_LOGIN_SECRET", loginSecret)
	t.Setenv("V8S_TEST_CLUSTER_SECRET", clusterSecret)
	e := &env{t: t, clock: newFakeClock(), logs: &syncBuf{}, creds: map[string]Creds{}}
	e.idp = oidctest.New()
	t.Cleanup(e.idp.Close)
	e.idp.Now = e.clock.Now
	e.idp.AddUser(oidctest.User{Sub: "alice-sub", Username: "alice@example.com", Groups: []string{"devs"}})
	e.idp.AddUser(oidctest.User{Sub: "bob-sub", Username: "bob@example.com", Groups: []string{"ops"}})
	e.idp.AddUser(oidctest.User{Sub: "root-sub", Username: "root@example.com", Groups: []string{"admins"}})

	var handler http.Handler
	e.ts = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { handler.ServeHTTP(w, r) }))
	t.Cleanup(e.ts.Close)
	e.idp.AddClient(oidctest.Client{ID: "v8s-login", Secret: loginSecret, RedirectURI: e.ts.URL + "/auth/callback"})
	e.idp.AddClient(oidctest.Client{ID: "k8s-app", Secret: clusterSecret, RedirectURI: e.ts.URL + "/auth/cluster-callback"})

	e.cfg = testConfig(e.idp.Issuer(), e.ts.URL, t.TempDir())
	if mut != nil {
		mut(e.cfg, e.idp)
	}
	e.cfg.Clusters = append([]config.Cluster(nil), e.cfg.Clusters...)
	// Defaults and validation exactly as a loaded config would get them.
	b := mustYAML(t, e.cfg)
	cfg, err := config.Parse(b)
	if err != nil {
		t.Fatal(err)
	}
	e.cfg = cfg
	e.fsrc = fake.NewSource(fake.Config{Clusters: 3, Pods: 300})
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	log := newTestLogger(e.logs)
	srv, err := New(ctx, Options{Config: cfg, Log: log, Clock: e.clock, HTTP: e.ts.Client(),
		CorrelationKey: []byte("test-corr-key"),
		Factory: func(spec config.Cluster, c Creds) (cluster.Backend, error) {
			e.mu.Lock()
			e.creds[c.Identity.Username+"/"+spec.ID] = c
			e.builds++
			e.mu.Unlock()
			return e.fsrc.Backend(fake.Name(int(spec.ID[len("cluster-")] - '1')))
		}})
	if err != nil {
		t.Fatal(err)
	}
	e.srv = srv
	handler = srv.Handler()
	t.Cleanup(srv.Close)
	return e
}

// browser is an HTTP client with a cookie jar that trusts the test TLS certificate and does not follow
// redirects automatically (so tests can see every hop).
type browser struct {
	*http.Client
	e *env
}

func (e *env) browser() *browser {
	jar, _ := cookiejar.New(nil)
	tr := e.ts.Client().Transport.(*http.Transport).Clone()
	tr.TLSClientConfig = &tls.Config{InsecureSkipVerify: true}
	return &browser{Client: &http.Client{Jar: jar, Transport: tr,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}, e: e}
}

// follow GETs a URL and follows 3xx hops; it returns every response seen (bodies closed).
func (b *browser) follow(u string) []*http.Response {
	b.e.t.Helper()
	var out []*http.Response
	for i := 0; i < 12; i++ {
		resp, err := b.Get(u)
		if err != nil {
			b.e.t.Fatal(err)
		}
		resp.Body.Close()
		out = append(out, resp)
		if resp.StatusCode < 300 || resp.StatusCode > 399 {
			return out
		}
		next, err := resp.Request.URL.Parse(resp.Header.Get("Location"))
		if err != nil {
			b.e.t.Fatal(err)
		}
		u = next.String()
	}
	b.e.t.Fatal("too many redirects")
	return nil
}

// ssoLogin signs the browser in at the IdP (so prompt=none succeeds).
func (b *browser) ssoLogin(sub string) {
	b.e.t.Helper()
	b.follow(b.e.idp.URL + "/mock/login?sub=" + url.QueryEscape(sub))
}

// login runs the whole sign-in and returns the responses of every hop.
func (b *browser) login(sub string) []*http.Response {
	b.e.t.Helper()
	b.ssoLogin(sub)
	return b.follow(b.e.ts.URL + "/auth/login?return=/clusters")
}

func (b *browser) cookie(name string) *http.Cookie {
	u, _ := url.Parse(b.e.ts.URL)
	for _, c := range b.Jar.Cookies(u) {
		if c.Name == name {
			return c
		}
	}
	return nil
}

// session returns the server-side session behind the browser's cookie.
func (b *browser) session() *Session {
	c := b.cookie(SessionCookie)
	if c == nil {
		return nil
	}
	b.e.srv.store.mu.Lock()
	defer b.e.srv.store.mu.Unlock()
	return b.e.srv.store.byID[c.Value]
}
