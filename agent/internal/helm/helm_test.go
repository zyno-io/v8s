package helm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/zyno-io/v8s/agent/internal/ingest"
	"github.com/zyno-io/v8s/agent/internal/jobs"
	"github.com/zyno-io/v8s/agent/internal/kube"
	"github.com/zyno-io/v8s/agent/pkg/protocol"
	"helm.sh/helm/v4/pkg/action"
	"helm.sh/helm/v4/pkg/release/common"
	releasev1 "helm.sh/helm/v4/pkg/release/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/rest"
)

// ---- helpers ----------------------------------------------------------------------------------

func pom(ns, name string, version int, status string, flux bool) metav1.PartialObjectMetadata {
	l := map[string]string{LabelName: name, LabelOwner: "helm", LabelStatus: status, LabelVersion: strconv.Itoa(version),
		LabelCreatedAt: strconv.Itoa(1700000000 + version), LabelModified: strconv.Itoa(1700000000 + version)}
	if flux {
		l[FluxNameLabel], l[FluxNSLabel] = name, "flux-system"
	}
	return metav1.PartialObjectMetadata{
		ObjectMeta: metav1.ObjectMeta{Name: secretName(name, version), Namespace: ns, Labels: l,
			UID: types.UID(fmt.Sprintf("uid-%s-%s-%d", ns, name, version)), ResourceVersion: strconv.Itoa(100 + version)},
	}
}

// fakeMeta is a MetaSource over fixed metadata and a controllable watch; it can only ever return
// metadata, which is what the list path must never go beyond.
type fakeMeta struct {
	items []metav1.PartialObjectMetadata
	w     *watch.RaceFreeFakeWatcher
	lists atomic.Int32
}

func (f *fakeMeta) List(_ context.Context, _ metav1.ListOptions) (*metav1.PartialObjectMetadataList, error) {
	f.lists.Add(1)
	return &metav1.PartialObjectMetadataList{ListMeta: metav1.ListMeta{ResourceVersion: "500"}, Items: f.items}, nil
}
func (f *fakeMeta) Watch(_ context.Context, _ metav1.ListOptions) (watch.Interface, error) {
	f.w = watch.NewRaceFreeFake()
	return f.w, nil
}

func rows(t *testing.T, l runtime.Object) map[string]*ingest.Row {
	out := map[string]*ingest.Row{}
	Module.Each(l, func(r *ingest.Row) { out[r.Name()] = r })
	return out
}

type fakeRT struct {
	kube.Runtime
	cs kubernetes.Interface
}

func (r fakeRT) Clientset() kubernetes.Interface { return r.cs }
func (r fakeRT) RESTConfig() *rest.Config        { return nil }

func newService(t *testing.T, cs kubernetes.Interface) *Service {
	return New(Config{
		Runtime: func(string) (kube.Runtime, error) { return fakeRT{cs: cs}, nil },
		Route: func(_ context.Context, cluster, verb, gvr, sub, ns, name string) (string, bool, error) {
			return "write-" + cluster, true, nil
		},
		NewActionConfig: func(_ context.Context, _ string, ns string) (*action.Configuration, error) {
			return NewFakeActionConfig(cs, ns)
		},
	})
}

