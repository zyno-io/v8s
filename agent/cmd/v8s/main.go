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
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/zyno-io/v8s/agent/internal/cluster"
	"github.com/zyno-io/v8s/agent/internal/engine"
	"github.com/zyno-io/v8s/agent/internal/fake"
	"github.com/zyno-io/v8s/agent/internal/klogbridge"
	"github.com/zyno-io/v8s/agent/internal/kube"
	"github.com/zyno-io/v8s/agent/internal/settings"
	"github.com/zyno-io/v8s/agent/internal/updatecheck"
	"github.com/zyno-io/v8s/agent/internal/version"
	"github.com/zyno-io/v8s/agent/internal/webui"
)

type stringList []string

func (s *stringList) String() string     { return strings.Join(*s, ",") }
func (s *stringList) Set(v string) error { *s = append(*s, v); return nil }

func main() {
	if err := dispatch(os.Args[1:]); err != nil {
		if err == errUsage {
			os.Exit(2)
		}
		if errors.Is(err, flag.ErrHelp) {
			os.Exit(0)
		}
		fmt.Fprintln(os.Stderr, "v8s:", err)
		os.Exit(1)
	}
}

// updateCheckEnvOff is the V8S_UPDATE_CHECK opt-out (0, false or off); the desktop app's only switch, since its
// agent inherits the environment.
func updateCheckEnvOff() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("V8S_UPDATE_CHECK"))) {
	case "0", "false", "off":
		return true
	}
	return false
}

func runAgent(args []string, hooks agentHooks) error {
	fs := flag.NewFlagSet("agent", flag.ContinueOnError)
	listen := fs.String("listen", "127.0.0.1:0", "listen address")
	kubeconfig := fs.String("kubeconfig", "", "kubeconfig path (default: client-go loading rules, $KUBECONFIG merged)")
	fakeOpt := fs.String("fake", "", "serve synthetic clusters, e.g. clusters=3,pods=50000,churn=200")
	staticDir := fs.String("static-dir", "", "serve UI files from this directory at /")
	devToken := fs.String("dev-token", "", "fixed token (only with --fake)")
	settingsPath := fs.String("settings", "", "settings file (default: <user config dir>/v8s/settings.json; memory-only with --fake unless set)")
	noUpdateCheck := fs.Bool("no-update-check", false, "do not check v8s.io for a newer release (also: V8S_UPDATE_CHECK=off)")
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
	klogbridge.Install(log)
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
	user := "v8s"
	if u, uerr := osuser.Current(); uerr == nil && u.Username != "" {
		user = u.Username
	}
	if fakeSet {
		// Remembered selection in fake mode = all fake clusters; the UI still calls clusters.select.
		log.Info("fake mode", "clusters", len(src.Clusters()))
	}
	// Never in fake mode (e2e/perf/screenshots must not reach the network). The cache sits beside settings.json so
	// a desktop launch within 15 minutes of the last check does not call v8s.io again.
	var chk *updatecheck.Checker
	updateCheck := "off"
	switch {
	case fakeSet || *noUpdateCheck || updateCheckEnvOff():
	case !updatecheck.Checkable(version.Version):
		updateCheck = "dev"
	default:
		chk = updatecheck.New(updatecheck.Options{Current: version.Version, Log: log,
			CachePath: filepath.Join(filepath.Dir(spath), "update-check.json")})
		go chk.Run(ctx)
		updateCheck = "on"
	}
	e := engine.New(engine.Config{Ctx: ctx, Source: src, Settings: set, Log: log, User: user, AllowPortForward: true, FakeHelm: fakeSet, Updates: chk,
		Token: token, Port: port, AllowedOrigins: origins, DevOrigins: fakeSet && *devToken != "", StaticDir: *staticDir})
	defer e.Close()
	srv := e.Server
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
	log.Info("agent listening", "addr", ln.Addr().String(), "version", version.Version, "mode", src.Mode(), "updateCheck", updateCheck)

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
	return nil
}
