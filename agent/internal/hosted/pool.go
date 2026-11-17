package hosted

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/zyno-io/v8s/agent/internal/cluster"
	"github.com/zyno-io/v8s/agent/internal/drainer"
	"github.com/zyno-io/v8s/agent/internal/flux"
	"github.com/zyno-io/v8s/agent/internal/helm"
	"github.com/zyno-io/v8s/agent/internal/ingest"
	"github.com/zyno-io/v8s/agent/internal/jobs"
	"github.com/zyno-io/v8s/agent/internal/kube"
	"github.com/zyno-io/v8s/agent/internal/ops"
	"github.com/zyno-io/v8s/agent/internal/settings"
	"github.com/zyno-io/v8s/agent/internal/streams"
	"github.com/zyno-io/v8s/agent/internal/version"
	"github.com/zyno-io/v8s/agent/internal/view"
	"github.com/zyno-io/v8s/agent/internal/wire"
	"github.com/zyno-io/v8s/agent/pkg/protocol"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

// EngineGrace is how long a user's watches keep running after their last client closed.
const EngineGrace = 30 * time.Second

// jobPoll is how often a watch-less engine checks whether its jobs finished.
const jobPoll = 10 * time.Second

// activation is the running part of a user's engine: watches, views, problems, and the wire front end. It is
// created on the user's first client and torn down EngineGrace after their last one.
type activation struct {
	cancel  context.CancelFunc
	eng     *view.Engine
	mgr     *cluster.Manager
	streams *streams.Manager
	srv     *wire.Server
}

// userEngine is one identity's engine (same subject and groups). Its durable parts (settings, jobs) outlive an
// activation so a running drain keeps its 15-minute bound after the watches stop.
type userEngine struct {
	pool  *Pool
	key   string
	ident Identity
	corr  string
	set   *settings.Store
	jobs  *jobs.Manager
	src   *userSource
	ctx   context.Context
	stop  context.CancelFunc

	mu       sync.Mutex
	sessions map[*Session]func() // live sessions of this identity -> remover of their close hook
	refs     int
	act      *activation
	timer    func() bool // pending grace stop
	poll     func() bool // pending jobs poll
	gone     bool
}

// clusterToken is the token source of oidc-passthrough clusters: the live session of this identity holding the
// token that lives longest.
func (u *userEngine) clusterToken() (string, error) {
	now := u.pool.s.clock.Now()
	u.mu.Lock()
	ss := make([]*Session, 0, len(u.sessions))
	for s := range u.sessions {
		ss = append(ss, s)
	}
	u.mu.Unlock()
	var best string
	var bestExp time.Time
	for _, s := range ss {
		if t, exp, err := s.ClusterToken(now); err == nil && exp.After(bestExp) {
			best, bestExp = t, exp
		}
	}
	if best == "" {
		return "", errNoClusterToken
	}
	return best, nil
}

// Pool owns the per-user engines.
type Pool struct {
	s       *Server
	grace   time.Duration
	mu      sync.Mutex
	engines map[string]*userEngine
}

func newPool(s *Server) *Pool {
	return &Pool{s: s, grace: EngineGrace, engines: map[string]*userEngine{}}
}

// Get returns the active engine of a session's identity (nil when none runs).
func (p *Pool) Get(sess *Session) *userEngine {
	p.mu.Lock()
	ue := p.engines[sess.Identity.Key()]
	p.mu.Unlock() // never wait for ue.mu while holding the pool lock
	if ue == nil {
		return nil
	}
	ue.mu.Lock()
	defer ue.mu.Unlock()
	if ue.act == nil {
		return nil
	}
	return ue
}

// Active is the number of engines with running watches (for tests and metrics).
func (p *Pool) Active() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	n := 0
	for _, ue := range p.engines {
		ue.mu.Lock()
		if ue.act != nil {
			n++
		}
		ue.mu.Unlock()
	}
	return n
}

// Retained is the number of engines in the pool, with or without running watches.
func (p *Pool) Retained() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.engines)
}

var errSessionEnded = errors.New("session ended")

