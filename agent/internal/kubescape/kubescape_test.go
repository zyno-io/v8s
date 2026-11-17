package kubescape

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/zyno-io/v8s/agent/internal/ingest"
	"github.com/zyno-io/v8s/agent/internal/problems/ptest"
	"github.com/zyno-io/v8s/agent/internal/security/sectest"
	"github.com/zyno-io/v8s/agent/pkg/protocol"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/watch"
)

const (
	vulnGVR = Group + "/" + Version + "/" + ResVulnSummary
	cfgGVR  = Group + "/" + Version + "/" + ResConfigSummary
)

func summaryJSON(name, ns, uid, rv, kind, wl, image string, crit, high int) []byte {
	return []byte(fmt.Sprintf(`{"kind":"VulnerabilityManifestSummary","metadata":{"name":%q,"namespace":%q,"uid":%q,"resourceVersion":%q,"creationTimestamp":"2026-11-17T08:00:00Z",
	"labels":{"kubescape.io/workload-kind":%q,"kubescape.io/workload-name":%q,"kubescape.io/workload-namespace":%q,"kubescape.io/workload-container-name":"app","kubescape.io/image-tag":%q}},
	"spec":{"severities":{"critical":{"all":%d,"fixable":1},"high":{"all":%d,"fixable":0},"medium":3,"low":2,"negligible":{"all":5},"unknown":{"all":1}}}}`,
		name, ns, uid, rv, kind, wl, ns, image, crit, high))
}

func TestDecodeSummaryAndRow(t *testing.T) {
	o, err := Decode(summaryJSON("deployment-web", "shop", "u1", "5", "Deployment", "web", "docker.io/library/nginx:1.25", 4, 31))
	if err != nil {
		t.Fatal(err)
	}
	s := o.(*Summary)
	if s.Critical != 4 || s.High != 31 || s.Medium != 3 || s.Low != 2 || s.Unknown != 6 {
		t.Fatalf("severities in both number and {all,fixable} forms: %+v", s)
	}
	r := project(s, true)
	m := ingest.Lookup(vulnGVR)
	if m.StrOf(r, ColRepository) != "docker.io/library/nginx" || m.StrOf(r, ColTag) != "1.25" || m.StrOf(r, ColWorkload) != "web" || m.StrOf(r, ColScanner) != "Kubescape" || r.Tone() != ingest.ToneDanger {
		t.Fatalf("row: %q %q %q", m.StrOf(r, ColRepository), m.StrOf(r, ColTag), m.StrOf(r, ColWorkload))
	}
}

func TestSplitImage(t *testing.T) {
	for in, want := range map[string][2]string{
		"nginx:1.25": {"nginx", "1.25"}, "reg.io:5000/team/app:v2": {"reg.io:5000/team/app", "v2"}, "reg.io:5000/app": {"reg.io:5000/app", ""},
		"app@sha256:abc": {"app", "sha256:abc"}, "": {"", ""},
	} {
		if r, tag := SplitImage(in); r != want[0] || tag != want[1] {
			t.Errorf("%q: %q %q", in, r, tag)
		}
	}
}

func TestVulnClassesWithSourceKubescape(t *testing.T) {
	h := ptest.New(t, vulnGVR, cfgGVR)
	for i, wl := range []string{"web", "api"} {
		o, _ := Decode(summaryJSON("deployment-"+wl, "shop", fmt.Sprintf("u%d", i), "1", "Deployment", wl, "nginx:1.25", 4, 31))
		h.Put(vulnGVR, project(o.(*Summary), true))
	}
	// a config scan summary
	cs := &Summary{Kind_: "Deployment", Workload: "web", Critical: 1, High: 2}
	cs.Name, cs.Namespace, cs.UID, cs.ResourceVersion = "deployment-web", "shop", "c1", "1"
	h.Put(cfgGVR, project(cs, false))
	h.Pass()
	ps := h.ByClass("vuln.critical")
	if len(ps) != 1 || ps[0].Source != "kubescape" || ps[0].Title != "nginx:1.25 · 4 critical, 31 high · 2 workloads" {
		t.Fatalf("%+v", ps)
	}
	h.Set.ProblemPrefs = map[string]any{"classes": map[string]any{"misconfig.critical": true}}
	h.Hub.SettingsChanged(h.Set)
	h.Pass()
	if mc := h.ByClass("misconfig.critical"); len(mc) != 1 || mc[0].Source != "kubescape" || mc[0].Subject.Name != "web" {
		t.Fatalf("misconfig: %+v", mc)
	}
}

