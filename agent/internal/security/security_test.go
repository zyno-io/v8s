package security

import (
	"context"
	"fmt"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/zyno-io/v8s/agent/internal/ingest"
	"github.com/zyno-io/v8s/agent/pkg/protocol"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func TestAggregateImagesAcrossWorkloadsAndClusters(t *testing.T) {
	wl := func(k, ns, n string) WorkloadRef { return WorkloadRef{k, ns, n} }
	in := []ImageVuln{
		{Source: "trivy", Cluster: "a", Repository: "nginx", Tag: "1.25", Critical: 4, High: 31, Workload: wl("Deployment", "n1", "web")},
		{Source: "trivy", Cluster: "a", Repository: "nginx", Tag: "1.25", Critical: 4, High: 30, Workload: wl("Deployment", "n1", "web")}, // second container, same workload
		{Source: "trivy", Cluster: "a", Repository: "nginx", Tag: "1.25", Critical: 4, High: 31, Workload: wl("StatefulSet", "n2", "db")},
		{Source: "trivy", Cluster: "b", Repository: "nginx", Tag: "1.25", Critical: 4, High: 31, Workload: wl("Deployment", "n1", "web")}, // same name, other cluster
		{Source: "kubescape", Cluster: "a", Repository: "nginx", Tag: "1.25", Critical: 1, Workload: wl("Deployment", "n1", "web")},
		{Source: "trivy", Cluster: "a", Repository: "redis", Tag: "7", Critical: 4, High: 40, Workload: wl("Deployment", "n3", "cache")},
		{Source: "trivy", Cluster: "a", Repository: "alpine", Tag: "3", Workload: wl("Deployment", "n3", "tool")},
	}
	got := AggregateImages(in)
	if len(got) != 4 {
		t.Fatalf("want 4 rows (kubescape is not merged with trivy): %+v", got)
	}
	// ranked by critical, then high
	if got[0].Image != "redis:7" || got[1].Image != "nginx:1.25" || got[1].Source != "trivy" {
		t.Fatalf("ranking: %+v", got)
	}
	n := got[1]
	if n.Critical != 4 || n.High != 31 || n.Workloads != 3 || n.Reports != 4 || !reflect.DeepEqual(n.Clusters, []string{"a", "b"}) {
		t.Fatalf("nginx: %+v", n)
	}
}

func TestImagesHandlerUsesRegisteredSources(t *testing.T) {
	RegisterImageSource(func(cfg *Config, cluster string, emit func(ImageVuln)) {
		emit(ImageVuln{Source: "trivy", Cluster: cluster, Repository: "x", Tag: "1", Critical: 1, Workload: WorkloadRef{"Deployment", "n", cluster}})
	})
	s := New(Config{Clusters: func() []string { return []string{"c1", "c2"} }})
	res, err := s.Images(context.Background(), protocol.SecurityImagesParams{})
	if err != nil || len(res.Images) != 1 || res.Images[0].Workloads != 2 || len(res.Images[0].Clusters) != 2 {
		t.Fatalf("%+v %v", res, err)
	}
	res, _ = s.Images(context.Background(), protocol.SecurityImagesParams{Clusters: []string{"c2"}})
	if res.Images[0].Workloads != 1 {
		t.Fatalf("cluster filter: %+v", res)
	}
}

func TestChunkSlicesWholeReport(t *testing.T) {
	d := &protocol.ReportDetail{Kind: "vulnerabilities", Total: 5, Vulns: make([]protocol.VulnFinding, 5)}
	if c := chunk(d, 3, 10); len(c.Vulns) != 2 || c.Offset != 3 {
		t.Fatalf("%+v", c)
	}
	if c := chunk(d, 9, 10); c.Vulns != nil || c.Total != 5 {
		t.Fatalf("past the end: %+v", c)
	}
}

func TestDetailCacheByResourceVersion(t *testing.T) {
	c := newDetailCache(2)
	now := testNow()
	d := &protocol.ReportDetail{Total: 1}
	c.put("a", "7", d, now)
	if c.get("a", "7", now) != d {
		t.Fatal("same rv must hit")
	}
	if c.get("a", "8", now) != nil {
		t.Fatal("a new resourceVersion must miss")
	}
	c.put("b", "", d, now)
	if c.get("b", "", now) != d {
		t.Fatal("unknown rv hits within the ttl")
	}
	if c.get("b", "", now.Add(ttlNoRV+1)) != nil {
		t.Fatal("unknown rv expires")
	}
}

func TestNormalizeWorkloadStripsReplicaSetHash(t *testing.T) {
	if w := NormalizeWorkload("ReplicaSet", "n", "web-5d8f9"); w.Kind != "Deployment" || w.Name != "web" {
		t.Fatalf("%+v", w)
	}
	if w := NormalizeWorkload("StatefulSet", "n", "db"); w.Kind != "StatefulSet" {
		t.Fatalf("%+v", w)
	}
}

var _ = ingest.ToneDanger

var gvrSeq atomic.Int32

// uniqueGVR keeps providers registered by repeated runs (-count) apart: the registry is process-wide.
func uniqueGVR(kind string) string { return fmt.Sprintf("test.io/v1/%s%d", kind, gvrSeq.Add(1)) }

func detailTestService(t *testing.T, gvr string, fetch func(ctx context.Context, ref protocol.ObjectRef) (*unstructured.Unstructured, error)) *Service {
	t.Helper()
	RegisterDetail(DetailProvider{
		Match: func(g string) bool { return g == gvr },
		Fetch: func(ctx context.Context, _ *Config, ref protocol.ObjectRef) (*unstructured.Unstructured, error) {
			return fetch(ctx, ref)
		},
		Build: func(o *unstructured.Unstructured) (*protocol.ReportDetail, error) {
			return &protocol.ReportDetail{Kind: "vulnerabilities", Total: 3, Vulns: make([]protocol.VulnFinding, 3), ResourceVersion: o.GetResourceVersion()}, nil
		}})
	return New(Config{})
}

// Concurrent requests for the same report share one fetch.
func TestReportDetailDedupesConcurrentFetches(t *testing.T) {
	gvr := uniqueGVR("dedupereports")
	var fetches atomic.Int32
	release := make(chan struct{})
	s := detailTestService(t, gvr, func(ctx context.Context, ref protocol.ObjectRef) (*unstructured.Unstructured, error) {
		fetches.Add(1)
		<-release
		return &unstructured.Unstructured{Object: map[string]any{"metadata": map[string]any{"name": ref.Name, "resourceVersion": "5"}}}, nil
	})
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := s.ReportDetail(context.Background(), protocol.ReportDetailParams{Ref: protocol.ObjectRef{Cluster: "c", GVR: gvr, Namespace: "n", Name: "r"}}); err != nil {
				t.Error(err)
			}
		}()
	}
	time.Sleep(100 * time.Millisecond)
	close(release)
	wg.Wait()
	if n := fetches.Load(); n != 1 {
		t.Fatalf("%d fetches of one report", n)
	}
}