// Acquire starts (or reuses) the identity's engine and counts one client. release must be called exactly once
// when the client closes; 30 s after the last release the watches stop.
func (p *Pool) Acquire(sess *Session) (*userEngine, func(), error) {
	key := sess.Identity.Key()
	p.mu.Lock()
	ue := p.engines[key]
	if ue == nil {
		var err error
		if ue, err = p.s.newUserEngine(p, key, sess); err != nil {
			p.mu.Unlock()
			return nil, nil, err
		}
		p.engines[key] = ue
	}
	p.mu.Unlock()

	ue.mu.Lock()
	if ue.gone {
		ue.mu.Unlock()
		return nil, nil, errors.New("engine is shutting down; retry")
	}
	if ue.timer != nil {
		ue.timer()
		ue.timer = nil
	}
	if ue.poll != nil {
		ue.poll()
		ue.poll = nil
	}
	if _, ok := ue.sessions[sess]; !ok {
		// The hook locks ue.mu, so it must never run inline under it: TryOnClose refuses a session that ended
		// after the caller looked it up.
		rm, ok := sess.TryOnClose(func() { ue.mu.Lock(); delete(ue.sessions, sess); ue.mu.Unlock() })
		if !ok {
			ue.mu.Unlock()
			return nil, nil, errSessionEnded
		}
		ue.sessions[sess] = rm
	}
	if ue.act == nil {
		ue.act = p.s.activate(ue)
	}
	ue.refs++
	ue.mu.Unlock()

	var once sync.Once
	return ue, func() { once.Do(func() { p.release(ue) }) }, nil
}

func (p *Pool) release(ue *userEngine) {
	ue.mu.Lock()
	defer ue.mu.Unlock()
	if ue.refs > 0 {
		ue.refs--
	}
	if ue.refs == 0 && ue.timer == nil && !ue.gone {
		ue.timer = p.s.clock.AfterFunc(p.grace, func() { p.idleExpired(ue) })
	}
}

// idleExpired runs EngineGrace after the last client closed: stop the watches. If jobs are still running (they
// are bounded to 15 minutes without a client by the jobs manager) the engine stays, watch-less, until they end.
func (p *Pool) idleExpired(ue *userEngine) {
	ue.mu.Lock()
	ue.timer = nil
	if ue.refs > 0 || ue.gone {
		ue.mu.Unlock()
		return
	}
	act := ue.act
	ue.act = nil
	ue.mu.Unlock()
	if act != nil {
		act.close()
	}
	p.maybeRetire(ue)
}

func (p *Pool) maybeRetire(ue *userEngine) {
	running := false
	for _, j := range ue.jobs.List() {
		running = running || j.State == protocol.JobRunning
	}
	ue.mu.Lock()
	if ue.refs > 0 || ue.gone || ue.act != nil || ue.timer != nil {
		ue.mu.Unlock()
		return
	}
	if running {
		ue.poll = p.s.clock.AfterFunc(jobPoll, func() {
			ue.mu.Lock()
			ue.poll = nil
			ue.mu.Unlock()
			p.maybeRetire(ue)
		})
		ue.mu.Unlock()
		return
	}
	ue.gone = true
	rms := ue.drainSessionsLocked()
	ue.mu.Unlock()
	for _, rm := range rms {
		rm()
	}
	p.mu.Lock()
	if p.engines[ue.key] == ue {
		delete(p.engines, ue.key)
	}
	p.mu.Unlock()
	ue.stop()
}

// drainSessionsLocked forgets the engine's sessions and returns their hook removers (so a retired engine is not
// kept reachable from long-lived sessions).
func (u *userEngine) drainSessionsLocked() []func() {
	rms := make([]func(), 0, len(u.sessions))
	for s, rm := range u.sessions {
		rms = append(rms, rm)
		delete(u.sessions, s)
	}
	return rms
}

// Close stops every engine (server shutdown).
func (p *Pool) Close() {
	p.mu.Lock()
	all := make([]*userEngine, 0, len(p.engines))
	for _, ue := range p.engines {
		all = append(all, ue)
	}
	p.engines = map[string]*userEngine{}
	p.mu.Unlock()
	for _, ue := range all {
		ue.mu.Lock()
		ue.gone = true
		act := ue.act
		ue.act = nil
		rms := ue.drainSessionsLocked()
		ue.mu.Unlock()
		for _, rm := range rms {
			rm()
		}
		if act != nil {
			act.close()
		}
		ue.stop()
	}
}

func (a *activation) close() {
	a.mgr.Close()
	a.streams.Close()
	a.cancel()
}

// ---- construction ---------------------------------------------------------------------------------

func (s *Server) corrFor(sess *Session) string {
	m := hmac.New(sha256.New, s.corrKey)
	m.Write([]byte(sess.id))
	return hex.EncodeToString(m.Sum(nil)[:8])
}

func (s *Server) settingsPath(sub string) string {
	sum := sha256.Sum256([]byte(sub))
	return filepath.Join(s.cfg.DataDir, "users", hex.EncodeToString(sum[:16])+".json")
}

// maxSettingsBytes caps one user's settings document on a shared server.
const maxSettingsBytes = 256 << 10