func demoClient(t *testing.T) *fake.Clientset {
	cs := fake.NewSimpleClientset()
	for _, s := range DemoSecrets("c1", time.Now()) {
		if _, err := cs.CoreV1().Secrets(s.Namespace).Create(context.Background(), s, metav1.CreateOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	return cs
}

func runJob(t *testing.T, run jobs.Run) protocol.JobFrame {
	t.Helper()
	m := jobs.New(context.Background(), nil)
	id := m.Start("", "helm", "c1", "s", run)
	m.Wait(id)
	f, _ := m.Get(id)
	return f
}

// ---- list projection --------------------------------------------------------------------------

func TestListProjectionLabelsOnly(t *testing.T) {
	src := &fakeMeta{}
	for v := 1; v <= 3; v++ {
		st := "superseded"
		if v == 3 {
			st = "deployed"
		}
		src.items = append(src.items, pom("a", "alpha", v, st, false))
	}
	src.items = append(src.items, pom("b", "beta", 1, "superseded", true), pom("b", "beta", 2, "failed", true), pom("b", "beta", 3, "pending-upgrade", true))
	src.items = append(src.items, pom("c", "gamma", 1, "uninstalled", false))
	src.items = append(src.items, metav1.PartialObjectMetadata{ObjectMeta: metav1.ObjectMeta{Name: "other", Namespace: "a", Labels: map[string]string{"owner": "someone"}}})
	api := NewReleaseAPI(src)
	l, err := api.List(context.Background(), metav1.ListOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if rv := l.(*ReleaseList).ResourceVersion; rv != "500" {
		t.Fatalf("list rv %q", rv)
	}
	r := rows(t, l)
	if len(r) != 3 {
		t.Fatalf("rows: %d", len(r))
	}
	a, b, g := r["alpha"], r["beta"], r["gamma"]
	if a.UID() != "helm:a/alpha" || a.Cell(0).N != 3 || a.StrCell(1) != "deployed" || a.Tone() != ingest.ToneSuccess || a.StrCell(3) != "" {
		t.Fatalf("alpha: %+v", a)
	}
	if a.Cell(2).N != 1700000003*1000 {
		t.Fatalf("updated %d", a.Cell(2).N)
	}
	if b.Cell(0).N != 3 || b.Tone() != ingest.ToneWarning || b.StrCell(3) != "flux:flux-system/beta" {
		t.Fatalf("beta: tone %d managed %q", b.Tone(), b.StrCell(3))
	}
	if g.Tone() != ingest.ToneNeutral || g.StrCell(1) != "uninstalled" {
		t.Fatal("gamma")
	}
	for st, want := range map[string]uint8{"failed": ingest.ToneDanger, "superseded": ingest.ToneInfo, "unknown": ingest.ToneWarning,
		"pending-install": ingest.ToneWarning, "pending-rollback": ingest.ToneWarning, "uninstalling": ingest.ToneNeutral} {
		if Tone(st) != want {
			t.Errorf("tone %s", st)
		}
	}
}

func TestModuleRegistered(t *testing.T) {
	m := ingest.Lookup(protocol.HelmGVR)
	if m == nil || !m.Virtual || !m.Curated || !m.NoWatchList || m.Tier0 || m.Kind != "Release" || m.NCells != 4 {
		t.Fatalf("module: %+v", m)
	}
}

func TestTypedSourceStripsData(t *testing.T) {
	cs := demoClient(t)
	cs.CoreV1().Secrets("x").Create(context.Background(), &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "plain", Namespace: "x"}, Data: map[string][]byte{"k": []byte("v")}}, metav1.CreateOptions{})
	src := NewTypedMetaSource(cs)
	l, err := src.List(context.Background(), metav1.ListOptions{LabelSelector: "owner=helm"})
	if err != nil {
		t.Fatal(err)
	}
	if len(l.Items) != 21 {
		t.Fatalf("items %d", len(l.Items))
	}
	b, _ := json.Marshal(l)
	if strings.Contains(string(b), `"data"`) || strings.Contains(string(b), "H4sI") {
		t.Fatal("payload leaked into metadata list")
	}
	// the watch filters by selector itself (the fake ignores it)
	w, _ := src.Watch(context.Background(), metav1.ListOptions{LabelSelector: "owner=helm"})
	defer w.Stop()
	cs.CoreV1().Secrets("x").Create(context.Background(), &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "plain2", Namespace: "x"}}, metav1.CreateOptions{})
	s := DemoSecrets("c1", time.Now())[0].DeepCopy()
	s.Name, s.Namespace = "sh.helm.release.v1.zz.v1", "x"
	s.Labels[LabelName] = "zz"
	cs.CoreV1().Secrets("x").Create(context.Background(), s, metav1.CreateOptions{})
	deadline := time.After(2 * time.Second)
	for found := false; !found; {
		select {
		case ev := <-w.ResultChan():
			m, ok := ev.Object.(*metav1.PartialObjectMetadata)
			if !ok || m.Name == "plain" || m.Name == "plain2" {
				t.Fatalf("non-helm secret leaked: %+v", ev)
			}
			found = m.Name == "sh.helm.release.v1.zz.v1"
		case <-deadline:
			t.Fatal("no event")
		}
	}
}

// ---- watch aggregation ------------------------------------------------------------------------

func next(t *testing.T, w watch.Interface) watch.Event {
	t.Helper()
	select {
	case ev, ok := <-w.ResultChan():
		if !ok {
			t.Fatal("watch closed")
		}
		return ev
	case <-time.After(2 * time.Second):
		t.Fatal("timeout")
	}
	return watch.Event{}
}

