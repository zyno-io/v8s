package updatecheck

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestCheckable(t *testing.T) {
	for v, want := range map[string]bool{
		"0.4.0": true, "v0.4.0": true, "0.5.0-rc1": true,
		"0.0.0-dev": false, "0.0.0-main.gabc1234": false, "0.4.0-3-gabc1234": false, "0.5.0-rc.1-3-gabc1234": false, "0.5.0-rc1-3-gabc1234": false, "0.5.0-rc.1-3-gabc1234-dirty": false, "0.4.0-3-gabc1234-dirty": false,
		"0.4.0-dirty": false, "abc1234": false, "": false,
	} {
		if got := Checkable(v); got != want {
			t.Errorf("Checkable(%q) = %v, want %v", v, got, want)
		}
	}
	if New(Options{Current: "0.0.0-dev"}) != nil {
		t.Error("dev build got a checker")
	}
}

// gh is a fake GitHub: it records requests and serves a scripted tag.
type gh struct {
	*httptest.Server
	mu    sync.Mutex
	tag   string
	etag  string
	code  int // 0 = 200
	body  string
	reqs  []*http.Request
	count atomic.Int32
}

func newGH(t *testing.T, tag string) *gh {
	g := &gh{tag: tag, etag: `"e1"`}
	g.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		g.count.Add(1)
		g.mu.Lock()
		defer g.mu.Unlock()
		g.reqs = append(g.reqs, r)
		if r.URL.Path != "/repos/zyno-io/v8s/releases/latest" {
			http.NotFound(w, r)
			return
		}
		if g.code != 0 {
			w.WriteHeader(g.code)
			return
		}
		if g.etag != "" && r.Header.Get("If-None-Match") == g.etag {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		w.Header().Set("ETag", g.etag)
		if g.body != "" {
			_, _ = w.Write([]byte(g.body))
			return
		}
		_, _ = w.Write([]byte(`{"tag_name":"` + g.tag + `","html_url":"https://evil.example/x"}`))
	}))
	t.Cleanup(g.Close)
	return g
}

func (g *gh) set(f func()) { g.mu.Lock(); f(); g.mu.Unlock() }

func mk(g *gh, cur string, mut func(*Options)) *Checker {
	o := Options{Current: cur, API: g.URL, RetryMin: time.Millisecond, Interval: time.Hour}
	if mut != nil {
		mut(&o)
	}
	return New(o)
}

func TestNewer(t *testing.T) {
	for _, c := range []struct {
		cur, latest string
		want        string
	}{
		{"0.9.0", "0.10.0", "0.10.0"},
		{"0.5.0-rc1", "0.5.0", "0.5.0"},
		{"0.4.0", "0.4.0", ""},
		{"0.4.0", "0.3.3", ""},
		{"0.4.0", "v0.4.1", "0.4.1"},
	} {
		g := newGH(t, c.latest)
		chk := mk(g, c.cur, nil)
		chk.check(context.Background())
		chk.firstOnce.Do(func() { close(chk.firstDone) })
		got := chk.Status(context.Background())
		if got.Latest != c.want {
			t.Errorf("%s -> %s: %+v, want %q", c.cur, c.latest, got, c.want)
		}
		if c.want != "" && got.URL != "https://github.com/zyno-io/v8s/releases/tag/v"+c.want {
			t.Errorf("url %q", got.URL)
		}
	}
}

func TestRequestHeadersAndETag(t *testing.T) {
	g := newGH(t, "0.5.0")
	chk := mk(g, "0.4.0", nil)
	chk.check(context.Background())
	chk.check(context.Background())
	g.mu.Lock()
	defer g.mu.Unlock()
	if len(g.reqs) != 2 {
		t.Fatalf("%d requests", len(g.reqs))
	}
	h := g.reqs[0].Header
	if h.Get("Accept") != "application/vnd.github+json" || h.Get("X-GitHub-Api-Version") != "2022-11-28" || h.Get("User-Agent") != "v8s" {
		t.Errorf("headers: %v", h)
	}
	if h.Get("If-None-Match") != "" || g.reqs[1].Header.Get("If-None-Match") != `"e1"` {
		t.Errorf("etag: first %q second %q", h.Get("If-None-Match"), g.reqs[1].Header.Get("If-None-Match"))
	}
	if chk.status.Latest != "0.5.0" { // 304 keeps the result
		t.Errorf("status after 304: %+v", chk.status)
	}
}