// settingsStore returns the user's settings store, shared by every engine of that subject (an identity with a
// different group set has its own engine but the same settings file).
func (s *Server) settingsStore(sub, path string) (*settings.Store, error) {
	s.smu.Lock()
	defer s.smu.Unlock()
	if st := s.stores[sub]; st != nil {
		return st, nil
	}
	st, err := settings.Open(path)
	if err != nil {
		return nil, err
	}
	st.MaxBytes = maxSettingsBytes
	if s.stores == nil {
		s.stores = map[string]*settings.Store{}
	}
	s.stores[sub] = st
	return st, nil
}

// newUserEngine builds the durable part of a user's engine. Called with pool.mu held.
func (s *Server) newUserEngine(p *Pool, key string, sess *Session) (*userEngine, error) {
	path := s.settingsPath(sess.Identity.Sub)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	set, err := s.settingsStore(sess.Identity.Sub, path)
	if err != nil {
		return nil, err
	}
	ctx, stop := context.WithCancel(s.baseCtx)
	ue := &userEngine{pool: p, key: key, ident: sess.Identity, corr: s.corrFor(sess), set: set,
		ctx: ctx, stop: stop, sessions: map[*Session]func(){}}
	ue.jobs = jobs.New(ctx, s.clock)
	ue.src = &userSource{srv: s, ue: ue, visible: visibleClusters(s.cfg, sess.Identity)}
	return ue, nil
}

// activate starts the watches/views/wire front end of a user's engine (mirrors the desktop agent's wiring).
// Port-forward is disabled in hosted mode (a local port is meaningless here); HTTP preview is not built yet.
func (s *Server) activate(ue *userEngine) *activation {
	ctx, cancel := context.WithCancel(ue.ctx)
	log := s.log.With("user", ue.ident.Username)
	src := ue.src
	eng := view.NewEngine()
	mgr := cluster.NewManager(ctx, src, eng, log)
	mgr.SetSettings(ue.set)
	connFn := func(id string) (kube.Conn, error) { return src.Backend(id) }
	opsSvc := ops.New(ops.Config{Conn: connFn, Links: mgr.Links, Resolve: mgr.Resolve, Now: s.clock.Now})
	streamMgr := streams.New(streams.Config{Router: opsSvc, Conn: connFn, Settings: ue.set.Get, AllowPortForward: false,
		User: ue.ident.Username, Log: log})
	drainSvc := drainer.New(drainer.Config{Router: opsSvc, Conn: connFn})
	fluxSvc := flux.New(flux.Config{
		Get: func(ctx context.Context, cl, gvr, ns, name string) (*unstructured.Unstructured, error) {
			g, v, r, err := ingest.ParseGVR(gvr)
			if err != nil {
				return nil, err
			}
			c, err := connFn(cl)
			if err != nil {
				return nil, err
			}
			ri := c.Dynamic().Resource(schema.GroupVersionResource{Group: g, Version: v, Resource: r})
			if ns != "" {
				return ri.Namespace(ns).Get(ctx, name, metav1.GetOptions{})
			}
			return ri.Get(ctx, name, metav1.GetOptions{})
		},
		Scan:     mgr.ScanKind,
		Clusters: func() []string { return mgr.List().Selected },
		Resolve:  mgr.Resolve,
	})
	helmCfg := helm.Config{
		Runtime: func(id string) (kube.Runtime, error) {
			c, err := connFn(id)
			if err != nil {
				return nil, err
			}
			rt, ok := kube.RuntimeOf(c)
			if !ok {
				return nil, errors.New("this cluster is read-only or has no runtime")
			}
			return rt, nil
		},
		Route: func(ctx context.Context, cl, verb, gvr, sub, ns, name string) (string, bool, error) {
			r, err := opsSvc.Route(ctx, cl, verb, gvr, sub, ns, name)
			if err != nil {
				return "", false, err
			}
			return r.ContextID, r.ViaWrite, nil
		},
		Log: log,
	}
	go eng.Run(ctx)
	srv := wire.NewServer(wire.Options{
		Manager: mgr, Engine: eng, Ops: opsSvc, Settings: ue.set, Streams: streamMgr, Jobs: ue.jobs, Drain: drainSvc, Flux: fluxSvc,
		Helm: helm.New(helmCfg), Mode: src.Mode(), AgentVersion: version.Version, PreAuthorized: true, Log: log, Tick: s.tick,
	})
	return &activation{cancel: cancel, eng: eng, mgr: mgr, streams: streamMgr, srv: srv}
}

// handler returns the wire handler of the active engine.
func (u *userEngine) handler() http.Handler {
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.act == nil {
		return nil
	}
	return u.act.srv.Handler()
}
