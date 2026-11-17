// Command v8s is the v8s agent. Subcommand: agent.
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
	"strings"
	"syscall"
	"time"

	"github.com/zyno-io/v8s/agent/internal/cluster"
	"github.com/zyno-io/v8s/agent/internal/fake"
	"github.com/zyno-io/v8s/agent/internal/kube"
	"github.com/zyno-io/v8s/agent/internal/version"
	"github.com/zyno-io/v8s/agent/internal/view"
	"github.com/zyno-io/v8s/agent/internal/wire"
)

type stringList []string

func (s *stringList) String() string     { return strings.Join(*s, ",") }
func (s *stringList) Set(v string) error { *s = append(*s, v); return nil }

func main() {
	if len(os.Args) < 2 || os.Args[1] != "agent" {
		fmt.Fprintln(os.Stderr, "usage: v8s agent [flags]")
		os.Exit(2)
	}
	if err := runAgent(os.Args[2:]); err != nil {
		fmt.Fprintln(os.Stderr, "v8s:", err)
		os.Exit(1)
	}
}

func runAgent(args []string) error {
	fs := flag.NewFlagSet("agent", flag.ContinueOnError)
	listen := fs.String("listen", "127.0.0.1:0", "listen address")
	kubeconfig := fs.String("kubeconfig", "", "kubeconfig path (default: client-go loading rules, $KUBECONFIG merged)")
	fakeOpt := fs.String("fake", "", "serve synthetic clusters, e.g. clusters=3,pods=50000,churn=200")
	staticDir := fs.String("static-dir", "", "serve UI files from this directory at /")
	devToken := fs.String("dev-token", "", "fixed token (only with --fake)")
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
		src = cluster.KubeSource{Loader: kube.Loader{Path: *kubeconfig}}
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

	eng := view.NewEngine()
	mgr := cluster.NewManager(ctx, src, eng, log)
	if fakeSet {
		// Remembered selection in fake mode = all fake clusters; the UI still calls clusters.select.
		log.Info("fake mode", "clusters", len(src.Clusters()))
	}
	go eng.Run(ctx)

	srv := wire.NewServer(wire.Options{
		Manager: mgr, Engine: eng, Token: token, Mode: src.Mode(), AgentVersion: version.Version,
		Port: port, AllowedOrigins: origins, DevOrigins: fakeSet && *devToken != "",
		StaticDir: *staticDir, Log: log,
	})
	hs := &http.Server{Handler: srv.Handler(), ReadHeaderTimeout: 10 * time.Second}

	line, _ := json.Marshal(struct {
		Port  int    `json:"port"`
		Token string `json:"token"`
	}{port, token})
	fmt.Fprintf(os.Stdout, "%s\n", line)
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
