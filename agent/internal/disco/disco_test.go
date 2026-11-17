package disco

import (
	"context"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
)

const aggCore = `{"kind":"APIGroupDiscoveryList","apiVersion":"apidiscovery.k8s.io/v2","items":[
{"metadata":{},"versions":[{"version":"v1","resources":[
 {"resource":"pods","responseKind":{"group":"","version":"v1","kind":"Pod"},"scope":"Namespaced","shortNames":["po"],"verbs":["create","delete","get","list","patch","update","watch"],"subresources":[{"subresource":"log","responseKind":{"kind":"Pod"},"verbs":["get"]}]},
 {"resource":"nodes","responseKind":{"kind":"Node"},"scope":"Cluster","shortNames":["no"],"verbs":["get","list","watch"]},
 {"resource":"bindings","responseKind":{"kind":"Binding"},"scope":"Namespaced","verbs":["create"]}
]}]}]}`

const aggGroups = `{"kind":"APIGroupDiscoveryList","apiVersion":"apidiscovery.k8s.io/v2","items":[
{"metadata":{"name":"apps"},"versions":[{"version":"v1","resources":[
 {"resource":"deployments","responseKind":{"kind":"Deployment"},"scope":"Namespaced","shortNames":["deploy"],"verbs":["get","list","watch"],"subresources":[{"subresource":"scale","responseKind":{"kind":"Scale"},"verbs":["get"]}]}]}]},
{"metadata":{"name":"example.io"},"versions":[
 {"version":"v2","resources":[{"resource":"widgets","responseKind":{"kind":"Widget"},"scope":"Namespaced","verbs":["list","watch"]}]},
 {"version":"v1","resources":[
  {"resource":"widgets","responseKind":{"kind":"Widget"},"scope":"Namespaced","verbs":["list","watch"]},
  {"resource":"gadgets","responseKind":{"kind":"Gadget"},"scope":"Cluster","verbs":["list","watch"]}]}]}
]}`

func byGVR(rs []Resource) map[string]Resource {
	m := map[string]Resource{}
	for _, r := range rs {
		m[r.GVR()] = r
	}
	return m
}

func checkCommon(t *testing.T, rs []Resource) {
	t.Helper()
	m := byGVR(rs)
	want := []string{"apps/v1/deployments", "example.io/v1/gadgets", "example.io/v2/widgets", "v1/bindings", "v1/nodes", "v1/pods"}
	var got []string
	for _, r := range rs {
		got = append(got, r.GVR())
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v want %v", got, want)
	}
	p := m["v1/pods"]
	if p.Kind != "Pod" || !p.Namespaced || !reflect.DeepEqual(p.ShortNames, []string{"po"}) || !p.Listable() {
		t.Fatalf("pods %+v", p)
	}
	if n := m["v1/nodes"]; n.Namespaced || n.Kind != "Node" {
		t.Fatalf("nodes %+v", n)
	}
	if b := m["v1/bindings"]; b.Listable() || len(b.Verbs) != 1 {
		t.Fatalf("bindings %+v", b)
	}
	if g := m["example.io/v1/gadgets"]; g.Namespaced {
		t.Fatalf("gadgets %+v", g)
	}
}

func TestParseAggregated(t *testing.T) {
	rs, err := ParseAggregated([]byte(aggCore), []byte(aggGroups))
	if err != nil {
		t.Fatal(err)
	}
	checkCommon(t, rs)
	rs, err = ParseAggregated(nil, []byte(strings.ReplaceAll(aggGroups, "/v2", "/v2beta1")))
	if err != nil || len(rs) != 3 {
		t.Fatalf("%v %d", err, len(rs))
	}
	if rs, err = ParseAggregated(nil, nil); err != nil || len(rs) != 0 {
		t.Fatal(err)
	}
	if _, err = ParseAggregated([]byte(`{"kind":"APIVersions","versions":["v1"]}`), nil); err == nil {
		t.Fatal("expected error")
	}
}

const legCore = `{"kind":"APIVersions","versions":["v1"]}`
const legGroups = `{"kind":"APIGroupList","apiVersion":"v1","groups":[
{"name":"apps","versions":[{"groupVersion":"apps/v1","version":"v1"}],"preferredVersion":{"groupVersion":"apps/v1","version":"v1"}},
{"name":"example.io","versions":[{"groupVersion":"example.io/v2","version":"v2"},{"groupVersion":"example.io/v1","version":"v1"}],"preferredVersion":{"groupVersion":"example.io/v1","version":"v1"}}]}`

