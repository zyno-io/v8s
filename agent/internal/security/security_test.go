package security

import (
	"context"
	"reflect"
	"testing"

	"github.com/zyno-io/v8s/agent/internal/ingest"
	"github.com/zyno-io/v8s/agent/pkg/protocol"
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
