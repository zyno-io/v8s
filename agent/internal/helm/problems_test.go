package helm

import (
	"testing"
	"time"

	"github.com/zyno-io/v8s/agent/internal/ingest"
	"github.com/zyno-io/v8s/agent/internal/problems/ptest"
	"github.com/zyno-io/v8s/agent/pkg/protocol"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

func relRow(ns, name, status string, rev int, updated time.Time, managedBy string) *ingest.Row {
	return project(&Release{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns, UID: types.UID(ReleaseUID(ns, name)), ResourceVersion: "1"},
		Revision: rev, Status: status, UpdatedMs: updated.UnixMilli(), ManagedBy: managedBy})
}

func TestHelmFailed(t *testing.T) {
	h := ptest.New(t, protocol.HelmGVR)
	now := h.Clk.Now()
	h.Put(protocol.HelmGVR, relRow("default", "broken-release", "failed", 3, now.Add(-3*time.Hour), ""))
	h.Put(protocol.HelmGVR, relRow("default", "podinfo", "deployed", 3, now.Add(-3*time.Hour), ""))
	h.Put(protocol.HelmGVR, relRow("default", "old", "superseded", 1, now.Add(-3*time.Hour), ""))
	h.Pass()
	ps := h.ByClass("helm.failed")
	if len(ps) != 1 || ps[0].Subject.Name != "broken-release" || ps[0].Since != now.Add(-3*time.Hour).UnixMilli() || ps[0].Subject.Kind != "Release" {
		t.Fatalf("%+v", ps)
	}
	// the release is fixed: held 30 s, then cleared
	h.Put(protocol.HelmGVR, relRow("default", "broken-release", "deployed", 4, now, ""))
	h.Pass()
	if len(h.ByClass("helm.failed")) != 1 {
		t.Fatal("cleared without the hold")
	}
	h.Advance(31 * time.Second)
	if len(h.ByClass("helm.failed")) != 0 {
		t.Fatal("not cleared")
	}
}

func TestHelmStuckPending(t *testing.T) {
	for _, st := range []string{"pending-install", "pending-upgrade", "pending-rollback"} {
		t.Run(st, func(t *testing.T) {
			h := ptest.New(t, protocol.HelmGVR)
			h.Put(protocol.HelmGVR, relRow("team-3", "stuck", st, 3, h.Clk.Now().Add(-10*time.Minute), ""))
			h.Pass()
			if len(h.ByClass("helm.stuck-pending")) != 0 {
				t.Fatal("raised before 15 minutes")
			}
			h.Advance(6 * time.Minute) // the deadline heap wakes it: 16 minutes since the revision
			ps := h.ByClass("helm.stuck-pending")
			if len(ps) != 1 || ps[0].Subject.Name != "stuck" {
				t.Fatalf("%+v", ps)
			}
		})
	}
	h := ptest.New(t, protocol.HelmGVR)
	h.Put(protocol.HelmGVR, relRow("team-3", "fresh", "pending-upgrade", 3, h.Clk.Now().Add(-time.Minute), ""))
	h.Put(protocol.HelmGVR, relRow("team-3", "ok", "deployed", 3, h.Clk.Now().Add(-time.Hour), ""))
	h.Pass()
	h.Advance(5 * time.Minute)
	if n := len(h.ByClass("helm.stuck-pending")); n != 0 {
		t.Fatalf("%d", n)
	}
}
