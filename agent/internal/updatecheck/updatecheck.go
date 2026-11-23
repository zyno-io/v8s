// Package updatecheck asks v8s.io (a static release.json published with the site once a release exists), every
// 15 minutes by default, whether a newer stable v8s release exists. The result is cached in memory (and optionally a
// file) and read by the update.status request; no render or request path touches the network.
package updatecheck

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/Masterminds/semver/v3"

	"github.com/zyno-io/v8s/agent/pkg/protocol"
)

type Options struct {
	Current   string        // version.Version
	Repo      string        // default "zyno-io/v8s"
	CachePath string        // "" = memory only
	Interval  time.Duration // default 15m
	RetryMin  time.Duration // default 5m (tests shrink)
	URL       string        // default DefaultURL (tests: httptest URL)
	HTTP      *http.Client  // default &http.Client{Timeout: 10 * time.Second} (default transport: honors HTTPS_PROXY)
	Now       func() time.Time
	Log       *slog.Logger
}

// DefaultURL is the static file the site publishes; its shape is {"version": "X.Y.Z"} (sites/www/src/pages/release.json.ts).
const DefaultURL = "https://v8s.io/release.json"

// statusWait bounds how long Status waits for this process's first check.
const statusWait = 10 * time.Second

// describeSuffix matches the prerelease git describe leaves on a build made after a tag ("3-gabc1234").
var describeSuffix = regexp.MustCompile(`(^|[.-])\d+-g[0-9a-f]+(-dirty)?$`) // also after an rc: 0.5.0-rc.1-3-gabc1234

func parse(v string) (*semver.Version, error) {
	return semver.StrictNewVersion(strings.TrimPrefix(v, "v"))
}

// Checkable reports whether v is a release build. Development, branch and git-describe builds are not: they
// would either never match a release or rank below the tag they came from and nag forever.
func Checkable(v string) bool {
	sv, err := parse(v)
	if err != nil {
		return false
	}
	if sv.Major() == 0 && sv.Minor() == 0 && sv.Patch() == 0 {
		return false
	}
	pre := sv.Prerelease()
	return !(describeSuffix.MatchString(pre) || strings.HasSuffix(pre, "dirty"))
}

type cacheFile struct {
	CheckedAt time.Time `json:"checkedAt"`
	Latest    string    `json:"latest"`
	ETag      string    `json:"etag,omitempty"`
}

type Checker struct {
	o       Options
	current *semver.Version

	firstDone chan struct{}
	firstOnce sync.Once

	mu       sync.Mutex
	status   protocol.UpdateStatus
	latest   string // newest stable version seen (no "v"), newer than current or not
	etag     string
	checked  time.Time
	failures int
	failLog  bool
	seen     string // version already logged as available
}

// New returns nil when o.Current is not a release build.
func New(o Options) *Checker {
	if !Checkable(o.Current) {
		return nil
	}
	if o.Repo == "" {
		o.Repo = "zyno-io/v8s"
	}
	if o.Interval <= 0 {
		o.Interval = 15 * time.Minute
	}
	if o.RetryMin <= 0 {
		o.RetryMin = 5 * time.Minute
	}
	if o.URL == "" {
		o.URL = DefaultURL
	}
	if o.HTTP == nil {
		o.HTTP = &http.Client{Timeout: 10 * time.Second}
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.Log == nil {
		o.Log = slog.New(slog.DiscardHandler)
	}
	cur, _ := parse(o.Current)
	c := &Checker{o: o, current: cur, firstDone: make(chan struct{})}
	if cf, ok := c.loadCache(); ok {
		age := o.Now().Sub(cf.CheckedAt)
		if _, err := stable(cf.Latest); err == nil && age >= 0 && age < o.Interval {
			c.store(cf.Latest, cf.ETag, cf.CheckedAt)
			c.firstOnce.Do(func() { close(c.firstDone) })
		}
	}
	return c
}

func stable(tag string) (*semver.Version, error) {
	v, err := parse(tag)
	if err != nil {
		return nil, err
	}
	if v.Prerelease() != "" {
		return nil, errors.New("prerelease")
	}
	return v, nil
}

func (c *Checker) loadCache() (cacheFile, bool) {
	var cf cacheFile
	if c.o.CachePath == "" {
		return cf, false
	}
	b, err := os.ReadFile(c.o.CachePath)
	if err != nil || json.Unmarshal(b, &cf) != nil {
		return cacheFile{}, false
	}
	return cf, true
}

// saveCache is best effort: a read-only config dir just means one more request next launch.
func (c *Checker) saveCache(cf cacheFile) {
	if c.o.CachePath == "" {
		return
	}
	b, err := json.Marshal(cf)
	if err != nil {
		return
	}
	dir := filepath.Dir(c.o.CachePath)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return
	}
	tmp, err := os.CreateTemp(dir, ".update-check-*")
	if err != nil {
		return
	}
	_, werr := tmp.Write(b)
	cerr := tmp.Close()
	if werr != nil || cerr != nil || os.Rename(tmp.Name(), c.o.CachePath) != nil {
		_ = os.Remove(tmp.Name())
	}
}