func TestWatchAggregation(t *testing.T) {
	src := &fakeMeta{items: []metav1.PartialObjectMetadata{pom("a", "alpha", 1, "deployed", false)}}
	api := NewReleaseAPI(src)
	if _, err := api.List(context.Background(), metav1.ListOptions{}); err != nil {
		t.Fatal(err)
	}
	w, err := api.Watch(context.Background(), metav1.ListOptions{ResourceVersion: "500", AllowWatchBookmarks: true})
	if err != nil {
		t.Fatal(err)
	}
	defer w.Stop()
	send := func(typ watch.EventType, m metav1.PartialObjectMetadata) { src.w.Action(typ, &m) }

	// a new revision: Modified with revision 2, status of the new one
	send(watch.Added, pom("a", "alpha", 2, "pending-upgrade", false))
	ev := next(t, w)
	r := ev.Object.(*Release)
	if ev.Type != watch.Modified || r.Revision != 2 || r.Status != "pending-upgrade" || r.UID != "helm:a/alpha" || r.ResourceVersion != "102" {
		t.Fatalf("got %v %+v", ev.Type, r)
	}
	// the old revision turning superseded does not change the latest: no event; then the latest turns deployed
	send(watch.Modified, pom("a", "alpha", 1, "superseded", false))
	send(watch.Modified, pom("a", "alpha", 2, "deployed", false))
	ev = next(t, w)
	if ev.Type != watch.Modified || ev.Object.(*Release).Status != "deployed" {
		t.Fatalf("got %v %+v", ev.Type, ev.Object)
	}
	// bookmark passes through with its resourceVersion and annotations
	bm := &metav1.PartialObjectMetadata{ObjectMeta: metav1.ObjectMeta{ResourceVersion: "900", Annotations: map[string]string{metav1.InitialEventsAnnotationKey: "true"}}}
	src.w.Action(watch.Bookmark, bm)
	ev = next(t, w)
	if ev.Type != watch.Bookmark || ev.Object.(*metav1.PartialObjectMetadata).ResourceVersion != "900" {
		t.Fatalf("bookmark %+v", ev)
	}
	// error passes through
	src.w.Action(watch.Error, &metav1.Status{Status: "Failure", Code: 410, Reason: metav1.StatusReasonExpired})
	if ev = next(t, w); ev.Type != watch.Error {
		t.Fatal("error not forwarded")
	}
	// latest revision deleted: Modified with the older one
	send(watch.Deleted, pom("a", "alpha", 2, "deployed", false))
	ev = next(t, w)
	if ev.Type != watch.Modified || ev.Object.(*Release).Revision != 1 || ev.Object.(*Release).Status != "superseded" {
		t.Fatalf("got %v %+v", ev.Type, ev.Object)
	}
	// last revision deleted: Deleted
	send(watch.Deleted, pom("a", "alpha", 1, "superseded", false))
	ev = next(t, w)
	if ev.Type != watch.Deleted || ev.Object.(*Release).Name != "alpha" {
		t.Fatalf("got %v", ev.Type)
	}
	// a brand new release: Added
	send(watch.Added, pom("b", "beta", 1, "deployed", true))
	ev = next(t, w)
	if ev.Type != watch.Added || ev.Object.(*Release).ManagedBy != "flux:flux-system/beta" {
		t.Fatalf("got %v", ev.Type)
	}
	// rows from events project
	if row := Module.ProjectObj(ev.Object); row == nil || row.UID() != "helm:b/beta" {
		t.Fatal("project")
	}
}

// ---- codec ------------------------------------------------------------------------------------

func TestEncodeDecodeRoundTrip(t *testing.T) {
	sec := DemoSecrets("c1", time.Now())[0]
	rel, err := DecodeRelease(sec.Data[ReleaseDataKey])
	if err != nil {
		t.Fatal(err)
	}
	enc, err := EncodeRelease(rel)
	if err != nil {
		t.Fatal(err)
	}
	rel2, err := DecodeRelease([]byte(enc))
	if err != nil || rel2.Name != rel.Name || rel2.Manifest != rel.Manifest || rel2.Chart.Metadata.Name != rel.Chart.Metadata.Name {
		t.Fatalf("round trip: %v", err)
	}
	if _, err := DecodeRelease([]byte("!!!")); err == nil {
		t.Fatal("expected error")
	}
	if _, err := DecodeRelease(nil); err == nil {
		t.Fatal("expected error")
	}
}

