// Package klogbridge routes client-go's klog output into the agent's slog logger.
package klogbridge

import (
	"context"
	"io"
	"log/slog"

	"github.com/go-logr/logr"
	"k8s.io/klog/v2"
)

// debugHandler lowers every record to Debug: client-go's reflector complaints (a forbidden LIST, a dropped
// watch) are already surfaced by v8s itself (watch errors, cluster states, problems "unavailable"), so they
// only matter when debugging.
type debugHandler struct{ slog.Handler }

func (h debugHandler) Enabled(ctx context.Context, l slog.Level) bool {
	return h.Handler.Enabled(ctx, slog.LevelDebug)
}

func (h debugHandler) Handle(ctx context.Context, r slog.Record) error {
	if !h.Handler.Enabled(ctx, slog.LevelDebug) {
		return nil // the logr sink hands errors to Handle without asking Enabled
	}
	r.Level = slog.LevelDebug
	return h.Handler.Handle(ctx, r)
}

func (h debugHandler) WithAttrs(a []slog.Attr) slog.Handler {
	return debugHandler{h.Handler.WithAttrs(a)}
}

func (h debugHandler) WithGroup(n string) slog.Handler { return debugHandler{h.Handler.WithGroup(n)} }

// Install makes klog write through log at Debug level and nothing to stderr. Call once per process.
func Install(log *slog.Logger) {
	klog.LogToStderr(false)
	klog.SetOutput(io.Discard)
	klog.SetLogger(logr.FromSlogHandler(debugHandler{log.Handler()}).WithName("klog"))
}