func TestFailuresGiveEmptyStatusAndBackOff(t *testing.T) {
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(time.Second):
		}
	}))
	defer slow.Close()
	big := newGH(t, "")
	big.body = `{"tag_name":"0.5.0","pad":"` + strings.Repeat("x", 2<<20) + `"}`
	cases := map[string]*gh{"403": newGH(t, ""), "500": newGH(t, ""), "404": newGH(t, "")}
	cases["403"].code, cases["500"].code, cases["404"].code = 403, 500, 404
	for _, tag := range []string{"latest", "v1.2", "v0.5.0-rc1"} {
		cases["tag "+tag] = newGH(t, tag)
	}
	cases["oversized"] = big
	for name, g := range cases {
		chk := mk(g, "0.4.0", nil)
		if d := chk.check(context.Background()); d != time.Millisecond {
			t.Errorf("%s: retry %v", name, d)
		}
		chk.firstOnce.Do(func() { close(chk.firstDone) })
		if st := chk.Status(context.Background()); st.Latest != "" {
			t.Errorf("%s: status %+v", name, st)
		}
	}
	// timeout
	chk := New(Options{Current: "0.4.0", API: slow.URL, RetryMin: time.Millisecond, HTTP: &http.Client{Timeout: 20 * time.Millisecond}})
	if d := chk.check(context.Background()); d != time.Millisecond {
		t.Errorf("timeout retry %v", d)
	}
	// backoff doubles to the cap
	b := New(Options{Current: "0.4.0", RetryMin: time.Hour, Interval: 24 * time.Hour})
	var got []time.Duration
	for n := 1; n <= 7; n++ {
		got = append(got, b.backoff(n))
	}
	want := []time.Duration{1, 2, 4, 8, 16, 24, 24}
	for i := range want {
		if got[i] != want[i]*time.Hour {
			t.Fatalf("backoff %v", got)
		}
	}
}

func TestOversizedBodyIsFailure(t *testing.T) {
	g := newGH(t, "")
	g.body = strings.Repeat(" ", 2<<20) + `{"tag_name":"0.5.0"}`
	chk := mk(g, "0.4.0", nil)
	chk.check(context.Background())
	if chk.status.Latest != "" || chk.failures != 1 {
		t.Fatalf("status %+v failures %d", chk.status, chk.failures)
	}
}

func TestHTMLURLIgnored(t *testing.T) {
	g := newGH(t, "0.5.0")
	chk := mk(g, "0.4.0", nil)
	chk.check(context.Background())
	if u := chk.status.URL; strings.Contains(u, "evil") || !strings.HasPrefix(u, "https://github.com/zyno-io/v8s/") {
		t.Fatalf("url %q", u)
	}
}

func TestCache(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "update-check.json")
	g := newGH(t, "0.5.0")
	now := time.Now()
	clock := func() time.Time { return now }
	chk := mk(g, "0.4.0", func(o *Options) { o.CachePath = path; o.Now = clock })
	chk.check(context.Background())
	if g.count.Load() != 1 {
		t.Fatal("first request")
	}
	// fresh: no request, Status immediate (Run not even started)
	g2 := newGH(t, "0.6.0")
	fresh := New(Options{Current: "0.4.0", API: g2.URL, CachePath: path, Now: func() time.Time { return now.Add(time.Hour) }})
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if st := fresh.Status(ctx); st.Latest != "0.5.0" {
		t.Fatalf("fresh status %+v", st)
	}
	if g2.count.Load() != 0 {
		t.Fatal("fresh cache made a request")
	}
	// stale: requests on Run
	stale := New(Options{Current: "0.4.0", API: g2.URL, CachePath: path, Now: func() time.Time { return now.Add(48 * time.Hour) }})
	rctx, rcancel := context.WithCancel(context.Background())
	go stale.Run(rctx)
	if st := stale.Status(ctx); st.Latest != "0.6.0" || g2.count.Load() != 1 {
		t.Fatalf("stale status %+v requests %d", st, g2.count.Load())
	}
	rcancel()
	// corrupt: ignored
	if err := os.WriteFile(path, []byte("{nope"), 0o600); err != nil {
		t.Fatal(err)
	}
	cc := New(Options{Current: "0.4.0", API: g2.URL, CachePath: path})
	cctx, ccancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer ccancel()
	if st := cc.Status(cctx); st.Latest != "" { // no Run: not answered, ctx ends
		t.Fatalf("corrupt cache status %+v", st)
	}
}

func TestStatusWaitsForFirstCheck(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-release
		_, _ = w.Write([]byte(`{"tag_name":"0.5.0"}`))
	}))
	defer srv.Close()
	chk := New(Options{Current: "0.4.0", API: srv.URL, Interval: time.Hour})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go chk.Run(ctx)
	done := make(chan string, 1)
	go func() { done <- chk.Status(context.Background()).Latest }()
	select {
	case <-done:
		t.Fatal("Status answered before the first check ended")
	case <-time.After(50 * time.Millisecond):
	}
	close(release)
	if got := <-done; got != "0.5.0" {
		t.Fatalf("got %q", got)
	}
	// ctx cancel is honored
	blocked := New(Options{Current: "0.4.0"})
	cctx, ccancel := context.WithCancel(context.Background())
	ccancel()
	if st := blocked.Status(cctx); st.Latest != "" {
		t.Fatal(st)
	}
}

func TestNilChecker(t *testing.T) {
	var c *Checker
	c.Run(context.Background())
	if st := c.Status(context.Background()); st.Latest != "" || st.URL != "" {
		t.Fatal(st)
	}
}