// store records a result and computes the status once, so readers do no work.
func (c *Checker) store(latest, etag string, at time.Time) {
	latest = strings.TrimPrefix(latest, "v")
	c.mu.Lock()
	c.latest, c.etag, c.checked = latest, etag, at
	c.status = protocol.UpdateStatus{}
	announce := false
	if lv, err := stable(latest); err == nil && lv.GreaterThan(c.current) {
		c.status = protocol.UpdateStatus{Latest: latest, URL: "https://github.com/" + c.o.Repo + "/releases/tag/v" + latest}
		announce = c.seen != latest
		c.seen = latest
	}
	st := c.status
	c.mu.Unlock()
	if announce {
		c.o.Log.Info("update available", "current", c.o.Current, "latest", st.Latest, "url", st.URL)
	}
}

// Status returns the update to offer, waiting (bounded) for this process's first check.
func (c *Checker) Status(ctx context.Context) protocol.UpdateStatus {
	if c == nil {
		return protocol.UpdateStatus{}
	}
	select {
	case <-c.firstDone:
	case <-ctx.Done():
		return protocol.UpdateStatus{}
	case <-time.After(statusWait):
		return protocol.UpdateStatus{}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.status
}

// Run checks now (unless a fresh cached result was adopted) and then on schedule until ctx ends.
func (c *Checker) Run(ctx context.Context) {
	if c == nil {
		return
	}
	c.mu.Lock()
	var wait time.Duration
	if !c.checked.IsZero() {
		wait = max(c.o.Interval-c.o.Now().Sub(c.checked), 0)
	}
	c.mu.Unlock()
	for {
		if wait > 0 {
			t := time.NewTimer(wait)
			select {
			case <-ctx.Done():
				t.Stop()
				return
			case <-t.C:
			}
		}
		if ctx.Err() != nil {
			return
		}
		wait = c.check(ctx)
		c.firstOnce.Do(func() { close(c.firstDone) })
	}
}

// backoff is RetryMin doubled per prior failure, capped at Interval (15m by default).
func (c *Checker) backoff(failures int) time.Duration {
	d := c.o.RetryMin
	for i := 1; i < failures && d < c.o.Interval; i++ {
		d *= 2
	}
	return min(d, c.o.Interval)
}

// check runs one request and returns the delay until the next one.
func (c *Checker) check(ctx context.Context) time.Duration {
	c.mu.Lock()
	etag, latest := c.etag, c.latest
	c.mu.Unlock()
	tag, newETag, notModified, err := c.fetch(ctx, etag)
	now := c.o.Now()
	if err != nil {
		c.mu.Lock()
		c.failures++
		n, first := c.failures, !c.failLog
		c.failLog = true
		c.mu.Unlock()
		if first {
			c.o.Log.Info("update check failed (later failures are not logged)", "err", err)
		}
		return c.backoff(n)
	}
	c.mu.Lock()
	c.failures = 0
	c.mu.Unlock()
	if notModified {
		tag, newETag = latest, etag
	}
	c.store(tag, newETag, now)
	c.saveCache(cacheFile{CheckedAt: now, Latest: tag, ETag: newETag})
	return c.o.Interval
}

func (c *Checker) fetch(ctx context.Context, etag string) (tag, newETag string, notModified bool, err error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.o.URL, nil)
	if err != nil {
		return "", "", false, err
	}
	req.Header.Set("User-Agent", "v8s")
	if etag != "" {
		req.Header.Set("If-None-Match", etag)
	}
	resp, err := c.o.HTTP.Do(req)
	if err != nil {
		return "", "", false, err
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusNotModified:
		if etag == "" {
			return "", "", false, errors.New("unexpected 304")
		}
		return "", "", true, nil
	case http.StatusOK:
	default:
		return "", "", false, fmt.Errorf("update check: %s", resp.Status)
	}
	var body struct {
		Version string `json:"version"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&body); err != nil {
		return "", "", false, err
	}
	v, err := stable(body.Version)
	if err != nil {
		return "", "", false, fmt.Errorf("update check: unusable version %q", body.Version)
	}
	return v.String(), resp.Header.Get("ETag"), false, nil
}