// helm3JSON is a release serialized the way Helm 3 stores it: no apply_method, info.status as a string,
// "deleted" as an empty time, no modtime fields, null lock/schema/files.
func helm3JSON(name, ns string, version int, status string) string {
	return fmt.Sprintf(`{"name":%q,"info":{"first_deployed":"2023-01-01T10:00:00.5Z","last_deployed":"2023-01-02T10:00:00Z","deleted":"","description":"Install complete","status":%q,"notes":"hello"},`+
		`"chart":{"metadata":{"name":"web","version":"1.0.0","description":"d","apiVersion":"v2","appVersion":"1.16.0","type":"application"},"lock":null,`+
		`"templates":[{"name":"templates/cm.yaml","data":"YXBpVmVyc2lvbjogdjEKa2luZDogQ29uZmlnTWFwCg=="}],"values":{"replicaCount":1,"image":{"tag":"a"}},"schema":null,"files":null},`+
		`"config":{"replicaCount":%d},"manifest":"---\n# Source: web/templates/cm.yaml\napiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: cm%d\n",`+
		`"hooks":[{"name":"h","kind":"Job","path":"web/templates/job.yaml","manifest":"kind: Job","events":["pre-upgrade"],"last_run":{"started_at":"0001-01-01T00:00:00Z","completed_at":"0001-01-01T00:00:00Z","phase":""},"weight":0,"delete_policies":["before-hook-creation"]}],`+
		`"version":%d,"namespace":%q}`, name, status, version, version, version, ns)
}

func helm3Secret(t *testing.T, ns, name string, version int, status string) *corev1.Secret {
	enc, err := encodeBytes([]byte(helm3JSON(name, ns, version, status)))
	if err != nil {
		t.Fatal(err)
	}
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: secretName(name, version), Namespace: ns, UID: types.UID(fmt.Sprintf("h3-%d", version)), ResourceVersion: "7",
			Labels: map[string]string{LabelName: name, LabelOwner: "helm", LabelStatus: status, LabelVersion: strconv.Itoa(version), LabelCreatedAt: "1672567200", LabelModified: "1672567200"}},
		Type: corev1.SecretType(ReleaseType),
		Data: map[string][]byte{ReleaseDataKey: []byte(enc)},
	}
}

func TestDecodeHelm3Payload(t *testing.T) {
	enc, _ := encodeBytes([]byte(helm3JSON("h3", "ns", 1, "deployed")))
	rel, err := DecodeRelease([]byte(enc))
	if err != nil {
		t.Fatal(err)
	}
	if rel.Info.Status != common.StatusDeployed || rel.Chart.Metadata.Version != "1.0.0" || rel.ApplyMethod != "" || rel.Info.LastDeployed.IsZero() || !rel.Info.Deleted.IsZero() {
		t.Fatalf("%+v", rel.Info)
	}
	p := Project(rel, "ns")
	if p.Chart != "web" || p.ChartVersion != "1.0.0" || p.AppVersion != "1.16.0" || p.Notes != "hello" || len(p.Hooks) != 1 || p.Hooks[0].Events[0] != "pre-upgrade" {
		t.Fatalf("%+v", p)
	}
}

// Verification item: a Helm 3 shaped release is read by the Helm 4 SDK storage driver and rolled back by
// action.Rollback against the printing kube client and a typed fake clientset secrets driver.
func TestHelm3ReleaseRollbackThroughHelm4SDK(t *testing.T) {
	cs := fake.NewSimpleClientset(helm3Secret(t, "ns", "h3", 1, "superseded"), helm3Secret(t, "ns", "h3", 2, "deployed"))
	cfg, err := NewFakeActionConfig(cs, "ns")
	if err != nil {
		t.Fatal(err)
	}
	last, err := cfg.Releases.Last("h3")
	if err != nil {
		t.Fatalf("Helm 4 cannot read the Helm 3 release: %v", err)
	}
	if last.(*releasev1.Release).Version != 2 {
		t.Fatal("last")
	}
	rb := action.NewRollback(cfg)
	rb.Version = 1
	rb.Timeout = 5 * time.Second
	if err := rb.Run("h3"); err != nil {
		t.Fatalf("rollback: %v", err)
	}
	hist, err := cfg.Releases.History("h3")
	if err != nil || len(hist) != 3 {
		t.Fatalf("history %d %v", len(hist), err)
	}
	got := map[int]string{}
	for _, h := range hist {
		r := h.(*releasev1.Release)
		got[r.Version] = string(r.Info.Status)
	}
	if got[3] != "deployed" || got[2] != "superseded" || got[1] != "superseded" {
		t.Fatalf("statuses %v", got)
	}
	r3, _ := cfg.Releases.Get("h3", 3)
	if r3.(*releasev1.Release).Info.RollbackRevision != 1 {
		t.Fatal("rollback revision")
	}
}

// ---- cache ------------------------------------------------------------------------------------

func relOfSize(n int) protocol.HelmRelease {
	return protocol.HelmRelease{Manifest: strings.Repeat("x", n)}
}

