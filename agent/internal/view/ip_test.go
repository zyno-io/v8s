package view

import (
	"bytes"
	"strings"
	"testing"

	"github.com/vmihailenco/msgpack/v5"
	"github.com/zyno-io/v8s/agent/internal/ingest"
	"github.com/zyno-io/v8s/agent/pkg/protocol"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

func ipPod(uid, name string, ips ...string) *ingest.PodRow {
	p := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "a", UID: types.UID(uid), ResourceVersion: "1"}}
	p.Spec.Containers = []corev1.Container{{Name: "c"}}
	p.Status.Phase = corev1.PodRunning
	for _, ip := range ips {
		p.Status.PodIPs = append(p.Status.PodIPs, corev1.PodIP{IP: ip})
	}
	return ingest.Project(p)
}

func viewOrder(t *testing.T, e *Engine, q protocol.ViewQuery) string {
	t.Helper()
	s, err := e.Subscribe(q, 1, win(0, 100))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	m := newModel()
	m.pull(t, s)
	var out []string
	for _, id := range m.ids {
		out = append(out, m.rows[id].Row.Name())
	}
	return strings.Join(out, ",")
}

func ipEngine() *Engine {
	e := newEngine()
	attach(e, "c1",
		ipPod("1", "a-1", "10.0.0.9"),
		ipPod("2", "a-2", "10.0.0.10"),
		ipPod("3", "a-3", "10.0.1.5"),
		ipPod("4", "a-4", "fd00::5"),
		ipPod("5", "a-5"),
		ipPod("6", "v1.2-api", "192.168.1.1"),
		ipPod("7", "a-7", "10.0.0.9", "fd00::9"),
	)
	return e
}

func TestIPTextFilter(t *testing.T) {
	e := ipEngine()
	byName := func(s string) protocol.ViewQuery {
		return protocol.ViewQuery{Text: s, Sort: []protocol.Sort{{Col: "name"}}}
	}
	for _, c := range []struct{ text, want string }{
		{"10.0.0.", "a-1,a-2,a-7"},
		{"10.0.0.9", "a-1,a-7"},
		{"10.0.", "a-1,a-2,a-3,a-7"},
		{"10.0.0.0/24", "a-1,a-2,a-7"},
		{"10.0.0.10-10.0.1.5", "a-2,a-3"},
		{"fd00::9", "a-7"},
		{"fd00:", ""},           // one colon is not an IP: name search only
		{"[fd00::5]:80", "a-4"}, // brackets and port
		{"1.2", "v1.2-api"},     // name contains it; also a prefix of 1.2xx addresses
		{"a-5", "a-5"},          // not an IP: the name search as before
		{"10.0.0.99", ""},
	} {
		if got := viewOrder(t, e, byName(c.text)); got != c.want {
			t.Errorf("text %q: got %q, want %q", c.text, got, c.want)
		}
	}
}

func TestIPFilterOnlyForKindsWithIPs(t *testing.T) {
	for gvr, want := range map[string]bool{"v1/pods": true, "v1/services": true, "discovery.k8s.io/v1/endpointslices": true, "v1/nodes": true,
		"networking.k8s.io/v1/ingresses": true, "apps/v1/deployments": false, "v1/namespaces": false, "batch/v1/jobs": false} {
		n, err := Normalize(protocol.ViewQuery{GVR: gvr, Text: "10.0.0.1"})
		if err != nil {
			t.Fatal(gvr, err)
		}
		if (n.ip != nil) != want {
			t.Errorf("%s: ip filter = %v, want %v", gvr, n.ip != nil, want)
		}
	}
	n, _ := Normalize(protocol.ViewQuery{GVR: "v1/pods", Text: "web"})
	if n.ip != nil {
		t.Error("a name must not read as an IP")
	}
	a, _ := Normalize(protocol.ViewQuery{GVR: "v1/pods", Text: " 10.0.0.1 "})
	b, _ := Normalize(protocol.ViewQuery{GVR: "v1/pods", Text: "10.0.0.1"})
	if a.Key != b.Key {
		t.Error("equal texts share a key")
	}
}

func TestIPColumnSortsNumerically(t *testing.T) {
	e := ipEngine()
	asc := viewOrder(t, e, protocol.ViewQuery{Sort: []protocol.Sort{{Col: "ip"}}})
	if want := "a-1,a-7,a-2,a-3,v1.2-api,a-4,a-5"; asc != want { // 10.0.0.9 < 10.0.0.10 < 10.0.1.5 < 192.168.1.1 < v6; no IP last
		t.Fatalf("ip asc: %s, want %s", asc, want)
	}
	desc := viewOrder(t, e, protocol.ViewQuery{Sort: []protocol.Sort{{Col: "ip", Desc: true}}})
	if want := "a-4,v1.2-api,a-3,a-2,a-1,a-7,a-5"; desc != want {
		t.Fatalf("ip desc: %s, want %s", desc, want)
	}
	// the column is filterable as text
	if got := viewOrder(t, e, protocol.ViewQuery{Filters: []protocol.Filter{{Col: "ip", Op: "contains", Value: "fd00"}}, Sort: []protocol.Sort{{Col: "name"}}}); got != "a-4,a-7" {
		t.Fatalf("ip contains: %s", got)
	}
}

type oneDict struct{}

func (oneDict) ID(ingest.Str) uint32 { return 1 }

func TestIPColumnEncoding(t *testing.T) {
	mod := ingest.Lookup(PodsGVR)
	idx := -1
	for i, c := range mod.Cols {
		if c.Key == "ip" {
			idx = i
		}
	}
	if idx < 0 {
		t.Fatal("no ip column")
	}
	if mod.Cols[idx].Type != protocol.ColStr || mod.Cols[idx].Src != ingest.SrcIP {
		t.Fatalf("ip column: %+v", mod.Cols[idx])
	}
	for _, c := range []struct {
		ips  []string
		want string
	}{{nil, ""}, {[]string{"10.0.0.5"}, "10.0.0.5"}, {[]string{"fd00::5", "10.0.0.5"}, "10.0.0.5,fd00::5"}} {
		var buf bytes.Buffer
		enc := msgpack.NewEncoder(&buf)
		_ = EncodeRow(enc, oneDict{}, mod, &PendRow{ID: 7, Row: ipPod("1", "p", c.ips...)})
		var row []any
		if err := msgpack.NewDecoder(&buf).Decode(&row); err != nil {
			t.Fatal(err)
		}
		if len(row) != 2+len(mod.Cols) || row[2+idx] != c.want {
			t.Fatalf("row %v: ip cell %v, want %q", row, row[2+idx], c.want)
		}
	}
	// the wire schema (view.subscribe) matches the documented Pods columns
	if cols := mod.Columns(); cols[idx].Key != protocol.PodColumns[idx].Key || len(cols) != len(protocol.PodColumns) {
		t.Fatalf("columns drifted from protocol.PodColumns")
	}
}
