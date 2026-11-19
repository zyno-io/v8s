package main

import (
	"context"
	"errors"
	"flag"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/zyno-io/v8s/agent/internal/hosted"
	"github.com/zyno-io/v8s/agent/internal/hosted/config"
	"github.com/zyno-io/v8s/agent/internal/klogbridge"
	"github.com/zyno-io/v8s/agent/internal/updatecheck"
	"github.com/zyno-io/v8s/agent/internal/version"
	"github.com/zyno-io/v8s/agent/internal/webui"
)

// runServer is `v8s server`: the self-hosted multi-user mode (OIDC login, per-user engines).
func runServer(args []string) error {
	fs := flag.NewFlagSet("server", flag.ContinueOnError)
	cfgPath := fs.String("config", "/etc/v8s/config.yaml", "YAML configuration file")
	if err := fs.Parse(args); err != nil {
		return err
	}
	cfg, err := config.Load(*cfgPath)
	if err != nil {
		return err
	}
	log := slog.New(slog.NewJSONHandler(os.Stderr, nil))
	klogbridge.Install(log)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// Streaming list (WatchList) is on by default in client-go v0.37; make it explicit.
	if os.Getenv("KUBE_FEATURE_WatchListClient") == "" {
		_ = os.Setenv("KUBE_FEATURE_WatchListClient", "true")
	}

	opt := hosted.Options{Config: cfg, Log: log}
	// One checker for the whole server; memory-only cache (the data dir holds per-user settings only).
	updateCheck := "off"
	switch {
	case !cfg.UpdateCheckOn() || updateCheckEnvOff():
	case !updatecheck.Checkable(version.Version):
		updateCheck = "dev"
	default:
		opt.Updates = updatecheck.New(updatecheck.Options{Current: version.Version, Log: log})
		go opt.Updates.Run(ctx)
		updateCheck = "on"
	}
	if webui.Available() {
		opt.UI = webui.Handler()
	}
	srv, err := hosted.New(ctx, opt)
	if err != nil {
		return err
	}
	ln, err := net.Listen("tcp", cfg.Listen)
	if err != nil {
		return err
	}
	go srv.Run(ctx)
	hs := &http.Server{Handler: srv.Handler(), ReadHeaderTimeout: 10 * time.Second}
	log.Info("v8s server listening", "addr", ln.Addr().String(), "publicUrl", cfg.PublicURL, "version", version.Version, "clusters", len(cfg.Clusters), "updateCheck", updateCheck)

	errCh := make(chan error, 1)
	go func() { errCh <- hs.Serve(ln) }()
	select {
	case <-ctx.Done():
	case err := <-errCh:
		if !errors.Is(err, http.ErrServerClosed) {
			return err
		}
	}
	shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = hs.Shutdown(shutdown)
	srv.Close()
	return nil
}
