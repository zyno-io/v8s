// Package engine builds one v8s engine (cluster manager, views, ops, streams, jobs, flux, helm, problems,
// security, Hubble and the wire front end) from a cluster Source. The desktop agent, serve/fake mode and every
// hosted user's engine are all built here so they cannot drift apart.
package engine

import (
	"context"
	"errors"
	"log/slog"
	"sync/atomic"
	"time"

	"github.com/zyno-io/v8s/agent/internal/cluster"
	"github.com/zyno-io/v8s/agent/internal/drainer"
	"github.com/zyno-io/v8s/agent/internal/flux"
	"github.com/zyno-io/v8s/agent/internal/helm"
	"github.com/zyno-io/v8s/agent/internal/hubble"
	"github.com/zyno-io/v8s/agent/internal/ingest"
	"github.com/zyno-io/v8s/agent/internal/jobs"
	"github.com/zyno-io/v8s/agent/internal/kube"
	_ "github.com/zyno-io/v8s/agent/internal/kubescape"
	_ "github.com/zyno-io/v8s/agent/internal/kyverno"
	"github.com/zyno-io/v8s/agent/internal/ops"
	_ "github.com/zyno-io/v8s/agent/internal/platform"
	"github.com/zyno-io/v8s/agent/internal/problems"
	"github.com/zyno-io/v8s/agent/internal/rpc"
	"github.com/zyno-io/v8s/agent/internal/security"
	"github.com/zyno-io/v8s/agent/internal/settings"
	"github.com/zyno-io/v8s/agent/internal/streams"
	_ "github.com/zyno-io/v8s/agent/internal/trivy"
	"github.com/zyno-io/v8s/agent/internal/version"
	"github.com/zyno-io/v8s/agent/internal/view"
	"github.com/zyno-io/v8s/agent/internal/wire"
	"github.com/zyno-io/v8s/agent/pkg/protocol"
	"helm.sh/helm/v4/pkg/action"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

// Config describes one engine.
type Config struct {
	Ctx      context.Context
	Source   cluster.Source
	Settings *settings.Store
	Log      *slog.Logger
	// Now is the clock of ops (default time.Now); Tick the wire patch interval (default 100 ms).
	Now  func() time.Time
	Tick time.Duration
	// User is the identity recorded on streams.
	User string
	// AllowPortForward enables port-forward streams (off for hosted engines).
	AllowPortForward bool
	// FakeHelm stores helm release records in the fake clientset (fake mode).
	FakeHelm bool
	// RuntimeErr is the message when a cluster has no runtime (default "context has no runtime").
	RuntimeErr string
	// Jobs is a long-lived jobs manager owned by the caller (hosted: outlives the engine so a drain keeps its
	// bound). Nil: the engine creates one bound to Ctx with JobsClock and stops it with the engine.
	Jobs      *jobs.Manager
	JobsClock jobs.Clock

	// Wire front end.
	Token          string
	Port           int
	AllowedOrigins []string
	DevOrigins     bool
	StaticDir      string
	PreAuthorized  bool
}

// Engine is a running engine.
type Engine struct {
	Manager  *cluster.Manager
	View     *view.Engine
	Ops      *ops.Service
	Streams  *streams.Manager
	Jobs     *jobs.Manager
	Drain    *drainer.Service
	Flux     *flux.Service
	Helm     *helm.Service
	Problems *problems.Hub
	Security *security.Service
	Hubble   *hubble.Service
	Handlers map[string]rpc.Handler
	Server   *wire.Server

	cancel   context.CancelFunc
	ownsJobs bool
	closed   atomic.Bool
}

func getter(connFn func(string) (kube.Conn, error)) func(ctx context.Context, cl, gvr, ns, name string) (*unstructured.Unstructured, error) {
	return func(ctx context.Context, cl, gvr, ns, name string) (*unstructured.Unstructured, error) {
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
	}
}

// New builds and starts an engine. Close it to stop everything.
func New(cfg Config) *Engine {
	if cfg.Log == nil {
		cfg.Log = slog.New(slog.DiscardHandler)
	}
	if cfg.RuntimeErr == "" {
		cfg.RuntimeErr = "context has no runtime"
	}
	log, src, set := cfg.Log, cfg.Source, cfg.Settings
	ctx, cancel := context.WithCancel(cfg.Ctx)
	e := &Engine{cancel: cancel, Handlers: map[string]rpc.Handler{}}
	connFn := func(id string) (kube.Conn, error) { return src.Backend(id) }

	e.View = view.NewEngine()
	e.Manager = cluster.NewManager(ctx, src, e.View, log)
	mgr := e.Manager
	mgr.SetSettings(set)
	e.Problems = problems.NewHub(problems.HubConfig{Settings: set.Get, Patch: set.Patch, Log: log, Fetch: getter(connFn)})
	// A settings store can outlive the engine (hosted); a closed engine's hub must stop reacting.
	set.OnChange(func(s protocol.Settings) {
		if !e.closed.Load() {
			e.Problems.SettingsChanged(s)
		}
	})
	mgr.SetProblems(e.Problems)
	e.Ops = ops.New(ops.Config{Conn: connFn, Links: mgr.Links, Resolve: mgr.Resolve, Now: cfg.Now})
	if cfg.Jobs != nil {
		e.Jobs = cfg.Jobs
	} else {
		e.Jobs, e.ownsJobs = jobs.New(ctx, cfg.JobsClock), true
	}
	e.Streams = streams.New(streams.Config{Router: e.Ops, Conn: connFn, Settings: set.Get, AllowPortForward: cfg.AllowPortForward, User: cfg.User, Log: log})
	e.Drain = drainer.New(drainer.Config{Router: e.Ops, Conn: connFn})
	selected := func() []string { return mgr.List().Selected }
	e.Flux = flux.New(flux.Config{Get: getter(connFn), Scan: mgr.ScanKind, Clusters: selected, Resolve: mgr.Resolve})
	helmCfg := helm.Config{
		Runtime: func(id string) (kube.Runtime, error) {
			c, err := connFn(id)
			if err != nil {
				return nil, err
			}
			rt, ok := kube.RuntimeOf(c)
			if !ok {
				return nil, errors.New(cfg.RuntimeErr)
			}
			return rt, nil
		},
		Route: func(ctx context.Context, cl, verb, gvr, sub, ns, name string) (string, bool, error) {
			r, err := e.Ops.Route(ctx, cl, verb, gvr, sub, ns, name)
			if err != nil {
				return "", false, err
			}
			return r.ContextID, r.ViaWrite, nil
		},
		Log: log,
	}
	if cfg.FakeHelm { // release records live in the fake clientset; no chart resources are touched
		helmCfg.NewActionConfig = func(ctx context.Context, id, ns string) (*action.Configuration, error) {
			rt, err := helmCfg.Runtime(id)
			if err != nil {
				return nil, err
			}
			return helm.NewFakeActionConfig(rt.Clientset(), ns)
		}
	}
	e.Helm = helm.New(helmCfg)
	go e.View.Run(ctx)

	e.Security = security.New(security.Config{Route: e.Ops.Route, Conn: connFn, Scan: mgr.ScanKind, Clusters: selected})
	for k, v := range e.Security.Handlers() {
		e.Handlers[k] = v
	}
	e.Hubble = hubble.New(hubble.Config{Conn: connFn, Router: e.Ops, Clusters: selected, Problems: e.Problems,
		ClientCount: func() int { return e.Server.ClientCount() }, Log: log})
	e.Hubble.RegisterStreams(e.Streams)
	for k, v := range e.Hubble.Handlers() {
		e.Handlers[k] = v
	}
	e.Server = wire.NewServer(wire.Options{Handlers: e.Handlers,
		Manager: mgr, Engine: e.View, Ops: e.Ops, Settings: set, Streams: e.Streams, Jobs: e.Jobs, Drain: e.Drain, Flux: e.Flux, Helm: e.Helm,
		Problems: e.Problems, Token: cfg.Token, Mode: src.Mode(), AgentVersion: version.Version, Port: cfg.Port,
		AllowedOrigins: cfg.AllowedOrigins, DevOrigins: cfg.DevOrigins, StaticDir: cfg.StaticDir, PreAuthorized: cfg.PreAuthorized,
		Log: log, Tick: cfg.Tick})
	go e.Hubble.StartProblems(ctx) // after Server is set: the monitor reads its client count
	return e
}

// Close stops the engine: problem monitor and persistence, Hubble links, streams, then the manager. A jobs
// manager supplied by the caller is left running; one the engine created stops with its context.
func (e *Engine) Close() {
	if !e.closed.CompareAndSwap(false, true) {
		return
	}
	e.cancel() // stops the Hubble problem monitor, view engine and (own) jobs
	e.Problems.Flush()
	e.Hubble.Close()
	e.Streams.Close()
	e.Manager.Close()
}
