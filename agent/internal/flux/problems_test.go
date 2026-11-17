package flux

import (
	"fmt"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/zyno-io/v8s/agent/internal/ingest"
	"github.com/zyno-io/v8s/agent/internal/problems"
	"github.com/zyno-io/v8s/agent/internal/problems/ptest"
	appsv1 "k8s.io/api/apps/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

const (
	gvrKS  = "kustomize.toolkit.fluxcd.io/v1/kustomizations"
	gvrHR  = "helm.toolkit.fluxcd.io/v2/helmreleases"
	gvrGit = "source.toolkit.fluxcd.io/v1/gitrepositories"
	gvrDep = "apps/v1/deployments"
)

func newHarness(t *testing.T) *ptest.Harness {
	return ptest.New(t, gvrKS, gvrHR, gvrGit, "source.toolkit.fluxcd.io/v1/ocirepositories", "source.toolkit.fluxcd.io/v1/helmrepositories",
		"source.toolkit.fluxcd.io/v1/helmcharts", "source.toolkit.fluxcd.io/v1/buckets", gvrDep)
}

type fobj struct {
	kind, apiVersion, ns, name string
	ready, reason              string // Ready condition ("" = none)
	readyAgo                   time.Duration
	stalled, suspend           bool
	interval                   string
	deps                       []string // ns/name
	now                        time.Time
}

func (o fobj) json() string {
	ts := func(d time.Duration) string { return o.now.Add(-d).UTC().Format(time.RFC3339) }
	var conds []string
	if o.ready != "" {
		conds = append(conds, fmt.Sprintf(`{"type":"Ready","status":%q,"reason":%q,"message":"msg of %s","lastTransitionTime":%q}`, o.ready, o.reason, o.name, ts(o.readyAgo)))
	}
	if o.stalled {
		conds = append(conds, fmt.Sprintf(`{"type":"Stalled","status":"True","reason":"HealthCheckFailed","message":"stalled","lastTransitionTime":%q}`, ts(o.readyAgo)))
	}
	var deps []string
	for _, d := range o.deps {
		ns, n, _ := strings.Cut(d, "/")
		deps = append(deps, fmt.Sprintf(`{"kind":%q,"name":%q,"namespace":%q}`, o.kind, n, ns))
	}
	iv := o.interval
	if iv == "" {
		iv = "10m"
	}
	return fmt.Sprintf(`{"apiVersion":%q,"kind":%q,"metadata":{"name":%q,"namespace":%q,"uid":"u-%s-%s","resourceVersion":"1","creationTimestamp":%q},
		"spec":{"interval":%q,"suspend":%t,"dependsOn":[%s]},"status":{"conditions":[%s]}}`,
		o.apiVersion, o.kind, o.name, o.ns, o.ns, o.name, ts(24*time.Hour), iv, o.suspend, strings.Join(deps, ","), strings.Join(conds, ","))
}

func (o fobj) row(t *testing.T) *ingest.Row {
	t.Helper()
	_, r := project(t, o.json())
	return r
}

func ks(now time.Time, ns, name, ready, reason string, ago time.Duration) fobj {
	return fobj{kind: "Kustomization", apiVersion: "kustomize.toolkit.fluxcd.io/v1", ns: ns, name: name, ready: ready, reason: reason, readyAgo: ago, now: now}
}

func names(ps []problems.Problem) []string {
	var out []string
	for _, p := range ps {
		out = append(out, p.Subject.Kind+"/"+p.Subject.NS+"/"+p.Subject.Name)
	}
	sort.Strings(out)
	return out
}

func TestNotReadyGraceAndExclusions(t *testing.T) {
	for _, tc := range []struct {
		name  string
		mod   func(*fobj)
		ago   time.Duration
		class string
		want  int
	}{
		{"failing for 10 minutes", nil, 10 * time.Minute, "flux.not-ready", 1},
		{"stalled is flux.stalled, not not-ready", func(o *fobj) { o.stalled = true }, 10 * time.Minute, "flux.not-ready", 0},
		{"stalled raises flux.stalled", func(o *fobj) { o.stalled = true }, 10 * time.Minute, "flux.stalled", 1},
		{"suspended excluded", func(o *fobj) { o.suspend = true }, 10 * time.Minute, "flux.not-ready", 0},
		{"dependency wait is dependency-blocked", func(o *fobj) { o.reason = "DependencyNotReady" }, 10 * time.Minute, "flux.not-ready", 0},
		{"suspended class is off by default", func(o *fobj) { o.suspend = true }, 10 * time.Minute, "flux.suspended", 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			o := ks(h.Clk.Now(), "team", "apps", "False", "BuildFailed", tc.ago)
			if tc.mod != nil {
				tc.mod(&o)
			}
			h.Put(gvrKS, o.row(t))
			h.Pass()
			if n := len(h.ByClass(tc.class)); n != tc.want {
				t.Fatalf("got %d want %d: %+v", n, tc.want, h.E.Problems())
			}
		})
	}
}

