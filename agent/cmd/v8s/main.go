// Command v8s is the v8s agent. Subcommands: agent, serve, version.
package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	osuser "os/user"
	"strings"
	"syscall"
	"time"

	"github.com/zyno-io/v8s/agent/internal/cluster"
	"github.com/zyno-io/v8s/agent/internal/drainer"
	"github.com/zyno-io/v8s/agent/internal/fake"
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
	"github.com/zyno-io/v8s/agent/internal/webui"
	"github.com/zyno-io/v8s/agent/internal/wire"
	"helm.sh/helm/v4/pkg/action"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

type stringList []string

func (s *stringList) String() string     { return strings.Join(*s, ",") }
func (s *stringList) Set(v string) error { *s = append(*s, v); return nil }

func main() {
	if err := dispatch(os.Args[1:]); err != nil {
		if err == errUsage {
			os.Exit(2)
		}
		fmt.Fprintln(os.Stderr, "v8s:", err)
		os.Exit(1)
	}
}

func runAgent(args []string, hooks agentHooks) error {
	fs := flag.NewFlagSet("agent", flag.ContinueOnError)
	listen := fs.String("listen", "127.0.0.1:0", "listen address")
	kubeconfig := fs.String("kubeconfig", "", "kubeconfig path (default: client-go loading rules, $KUBECONFIG merged)")
	fakeOpt := fs.String("fake", "", "serve synthetic clusters, e.g. clusters=3,pods=50000,churn=200")
	staticDir := fs.String("static-dir", "", "serve UI files from this directory at /")
	devToken := fs.String("dev-token", "", "fixed token (only with --fake)")
	settingsPath := fs.String("settings", "", "settings file (default: <user config dir>/v8s/settings.json; memory-only with --fake unless set)")
	lifeline := fs.Bool("stdin-lifeline", false, "exit when stdin reaches EOF")
	var origins stringList
	fs.Var(&origins, "allowed-origin", "allowed WebSocket Origin (repeatable)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	fakeSet := false
	fs.Visit(func(f *flag.Flag) { fakeSet = fakeSet || f.Name == "fake" })
	if *devToken != "" && !fakeSet {
		return errors.New("--dev-token is only allowed together with --fake")
	}

	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// Streaming list (WatchList) is on by default in client-go v0.37; make it explicit.
	if os.Getenv("KUBE_FEATURE_WatchListClient") == "" {
		_ = os.Setenv("KUBE_FEATURE_WatchListClient", "true")
	}

	var src cluster.Source
	if fakeSet {
		cfg, err := fake.ParseConfig(*fakeOpt)
		if err != nil {
			return err
		}
		src = fake.NewSource(cfg)
	} else {
		src = &cluster.KubeSource{Loader: kube.Loader{Path: *kubeconfig}}
	}

	token := *devToken
	if token == "" {
		b := make([]byte, 32)
		if _, err := rand.Read(b); err != nil {
			return err
		}
		token = hex.EncodeToString(b)
	}

	ln, err := net.Listen("tcp", *listen)
	if err != nil {
		return err
	}
	port := ln.Addr().(*net.TCPAddr).Port

	spath := *settingsPath
	if spath == "" && !fakeSet {
		if spath, err = settings.DefaultPath(); err != nil {
			return err
		}
	}
	set, err := settings.Open(spath)
	if err != nil {
		return err
	}
	eng := view.NewEngine()
	mgr := cluster.NewManager(ctx, src, eng, log)
	mgr.SetSettings(set)
	probHub := problems.NewHub(problems.HubConfig{
		Settings: set.Get, Patch: set.Patch, Log: log,
		Fetch: func(ctx context.Context, cluster, gvr, ns, name string) (*unstructured.Unstructured, error) {
			g, v, r, err := ingest.ParseGVR(gvr)
			if err != nil {
				return nil, err
			}
			c, err := src.Backend(cluster)
			if err != nil {
				return nil, err
			}
			ri := c.Dynamic().Resource(schema.GroupVersionResource{Group: g, Version: v, Resource: r})
			if ns != "" {
				return ri.Namespace(ns).Get(ctx, name, metav1.GetOptions{})
			}
			return ri.Get(ctx, name, metav1.GetOptions{})
		},
	})
	set.OnChange(probHub.SettingsChanged)
	mgr.SetProblems(probHub)
	opsSvc := ops.New(ops.Config{
		Conn:    func(id string) (kube.Conn, error) { return src.Backend(id) },
		Links:   mgr.Links,
		Resolve: mgr.Resolve,
	})
	connFn := func(id string) (kube.Conn, error) { return src.Backend(id) }
	jobsMgr := jobs.New(ctx, nil)
	user := "v8s"
	if u, uerr := osuser.Current(); uerr == nil && u.Username != "" {
		user = u.Username
	}
	streamMgr := streams.New(streams.Config{Router: opsSvc, Conn: connFn, Settings: set.Get, AllowPortForward: true, User: user, Log: log})
	defer streamMgr.Close()
	drainSvc := drainer.New(drainer.Config{Router: opsSvc, Conn: connFn})
	fluxSvc := flux.New(flux.Config{
		Get: func(ctx context.Context, cluster, gvr, ns, name string) (*unstructured.Unstructured, error) {
			g, v, r, err := ingest.ParseGVR(gvr)
			if err != nil {
				return nil, err
			}
			c, err := connFn(cluster)
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
				return nil, errors.New("context has no runtime")
			}
			return rt, nil
		},
		Route: func(ctx context.Context, cluster, verb, gvr, sub, ns, name string) (string, bool, error) {
			r, err := opsSvc.Route(ctx, cluster, verb, gvr, sub, ns, name)
			if err != nil {
				return "", false, err
			}
			return r.ContextID, r.ViaWrite, nil
		},
		Log: log,
	}
	if fakeSet { // release records live in the fake clientset; no chart resources are touched
		helmCfg.NewActionConfig = func(ctx context.Context, id, ns string) (*action.Configuration, error) {
			rt, err := helmCfg.Runtime(id)
			if err != nil {
				return nil, err
			}
			return helm.NewFakeActionConfig(rt.Clientset(), ns)
		}
	}
	helmSvc := helm.New(helmCfg)
	if fakeSet {
		// Remembered selection in fake mode = all fake clusters; the UI still calls clusters.select.
		log.Info("fake mode", "clusters", len(src.Clusters()))
	}
	go eng.Run(ctx)

	handlers := map[string]rpc.Handler{}
	var srv *wire.Server
	secSvc := security.New(security.Config{Route: opsSvc.Route, Conn: func(id string) (kube.Conn, error) { return src.Backend(id) }, Scan: mgr.ScanKind,
		Clusters: func() []string { return mgr.List().Selected }})
	for k, v := range secSvc.Handlers() {
		handlers[k] = v
	}
	hubbleSvc := hubble.New(hubble.Config{Conn: connFn, Router: opsSvc, Clusters: func() []string { return mgr.List().Selected }, Problems: probHub,
		ClientCount: func() int { return srv.ClientCount() }, Log: log})
	defer hubbleSvc.Close()
	hubbleSvc.RegisterStreams(streamMgr)
	for k, v := range hubbleSvc.Handlers() {
		handlers[k] = v
	}
	go hubbleSvc.StartProblems(ctx)

	srv = wire.NewServer(wire.Options{Handlers: handlers,
		Manager: mgr, Engine: eng, Ops: opsSvc, Settings: set, Streams: streamMgr, Jobs: jobsMgr, Drain: drainSvc, Flux: fluxSvc, Helm: helmSvc, Problems: probHub, Token: token, Mode: src.Mode(), AgentVersion: version.Version,
		Port: port, AllowedOrigins: origins, DevOrigins: fakeSet && *devToken != "",
		StaticDir: *staticDir, Log: log,
	})
	handler := srv.Handler()
	if *staticDir == "" && webui.Available() {
		handler = withEmbeddedUI(handler)
	}
	hs := &http.Server{Handler: handler, ReadHeaderTimeout: 10 * time.Second}

	if hooks.onReady != nil {
		hooks.onReady(port, token)
	} else {
		line, _ := json.Marshal(struct {
			Port  int    `json:"port"`
			Token string `json:"token"`
		}{port, token})
		fmt.Fprintf(os.Stdout, "%s\n", line)
	}
	log.Info("agent listening", "addr", ln.Addr().String(), "version", version.Version, "mode", src.Mode())

	if *lifeline {
		go func() {
			_, _ = io.Copy(io.Discard, os.Stdin)
			log.Info("stdin closed; exiting")
			stop()
		}()
	}

	errCh := make(chan error, 1)
	go func() { errCh <- hs.Serve(ln) }()
	select {
	case <-ctx.Done():
	case err := <-errCh:
		return err
	}
	shutdown, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_ = hs.Shutdown(shutdown)
	mgr.Close()
	return nil
}