// At most maxDetailFetches distinct reports of one cluster are fetched at a time.
func TestReportDetailBoundsFetchesPerCluster(t *testing.T) {
	gvr := uniqueGVR("boundedreports")
	var cur, peak atomic.Int32
	s := detailTestService(t, gvr, func(ctx context.Context, ref protocol.ObjectRef) (*unstructured.Unstructured, error) {
		n := cur.Add(1)
		defer cur.Add(-1)
		for {
			p := peak.Load()
			if n <= p || peak.CompareAndSwap(p, n) {
				break
			}
		}
		time.Sleep(30 * time.Millisecond)
		return &unstructured.Unstructured{Object: map[string]any{"metadata": map[string]any{"name": ref.Name, "resourceVersion": "5"}}}, nil
	})
	var wg sync.WaitGroup
	for i := range 16 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ref := protocol.ObjectRef{Cluster: "c", GVR: gvr, Namespace: "n", Name: fmt.Sprintf("r%d", i)}
			if _, err := s.ReportDetail(context.Background(), protocol.ReportDetailParams{Ref: ref}); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if p := peak.Load(); p > maxDetailFetches || p < 2 {
		t.Fatalf("peak concurrency %d, want 2..%d", p, maxDetailFetches)
	}
}

func TestChunkCapsLimit(t *testing.T) {
	d := &protocol.ReportDetail{Kind: "vulnerabilities", Total: 5000, Vulns: make([]protocol.VulnFinding, 5000)}
	if c := chunk(d, 0, 1_000_000); len(c.Vulns) != maxChunk {
		t.Fatalf("limit not capped: %d rows", len(c.Vulns))
	}
}