// ---- polling fallback ----

type fakeInner struct {
	items    []runtime.Object
	pages    int
	watchErr error
	lists    int
}

func (f *fakeInner) List(ctx context.Context, opts metav1.ListOptions) (runtime.Object, error) {
	f.lists++
	if f.pages > 1 && opts.Continue == "" { // two pages
		return &ingest.PartialList{ListMeta: metav1.ListMeta{ResourceVersion: "1", Continue: "p2"}, Items: f.items[:1]}, nil
	}
	if f.pages > 1 {
		return &ingest.PartialList{ListMeta: metav1.ListMeta{ResourceVersion: "1"}, Items: f.items[1:]}, nil
	}
	return &ingest.PartialList{ListMeta: metav1.ListMeta{ResourceVersion: "1"}, Items: f.items}, nil
}
func (f *fakeInner) Watch(ctx context.Context, opts metav1.ListOptions) (watch.Interface, error) {
	return nil, f.watchErr
}

func sum(uid, rv string) runtime.Object {
	o, _ := Decode(summaryJSON("n-"+uid, "ns", uid, rv, "Deployment", uid, "img:1", 1, 1))
	return o
}

func TestPollingFallbackWhenWatchUnsupported(t *testing.T) {
	inner := &fakeInner{items: []runtime.Object{sum("a", "1"), sum("b", "1"), sum("c", "1")}, pages: 2,
		watchErr: apierrors.NewMethodNotSupported(schema.GroupResource{Group: Group, Resource: ResVulnSummary}, "watch")}
	tick := make(chan time.Time)
	api := NewPollingAPI(inner, 5*time.Minute)
	var mu sync.Mutex
	var asked []time.Duration
	api.After = func(d time.Duration) <-chan time.Time { mu.Lock(); asked = append(asked, d); mu.Unlock(); return tick }
	ctx := context.Background()
	for opts := (metav1.ListOptions{}); ; { // the reflector's own paged list seeds the known state
		obj, err := api.List(ctx, opts)
		if err != nil {
			t.Fatal(err)
		}
		if c := obj.(*ingest.PartialList).Continue; c != "" {
			opts.Continue = c
			continue
		}
		break
	}
	w, err := api.Watch(ctx, metav1.ListOptions{})
	if err != nil {
		t.Fatalf("an unsupported watch must fall back, got %v", err)
	}
	defer w.Stop()
	// next poll: b modified, c deleted, d added
	inner.items = []runtime.Object{sum("a", "1"), sum("b", "2"), sum("d", "1")}
	tick <- time.Now()
	got := map[watch.EventType]string{}
	for i := 0; i < 3; i++ {
		select {
		case ev := <-w.ResultChan():
			m, _ := metaOf(ev.Object)
			got[ev.Type] = m
		case <-time.After(2 * time.Second):
			t.Fatalf("missing event %d: %v", i, got)
		}
	}
	if got[watch.Modified] != "b" || got[watch.Deleted] != "c" || got[watch.Added] != "d" {
		t.Fatalf("diff events: %v", got)
	}
	mu.Lock()
	first := time.Duration(0)
	if len(asked) > 0 {
		first = asked[0]
	}
	mu.Unlock()
	if first != 5*time.Minute {
		t.Fatalf("poll interval: %v", first)
	}
	// nothing changed: no events
	tick <- time.Now()
	select {
	case ev := <-w.ResultChan():
		t.Fatalf("unexpected event %v", ev)
	case <-time.After(100 * time.Millisecond):
	}
}

func metaOf(o runtime.Object) (string, bool) {
	if s, ok := o.(*Summary); ok {
		return string(s.UID), true
	}
	return "", false
}

func TestNativeWatchIsUsedWhenSupported(t *testing.T) {
	inner := &fakeInner{watchErr: fmt.Errorf("boom")}
	api := NewPollingAPI(inner, time.Minute)
	if _, err := api.Watch(context.Background(), metav1.ListOptions{}); err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("a real watch error must surface, not trigger polling: %v", err)
	}
	for _, c := range []int32{404, 405, 406, 501} {
		if !Unsupported(&apierrors.StatusError{ErrStatus: metav1.Status{Code: c}}) {
			t.Errorf("%d must be unsupported", c)
		}
	}
	if Unsupported(&apierrors.StatusError{ErrStatus: metav1.Status{Code: 500}}) || Unsupported(nil) {
		t.Error("500 is a failure, not a missing feature")
	}
}

// ---- detail and actions ----