var legLists = map[string]string{
	"v1": `{"kind":"APIResourceList","groupVersion":"v1","resources":[
 {"name":"pods","singularName":"","namespaced":true,"kind":"Pod","verbs":["get","list","watch"],"shortNames":["po"]},
 {"name":"pods/log","namespaced":true,"kind":"Pod","verbs":["get"]},
 {"name":"nodes","namespaced":false,"kind":"Node","verbs":["list","watch"]},
 {"name":"bindings","namespaced":true,"kind":"Binding","verbs":["create"]}]}`,
	"apps/v1":       `{"resources":[{"name":"deployments","namespaced":true,"kind":"Deployment","verbs":["list","watch"],"shortNames":["deploy"]},{"name":"deployments/scale","namespaced":true,"kind":"Scale","verbs":["get"]}]}`,
	"example.io/v1": `{"resources":[{"name":"widgets","namespaced":true,"kind":"Widget","verbs":["list","watch"]},{"name":"gadgets","namespaced":false,"kind":"Gadget","verbs":["list","watch"]}]}`,
	"example.io/v2": `{"resources":[{"name":"widgets","namespaced":true,"kind":"Widget","verbs":["list","watch"]}]}`,
}

func TestParseLegacy(t *testing.T) {
	lists := map[string][]byte{}
	for k, v := range legLists {
		lists[k] = []byte(v)
	}
	rs, err := ParseLegacy([]byte(legCore), []byte(legGroups), lists)
	if err != nil {
		t.Fatal(err)
	}
	// legacy uses the preferred version (v1) for example.io
	m := byGVR(rs)
	if _, ok := m["example.io/v1/widgets"]; !ok || len(rs) != 6 {
		t.Fatalf("%v", rs)
	}
	delete(m, "example.io/v1/widgets")
	m["example.io/v2/widgets"] = Resource{}
	for _, r := range rs {
		if strings.Contains(r.Resource, "/") {
			t.Fatal("subresource leaked")
		}
	}
	// only v2 list available: falls to the next version that serves it
	delete(lists, "example.io/v1")
	rs, _ = ParseLegacy([]byte(legCore), []byte(legGroups), lists)
	if _, ok := byGVR(rs)["example.io/v2/widgets"]; !ok {
		t.Fatalf("%v", rs)
	}
}

func testClient(t *testing.T, h http.Handler) rest.Interface {
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	c, err := rest.UnversionedRESTClientFor(&rest.Config{
		Host: srv.URL,
		ContentConfig: rest.ContentConfig{
			GroupVersion:         &schema.GroupVersion{},
			NegotiatedSerializer: scheme.Codecs.WithoutConversion(),
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestFetchAggregated(t *testing.T) {
	var gotAccept atomic.Value
	c := testClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAccept.Store(r.Header.Get("Accept"))
		if !strings.Contains(r.Header.Get("Accept"), "as=APIGroupDiscoveryList") {
			http.Error(w, "nope", http.StatusNotAcceptable)
			return
		}
		w.Header().Set("Content-Type", "application/json;g=apidiscovery.k8s.io;v=v2;as=APIGroupDiscoveryList")
		switch r.URL.Path {
		case "/api":
			w.Write([]byte(aggCore))
		case "/apis":
			w.Write([]byte(aggGroups))
		default:
			http.NotFound(w, r)
		}
	}))
	rs, err := Fetch(context.Background(), c)
	if err != nil {
		t.Fatal(err)
	}
	checkCommon(t, rs)
	if a, _ := gotAccept.Load().(string); !strings.Contains(a, "v=v2") {
		t.Fatalf("accept %q", a)
	}
}

func TestFetchLegacyFallback(t *testing.T) {
	for _, mode := range []string{"plain", "406"} {
		t.Run(mode, func(t *testing.T) {
			var hits atomic.Int32
			c := testClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				agg := strings.Contains(r.Header.Get("Accept"), "as=APIGroupDiscoveryList")
				if agg && mode == "406" {
					http.Error(w, "nope", http.StatusNotAcceptable)
					return
				}
				w.Header().Set("Content-Type", "application/json")
				switch r.URL.Path {
				case "/api":
					w.Write([]byte(legCore))
				case "/apis":
					w.Write([]byte(legGroups))
				case "/api/v1":
					w.Write([]byte(legLists["v1"]))
				case "/apis/apps/v1":
					w.Write([]byte(legLists["apps/v1"]))
				case "/apis/example.io/v1":
					w.Write([]byte(legLists["example.io/v1"]))
				default:
					hits.Add(1) // non-preferred versions must not be fetched
					http.NotFound(w, r)
				}
			}))
			rs, err := Fetch(context.Background(), c)
			if err != nil {
				t.Fatal(err)
			}
			if hits.Load() != 0 || len(rs) != 6 {
				t.Fatalf("hits=%d rs=%v", hits.Load(), rs)
			}
			if _, ok := byGVR(rs)["example.io/v1/widgets"]; !ok {
				t.Fatal("missing preferred widgets")
			}
		})
	}
}