func TestCacheBudgetEvictionRecency(t *testing.T) {
	const cost = 1000 + entryOverhead
	c := NewCache(3 * cost)
	c.Put("a", relOfSize(1000))
	c.Put("b", relOfSize(1000))
	c.Put("c", relOfSize(1000))
	if c.Len() != 3 || c.Bytes() != 3*cost {
		t.Fatalf("len %d bytes %d", c.Len(), c.Bytes())
	}
	c.Get("a") // a is now most recent; b is the least recent
	c.Put("d", relOfSize(1000))
	if _, ok := c.Get("b"); ok {
		t.Fatal("b should be evicted")
	}
	for _, k := range []string{"a", "c", "d"} {
		if _, ok := c.Get(k); !ok {
			t.Fatalf("%s evicted", k)
		}
	}
	// an oversize entry is not cached and evicts nothing
	c.Put("huge", relOfSize(10*cost))
	if _, ok := c.Get("huge"); ok || c.Len() != 3 {
		t.Fatal("oversize cached")
	}
	// replacing a key keeps the accounting exact
	c.Put("a", relOfSize(500))
	if c.Bytes() != 2*cost+500+entryOverhead {
		t.Fatalf("bytes %d", c.Bytes())
	}
	// key by uid + resourceVersion
	c.Put(CacheKey("u1", "5"), relOfSize(10))
	if _, ok := c.Get(CacheKey("u1", "5")); !ok {
		t.Fatal("miss")
	}
	if _, ok := c.Get(CacheKey("u1", "6")); ok {
		t.Fatal("new resourceVersion must miss")
	}
	if NewCache(0).budget != DefaultCacheBytes {
		t.Fatal("default budget")
	}
}

func TestLimiterFourConcurrent(t *testing.T) {
	l := newLimiter(MaxConcurrentFetches)
	var cur, max atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			rel, err := l.acquire(context.Background(), "c1")
			if err != nil {
				t.Error(err)
				return
			}
			n := cur.Add(1)
			for {
				m := max.Load()
				if n <= m || max.CompareAndSwap(m, n) {
					break
				}
			}
			time.Sleep(10 * time.Millisecond)
			cur.Add(-1)
			rel()
		}()
	}
	wg.Wait()
	if max.Load() != 4 {
		t.Fatalf("max concurrent %d", max.Load())
	}
	// another cluster has its own slots; a cancelled ctx stops waiting
	var rels []func()
	for i := 0; i < 4; i++ {
		r, _ := l.acquire(context.Background(), "c2")
		rels = append(rels, r)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, err := l.acquire(ctx, "c2"); err == nil {
		t.Fatal("expected ctx error")
	}
	if r, err := l.acquire(context.Background(), "c3"); err != nil {
		t.Fatal(err)
	} else {
		r()
	}
	for _, r := range rels {
		r()
	}
}

// ---- service ----------------------------------------------------------------------------------