func TestBuildManifestAndScanDetail(t *testing.T) {
	m := &unstructured.Unstructured{Object: map[string]any{"kind": "VulnerabilityManifest", "metadata": map[string]any{"name": "m", "resourceVersion": "2"},
		"spec": map[string]any{"payload": map[string]any{"matches": []any{
			map[string]any{"vulnerability": map[string]any{"id": "CVE-2", "severity": "Medium", "fix": map[string]any{"versions": []any{"1.2"}}}, "artifact": map[string]any{"name": "libz", "version": "1.1"}},
			map[string]any{"vulnerability": map[string]any{"id": "CVE-1", "severity": "Critical", "dataSource": "https://nvd/CVE-1"}, "artifact": map[string]any{"name": "libc", "version": "2.0"}}}}}}}
	d, err := build(m)
	if err != nil || d.Kind != "vulnerabilities" || d.Vulns[0].ID != "CVE-1" || d.Vulns[1].Fixed != "1.2" || d.Vulns[0].Link != "https://nvd/CVE-1" || d.Summary["critical"] != 1 {
		t.Fatalf("%+v %v", d, err)
	}
	sc := &unstructured.Unstructured{Object: map[string]any{"kind": "WorkloadConfigurationScan", "metadata": map[string]any{"name": "s"},
		"spec": map[string]any{"controls": map[string]any{
			"C-0017": map[string]any{"name": "Immutable container filesystem", "severity": "Medium", "status": map[string]any{"status": "failed"}},
			"C-0013": map[string]any{"name": "Non-root containers", "severity": map[string]any{"severity": "High"}, "status": map[string]any{"status": "failed"}},
			"C-0001": map[string]any{"name": "ok", "severity": "Low", "status": map[string]any{"status": "passed"}}}}}}
	c, _ := build(sc)
	if c.Kind != "checks" || c.Total != 2 || c.Checks[0].ID != "C-0013" || c.Checks[0].Source != "kubescape" {
		t.Fatalf("%+v", c)
	}
	doc, _ := build(&unstructured.Unstructured{Object: map[string]any{"kind": "ApplicationProfile", "metadata": map[string]any{"name": "ap"},
		"spec": map[string]any{"containers": []any{map[string]any{"name": "app", "execs": []any{map[string]any{"path": "/bin/sh"}}}}}}})
	if doc.Kind != "document" || !strings.Contains(doc.Doc, "/bin/sh") {
		t.Fatalf("%+v", doc)
	}
}

func TestScanNowCreatesJobFromCronJob(t *testing.T) {
	cj := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "batch/v1", "kind": "CronJob", "metadata": map[string]any{"name": "kubescape-scheduler", "namespace": "kubescape", "uid": "cj1"},
		"spec": map[string]any{"schedule": "54 0 * * *", "jobTemplate": map[string]any{"spec": map[string]any{"template": map[string]any{"spec": map[string]any{
			"restartPolicy": "Never", "containers": []any{map[string]any{"name": "scan", "image": "curlimages/curl"}}}}}}}}}
	other := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "batch/v1", "kind": "CronJob", "metadata": map[string]any{"name": "backup", "namespace": "default"}, "spec": map[string]any{}}}
	cronGVR := schema.GroupVersionResource{Group: "batch", Version: "v1", Resource: "cronjobs"}
	jobGVR := schema.GroupVersionResource{Group: "batch", Version: "v1", Resource: "jobs"}
	env := sectest.New(map[schema.GroupVersionResource]string{cronGVR: "CronJobList", jobGVR: "JobList"}, cj, other)
	res, err := ScanNow(context.Background(), &env.Cfg, protocol.KubescapeScanNowParams{Cluster: "c"})
	if err != nil || len(res.Jobs) != 1 || !strings.HasPrefix(res.Jobs[0], "kubescape/kubescape-scheduler-manual-") {
		t.Fatalf("%v %v", res, err)
	}
	list, _ := env.Dyn.Resource(jobGVR).Namespace("kubescape").List(context.Background(), metav1.ListOptions{})
	if len(list.Items) != 1 || list.Items[0].GetAnnotations()["cronjob.kubernetes.io/instantiate"] != "manual" {
		t.Fatalf("jobs: %+v", list.Items)
	}
	// no scan CronJob: not_found
	env2 := sectest.New(map[schema.GroupVersionResource]string{cronGVR: "CronJobList", jobGVR: "JobList"}, other)
	if _, err := ScanNow(context.Background(), &env2.Cfg, protocol.KubescapeScanNowParams{Cluster: "c"}); err == nil {
		t.Fatal("expected not_found")
	}
}