func TestNotReadyRaisesAfterGrace(t *testing.T) {
	h := newHarness(t)
	h.Put(gvrKS, ks(h.Clk.Now(), "team", "apps", "False", "BuildFailed", time.Minute).row(t))
	h.Pass()
	if len(h.ByClass("flux.not-ready")) != 0 {
		t.Fatal("raised inside the 5 minute grace")
	}
	h.Advance(4*time.Minute + time.Second)
	ps := h.ByClass("flux.not-ready")
	if len(ps) != 1 || ps[0].Since != h.Clk.Now().Add(-4*time.Minute-time.Second-time.Minute).UnixMilli() {
		t.Fatalf("after grace: %+v", ps)
	}
	// it recovers: the problem is held for 30 s (hysteresis), then cleared
	h.Put(gvrKS, ks(h.Clk.Now(), "team", "apps", "True", "ReconciliationSucceeded", 0).row(t))
	h.Pass()
	if len(h.ByClass("flux.not-ready")) != 1 {
		t.Fatal("cleared without the hold")
	}
	h.Advance(31 * time.Second)
	if len(h.ByClass("flux.not-ready")) != 0 {
		t.Fatal("not cleared after the hold")
	}
}

func TestSourceFailedAndSuspended(t *testing.T) {
	h := newHarness(t)
	git := fobj{kind: "GitRepository", apiVersion: "source.toolkit.fluxcd.io/v1", ns: "flux-system", name: "platform", ready: "False", reason: "GitOperationFailed", readyAgo: time.Hour, now: h.Clk.Now()}
	h.Put(gvrGit, git.row(t))
	susp := ks(h.Clk.Now(), "team", "legacy", "True", "Succeeded", time.Hour)
	susp.suspend = true
	h.Put(gvrKS, susp.row(t))
	h.Pass()
	if got := names(h.ByClass("flux.source-failed")); fmt.Sprint(got) != "[GitRepository/flux-system/platform]" {
		t.Fatalf("source-failed: %v", got)
	}
	if len(h.ByClass("flux.not-ready")) != 0 {
		t.Fatal("a failed source must not also be flux.not-ready")
	}
	h.SetClass("flux.suspended", true)
	h.Pass()
	if got := names(h.ByClass("flux.suspended")); fmt.Sprint(got) != "[Kustomization/team/legacy]" {
		t.Fatalf("suspended: %v", got)
	}
}

func TestStale(t *testing.T) {
	h := newHarness(t)
	hung := ks(h.Clk.Now(), "team", "hung", "Unknown", "Progressing", 5*time.Minute)
	hung.interval = "10m"
	h.Put(gvrKS, hung.row(t))
	failing := ks(h.Clk.Now(), "team", "failing", "False", "BuildFailed", 5*time.Hour) // not stale: not-ready covers it
	h.Put(gvrKS, failing.row(t))
	h.Pass()
	if len(h.ByClass("flux.stale")) != 0 {
		t.Fatal("stale raised before 3x the interval")
	}
	h.Advance(26 * time.Minute) // 5 + 26 > 30
	if got := names(h.ByClass("flux.stale")); fmt.Sprint(got) != "[Kustomization/team/hung]" {
		t.Fatalf("stale: %v", got)
	}
}