func TestReleaseRead(t *testing.T) {
	cs := demoClient(t)
	s := newService(t, cs)
	rel, err := s.Release(context.Background(), protocol.HelmReleaseParams{Cluster: "c1", Namespace: "podinfo", Name: "podinfo"})
	if err != nil {
		t.Fatal(err)
	}
	if rel.Revision != 3 || rel.Status != "deployed" || rel.Chart != "podinfo" || rel.ChartVersion != "6.7.1" || rel.AppVersion != "6.7.1" || rel.Description != "Upgrade complete" {
		t.Fatalf("%+v", rel)
	}
	if !strings.Contains(rel.Values, "replicaCount: 3") || !strings.Contains(rel.ComputedValues, "replicaCount: 3") ||
		!strings.Contains(rel.ComputedValues, "type: ClusterIP") || strings.Contains(rel.Values, "ClusterIP") {
		t.Fatalf("values:\n%s\ncomputed:\n%s", rel.Values, rel.ComputedValues)
	}
	if !strings.Contains(rel.Manifest, "replicas: 3") || rel.Notes == "" || rel.ManagedBy != nil || rel.Updated == 0 {
		t.Fatal("manifest/notes")
	}
	if len(rel.Hooks) != 1 || rel.Hooks[0].Kind != "Job" || rel.Hooks[0].Weight != -5 || len(rel.Hooks[0].Events) != 2 {
		t.Fatalf("hooks %+v", rel.Hooks)
	}
	if len(rel.History) != 3 || rel.History[0].Revision != 1 || rel.History[2].Status != "deployed" || rel.History[2].Chart != "podinfo-6.7.1" || rel.History[0].Chart != "" {
		t.Fatalf("history %+v", rel.History)
	}
	// older revision; second read hits the cache (body not re-fetched), and now its history row is filled in
	n := s.Cache().Len()
	if _, err = s.Release(context.Background(), protocol.HelmReleaseParams{Cluster: "c1", Namespace: "podinfo", Name: "podinfo"}); err != nil || s.Cache().Len() != n {
		t.Fatal("cache")
	}
	r1, err := s.Release(context.Background(), protocol.HelmReleaseParams{Cluster: "c1", Namespace: "podinfo", Name: "podinfo", Revision: 1})
	if err != nil || r1.Revision != 1 || r1.Status != "superseded" || r1.History[0].Chart == "" {
		t.Fatalf("%v %+v", err, r1)
	}
	// Flux-managed
	fl, err := s.Release(context.Background(), protocol.HelmReleaseParams{Cluster: "c1", Namespace: "monitoring", Name: "kube-prometheus-stack"})
	if err != nil || fl.ManagedBy == nil || fl.ManagedBy.Kind != "HelmRelease" || fl.ManagedBy.Name != "kube-prometheus-stack" || fl.ManagedBy.Namespace != "flux-system" {
		t.Fatalf("%v %+v", err, fl.ManagedBy)
	}
	// errors
	var ae *ActionError
	_, err = s.Release(context.Background(), protocol.HelmReleaseParams{Cluster: "c1", Namespace: "podinfo", Name: "nope"})
	if !errors.As(err, &ae) || ae.Body.Code != "not_found" {
		t.Fatalf("%v", err)
	}
	_, err = s.Release(context.Background(), protocol.HelmReleaseParams{Cluster: "c1", Namespace: "podinfo", Name: "podinfo", Revision: 9})
	if !errors.As(err, &ae) || ae.Body.Code != "not_found" {
		t.Fatalf("%v", err)
	}
}

func TestDemoSecretsAllDecode(t *testing.T) {
	secs := DemoSecrets("c1", time.Now())
	if len(secs) < 15 {
		t.Fatalf("only %d secrets", len(secs))
	}
	names := map[string]bool{}
	for _, sec := range secs {
		rel, err := DecodeRelease(sec.Data[ReleaseDataKey])
		if err != nil {
			t.Fatalf("%s: %v", sec.Name, err)
		}
		if sec.Name != secretName(rel.Name, rel.Version) || sec.Namespace != rel.Namespace || string(rel.Info.Status) != sec.Labels[LabelStatus] ||
			sec.Labels[LabelVersion] != strconv.Itoa(rel.Version) || string(sec.Type) != ReleaseType || sec.Labels[LabelOwner] != "helm" {
			t.Fatalf("%s inconsistent", sec.Name)
		}
		names[rel.Name+"/"+string(rel.Info.Status)] = true
	}
	for _, w := range []string{"podinfo/deployed", "broken-release/failed", "stuck-upgrade/pending-upgrade", "cleanup/uninstalled", "kube-prometheus-stack/deployed"} {
		if !names[w] {
			t.Errorf("missing %s", w)
		}
	}
	// the list over the typed wrapper sees one row per release, with the stuck one aged past 15 minutes
	api := NewReleaseAPI(NewTypedMetaSource(demoClient(t)))
	l, _ := api.List(context.Background(), metav1.ListOptions{})
	r := rows(t, l)
	if len(r) != 10 || r["podinfo"].Cell(0).N != 3 || r["broken-release"].Tone() != ingest.ToneDanger || r["kube-prometheus-stack"].StrCell(3) != "flux:flux-system/kube-prometheus-stack" {
		t.Fatalf("rows %d", len(r))
	}
	if age := time.Since(time.UnixMilli(r["stuck-upgrade"].Cell(2).N)); age < 15*time.Minute || r["stuck-upgrade"].StrCell(1) != "pending-upgrade" {
		t.Fatalf("stuck age %v", age)
	}
}

