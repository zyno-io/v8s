package helm

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/base64"
	"strings"
	"testing"
	"time"

	"github.com/zyno-io/v8s/agent/internal/jobs"
	"github.com/zyno-io/v8s/agent/pkg/protocol"
)

func gz(t *testing.T, payload []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	w := gzip.NewWriter(&buf)
	_, _ = w.Write(payload)
	_ = w.Close()
	return []byte(base64.StdEncoding.EncodeToString(buf.Bytes()))
}

func TestDecodeReleaseRejectsGzipBomb(t *testing.T) {
	// valid JSON prefix padded with whitespace beyond the cap: compresses to a few KiB
	big := append([]byte(`{"name":"x"}`), bytes.Repeat([]byte(" "), maxReleaseBytes+10)...)
	if _, err := DecodeRelease(gz(t, big)); err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("want overflow error, got %v", err)
	}
	ok := append([]byte(`{"name":"x"}`), bytes.Repeat([]byte(" "), 1024)...)
	if r, err := DecodeRelease(gz(t, ok)); err != nil || r.Name != "x" {
		t.Fatalf("%v %+v", err, r)
	}
}

func TestBudgetTimeoutClampsToJobDeadline(t *testing.T) {
	if got := budgetTimeout(context.Background(), 600); got != 600*time.Second {
		t.Fatal(got)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if got := budgetTimeout(ctx, 600); got > 20*time.Second || got < 15*time.Second {
		t.Fatalf("not clamped: %v", got)
	}
	if got := budgetTimeout(ctx, 5); got != 5*time.Second {
		t.Fatalf("clamped below request: %v", got)
	}
}

func TestUncancellableHelmCallKeepsJobRunningUntilItReturns(t *testing.T) {
	m := jobs.New(context.Background(), nil)
	release := make(chan struct{})
	started := make(chan struct{})
	id := m.Start("", "helm", "c1", "s", func(ctx context.Context, jc *jobs.Ctx) error {
		return runUncancellable(ctx, jc, "ns/x", func() error { close(started); <-release; return nil })
	})
	<-started
	if err := m.Cancel(id); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		f, _ := m.Get(id)
		var cancelling bool
		for _, p := range f.Progress {
			cancelling = cancelling || p.State == "cancelling"
		}
		if cancelling {
			if f.State != protocol.JobRunning {
				t.Fatalf("job ended while Helm still runs: %s", f.State)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("no cancelling progress")
		}
		time.Sleep(5 * time.Millisecond)
	}
	close(release)
	m.Wait(id)
}