func TestDependencyBlockedCollapsesToRoot(t *testing.T) {
	h := newHarness(t)
	now := h.Clk.Now()
	root := ks(now, "team", "root", "False", "BuildFailed", time.Hour)
	b := ks(now, "team", "b", "False", "DependencyNotReady", time.Hour)
	b.deps = []string{"team/root"}
	c := ks(now, "team", "c", "False", "DependencyNotReady", time.Hour)
	c.deps = []string{"team/b"}
	for _, o := range []fobj{root, b, c} {
		h.Put(gvrKS, o.row(t))
	}
	h.Pass()
	ps := h.ByClass("flux.dependency-blocked")
	if len(ps) != 1 || ps[0].Subject.Name != "root" || ps[0].Count != 2 || len(ps[0].Evidence) != 2 {
		t.Fatalf("collapse: %+v", ps)
	}
	if got := names(h.ByClass("flux.not-ready")); fmt.Sprint(got) != "[Kustomization/team/root]" {
		t.Fatalf("only the root is not-ready: %v", got)
	}
	// the root recovers: the whole chain is re-evaluated (transitive dependents) and clears after the hold
	h.Put(gvrKS, ks(now, "team", "root", "True", "ReconciliationSucceeded", 0).row(t))
	h.Pass()
	// b and c still say DependencyNotReady until Flux reconciles them: the blame moves to b, root is held 30 s
	if ps := h.ByClass("flux.dependency-blocked"); len(ps) != 2 {
		t.Fatalf("expected the held root problem and b's: %+v", ps)
	}
	h.Put(gvrKS, ks(now, "team", "b", "True", "ReconciliationSucceeded", 0).row(t))
	h.Put(gvrKS, ks(now, "team", "c", "True", "ReconciliationSucceeded", 0).row(t))
	h.Pass()
	h.Advance(31 * time.Second)
	if n := len(h.ByClass("flux.dependency-blocked")); n != 0 {
		t.Fatalf("still blocked: %+v", h.ByClass("flux.dependency-blocked"))
	}
}

func TestDependencyMissing(t *testing.T) {
	h := newHarness(t)
	b := ks(h.Clk.Now(), "team", "b", "False", "DependencyNotReady", time.Hour)
	b.deps = []string{"team/nope"}
	h.Put(gvrKS, b.row(t))
	h.Pass()
	ps := h.ByClass("flux.dependency-blocked")
	if len(ps) != 1 || ps[0].Subject.Name != "b" || !strings.Contains(ps[0].Message, "team/nope not found") {
		t.Fatalf("%+v", ps)
	}
}

func deployRow(ns, name string, replicas, available int32) *ingest.Row {
	m := ingest.Lookup(gvrDep)
	return m.ProjectObj(&appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: name, UID: types.UID("d-" + ns + name), ResourceVersion: "1"},
		Spec: appsv1.DeploymentSpec{Replicas: &replicas}, Status: appsv1.DeploymentStatus{AvailableReplicas: available, ReadyReplicas: available}})
}

func TestControllerDown(t *testing.T) {
	h := newHarness(t)
	h.Put(gvrDep, deployRow("flux-system", "source-controller", 1, 0))
	h.Put(gvrDep, deployRow("flux-system", "helm-controller", 1, 1))
	h.Put(gvrDep, deployRow("flux-system", "not-a-controller", 1, 0))
	h.Put(gvrDep, deployRow("elsewhere", "source-controller", 1, 0))
	h.Pass()
	if n := len(h.ByClass("flux.controller-down")); n != 0 {
		t.Fatal("raised inside the grace")
	}
	h.Advance(61 * time.Second)
	ps := h.ByClass("flux.controller-down")
	if len(ps) != 1 || ps[0].Subject.Name != "source-controller" || ps[0].Severity.String() != "critical" {
		t.Fatalf("%+v", ps)
	}
	h.Put(gvrDep, deployRow("flux-system", "source-controller", 1, 1))
	h.Pass()
	h.Advance(31 * time.Second)
	if len(h.ByClass("flux.controller-down")) != 0 {
		t.Fatal("not cleared")
	}
}