func TestDiff(t *testing.T) {
	s := newService(t, demoClient(t))
	ref := protocol.ObjectRef{Cluster: "c1", Namespace: "podinfo", Name: "podinfo", GVR: protocol.HelmGVR}
	d, err := s.Diff(context.Background(), protocol.HelmDiffParams{Ref: ref, FromRevision: 1, ToRevision: 3})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"--- revision 1\n", "+++ revision 3\n", "-replicaCount: 1", "+replicaCount: 3", "-  tag: 6.6.0", "+  tag: 6.7.1"} {
		if !strings.Contains(d.Values, want) {
			t.Errorf("values diff lacks %q:\n%s", want, d.Values)
		}
	}
	if !strings.Contains(d.Manifest, "-  replicas: 1") || !strings.Contains(d.Manifest, "+  replicas: 3") || !strings.Contains(d.Manifest, "@@") {
		t.Fatalf("manifest diff:\n%s", d.Manifest)
	}
	same, err := s.Diff(context.Background(), protocol.HelmDiffParams{Ref: ref, FromRevision: 3, ToRevision: 3})
	if err != nil || same.Values != "" || same.Manifest != "" {
		t.Fatalf("identical revisions: %v %+v", err, same)
	}
	if _, err = s.Diff(context.Background(), protocol.HelmDiffParams{Ref: ref, FromRevision: 0, ToRevision: 3}); err == nil {
		t.Fatal("expected error")
	}
}

func TestFluxGuard(t *testing.T) {
	cs := demoClient(t)
	s := newService(t, cs)
	p := protocol.HelmRollbackParams{Cluster: "c1", Namespace: "monitoring", Name: "kube-prometheus-stack", Revision: 1}
	_, _, err := s.PrepareRollback(context.Background(), p)
	var ae *ActionError
	if !errors.As(err, &ae) || ae.Body.Code != "gitops_managed" || ae.Body.Message != "release is managed by Flux HelmRelease flux-system/kube-prometheus-stack" || ae.Body.Hint == "" {
		t.Fatalf("%v", err)
	}
	if _, _, err = s.PrepareUninstall(context.Background(), protocol.HelmUninstallParams{Cluster: "c1", Namespace: "monitoring", Name: "kube-prometheus-stack"}); !errors.As(err, &ae) || ae.Body.Code != "gitops_managed" {
		t.Fatal("uninstall not guarded")
	}
	if _, _, err = s.PrepareRetry(context.Background(), protocol.HelmRetryParams{Cluster: "c1", Namespace: "monitoring", Name: "kube-prometheus-stack"}); !errors.As(err, &ae) || ae.Body.Code != "gitops_managed" {
		t.Fatal("retry not guarded")
	}
	p.Force = true
	run, prep, err := s.PrepareRollback(context.Background(), p)
	if err != nil || prep.UsedContext != "write-c1" || prep.Subject != "monitoring/kube-prometheus-stack" || prep.Cluster != "c1" {
		t.Fatalf("%v %+v", err, prep)
	}
	f := runJob(t, run)
	if f.State != protocol.JobDone || f.UsedContext != "write-c1" {
		t.Fatalf("%+v", f)
	}
	// not found and bad revision are synchronous
	if _, _, err = s.PrepareRollback(context.Background(), protocol.HelmRollbackParams{Cluster: "c1", Namespace: "x", Name: "none"}); !errors.As(err, &ae) || ae.Body.Code != "not_found" {
		t.Fatalf("%v", err)
	}
	if _, _, err = s.PrepareRollback(context.Background(), protocol.HelmRollbackParams{Cluster: "c1", Namespace: "podinfo", Name: "podinfo", Revision: 42}); !errors.As(err, &ae) || ae.Body.Code != "bad_request" {
		t.Fatalf("%v", err)
	}
	// Route errors are returned as they are
	boom := errors.New("route boom")
	s2 := New(Config{Runtime: func(string) (kube.Runtime, error) { return fakeRT{cs: cs}, nil },
		Route: func(context.Context, string, string, string, string, string, string) (string, bool, error) {
			return "", false, boom
		}})
	if _, _, err = s2.PrepareRollback(context.Background(), protocol.HelmRollbackParams{Cluster: "c1", Namespace: "podinfo", Name: "podinfo", Revision: 1}); !errors.Is(err, boom) {
		t.Fatalf("%v", err)
	}
}

func statuses(t *testing.T, cs kubernetes.Interface, ns, name string) map[int]string {
	t.Helper()
	l, err := cs.CoreV1().Secrets(ns).List(context.Background(), metav1.ListOptions{LabelSelector: "owner=helm,name=" + name})
	if err != nil {
		t.Fatal(err)
	}
	out := map[int]string{}
	for _, s := range l.Items {
		v, _ := strconv.Atoi(s.Labels[LabelVersion])
		out[v] = s.Labels[LabelStatus]
	}
	return out
}

