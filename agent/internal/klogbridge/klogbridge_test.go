package klogbridge

import (
	"bytes"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"k8s.io/klog/v2"
)

func TestKlogGoesThroughSlogAtDebug(t *testing.T) {
	var buf bytes.Buffer
	Install(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	defer klog.ClearLogger()
	klog.ErrorS(errors.New("boom"), "Failed to watch", "type", "x")
	klog.Flush()
	out := buf.String()
	if !strings.Contains(out, "Failed to watch") || !strings.Contains(out, "boom") || !strings.Contains(out, "level=DEBUG") {
		t.Fatalf("klog error not routed at debug: %q", out)
	}
}

func TestKlogQuietAtInfoLevel(t *testing.T) {
	var buf bytes.Buffer
	Install(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo})))
	defer klog.ClearLogger()
	klog.ErrorS(errors.New("boom"), "Failed to watch")
	klog.Infof("hello")
	if buf.Len() != 0 {
		t.Fatalf("expected nothing at info level, got %q", buf.String())
	}
}