func TestRollbackJob(t *testing.T) {
	cs := demoClient(t)
	s := newService(t, cs)
	run, _, err := s.PrepareRollback(context.Background(), protocol.HelmRollbackParams{Cluster: "c1", Namespace: "podinfo", Name: "podinfo", Revision: 1, Wait: true})
	if err != nil {
		t.Fatal(err)
	}
	f := runJob(t, run)
	if f.State != protocol.JobDone || len(f.Progress) < 2 {
		t.Fatalf("%+v", f)
	}
	st := statuses(t, cs, "podinfo", "podinfo")
	if len(st) != 4 || st[4] != "deployed" || st[3] != "superseded" {
		t.Fatalf("%v", st)
	}
	rel, err := s.Release(context.Background(), protocol.HelmReleaseParams{Cluster: "c1", Namespace: "podinfo", Name: "podinfo"})
	if err != nil || rel.Revision != 4 || !strings.Contains(rel.Description, "Rollback to 1") {
		t.Fatalf("%v %+v", err, rel)
	}
}

func TestUninstallKeepHistory(t *testing.T) {
	cs := demoClient(t)
	s := newService(t, cs)
	run, _, err := s.PrepareUninstall(context.Background(), protocol.HelmUninstallParams{Cluster: "c1", Namespace: "team-2", Name: "redis", KeepHistory: true})
	if err != nil {
		t.Fatal(err)
	}
	if f := runJob(t, run); f.State != protocol.JobDone {
		t.Fatalf("%+v", f)
	}
	st := statuses(t, cs, "team-2", "redis")
	if len(st) != 4 || st[4] != "uninstalled" {
		t.Fatalf("history not kept: %v", st)
	}
	run, _, err = s.PrepareUninstall(context.Background(), protocol.HelmUninstallParams{Cluster: "c1", Namespace: "team-1", Name: "worker", Cascade: "foreground"})
	if err != nil {
		t.Fatal(err)
	}
	if f := runJob(t, run); f.State != protocol.JobDone {
		t.Fatalf("%+v", f)
	}
	if st := statuses(t, cs, "team-1", "worker"); len(st) != 0 {
		t.Fatalf("history should be gone: %v", st)
	}
	if _, _, err = s.PrepareUninstall(context.Background(), protocol.HelmUninstallParams{Cluster: "c1", Namespace: "team-1", Name: "worker", Cascade: "bogus"}); err == nil {
		t.Fatal("expected not_found")
	}
}

func TestRetryStuckPending(t *testing.T) {
	cs := demoClient(t)
	s := newService(t, cs)
	p := protocol.HelmRetryParams{Cluster: "c1", Namespace: "team-3", Name: "stuck-upgrade"}
	run, _, err := s.PrepareRetry(context.Background(), p)
	if err != nil {
		t.Fatal(err)
	}
	f := runJob(t, run)
	if f.State != protocol.JobFailed || f.Error == nil || f.Error.Code != "conflict" || !strings.Contains(f.Error.Message, "markFailed") {
		t.Fatalf("%+v", f)
	}
	if st := statuses(t, cs, "team-3", "stuck-upgrade"); len(st) != 2 || st[2] != "pending-upgrade" {
		t.Fatalf("nothing may change without markFailed: %v", st)
	}
	p.MarkFailed = true
	run, _, _ = s.PrepareRetry(context.Background(), p)
	f = runJob(t, run)
	if f.State != protocol.JobDone {
		t.Fatalf("%+v", f)
	}
	st := statuses(t, cs, "team-3", "stuck-upgrade")
	if len(st) != 3 || st[2] != "failed" || st[3] != "deployed" {
		t.Fatalf("%v", st)
	}
	rel, err := s.Release(context.Background(), protocol.HelmReleaseParams{Cluster: "c1", Namespace: "team-3", Name: "stuck-upgrade"})
	if err != nil || rel.Revision != 3 || !strings.Contains(rel.Values, "replicaCount: 3") {
		t.Fatalf("%v %+v", err, rel)
	}
	// a failed release retries without markFailed
	run, _, _ = s.PrepareRetry(context.Background(), protocol.HelmRetryParams{Cluster: "c1", Namespace: "default", Name: "broken-release"})
	if f = runJob(t, run); f.State != protocol.JobDone {
		t.Fatalf("%+v", f)
	}
	if st := statuses(t, cs, "default", "broken-release"); st[3] != "deployed" {
		t.Fatalf("%v", st)
	}
}

func TestMapError(t *testing.T) {
	if f := MapError(errors.New("another operation (install/upgrade/rollback) is in progress")); f.Body.Code != "conflict" {
		t.Fatal(f.Body.Code)
	}
	if f := MapError(fmt.Errorf("x: %w", errors.New("line one\nline two"))); f.Body.Code != "internal" || strings.Contains(f.Body.Message, "two") {
		t.Fatal(f.Body)
	}
}
