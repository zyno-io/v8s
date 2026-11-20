package security

import (
	"context"
	"testing"

	"github.com/zyno-io/v8s/agent/internal/ingest"
	"github.com/zyno-io/v8s/agent/pkg/protocol"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
)

type fakeStore map[string][]*ingest.Row

func (f fakeStore) put(t *testing.T, gvr string, o runtime.Object) {
	t.Helper()
	m := ingest.Lookup(gvr)
	if m == nil {
		t.Fatalf("%s: no module", gvr)
	}
	r := m.ProjectObj(o)
	if r == nil {
		t.Fatalf("%s: projection returned nil", gvr)
	}
	f[gvr] = append(f[gvr], r)
}

func (f fakeStore) putRoute(t *testing.T, raw string) {
	t.Helper()
	gvr := gvrRoutes[0]
	m := ingest.Lookup(gvr)
	if m == nil {
		t.Fatalf("%s: no module", gvr)
	}
	o, err := m.DecodeJSON([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	f[gvr] = append(f[gvr], m.ProjectObj(o))
}

func (f fakeStore) scan(cluster, gvr string, fn func(*ingest.Row) bool) bool {
	rows, ok := f[gvr]
	for _, r := range rows {
		if !fn(r) {
			break
		}
	}
	return ok
}

func meta(ns, name string, owner ...string) metav1.ObjectMeta {
	m := metav1.ObjectMeta{Namespace: ns, Name: name, UID: types.UID("u-" + ns + "-" + name), ResourceVersion: "1"}
	if len(owner) == 2 {
		ctl := true
		m.OwnerReferences = []metav1.OwnerReference{{Kind: owner[0], Name: owner[1], Controller: &ctl}}
	}
	return m
}

func svc(ns, name string, typ corev1.ServiceType) *corev1.Service {
	return &corev1.Service{ObjectMeta: meta(ns, name), Spec: corev1.ServiceSpec{Type: typ}}
}

func slice(ns, svc string, pods ...string) *discoveryv1.EndpointSlice {
	m := meta(ns, svc+"-abc")
	m.Labels = map[string]string{discoveryv1.LabelServiceName: svc}
	e := &discoveryv1.EndpointSlice{ObjectMeta: m, AddressType: discoveryv1.AddressTypeIPv4}
	for _, p := range pods {
		e.Endpoints = append(e.Endpoints, discoveryv1.Endpoint{Addresses: []string{"10.0.0.1"}, TargetRef: &corev1.ObjectReference{Kind: "Pod", Name: p}})
	}
	return e
}

func ingress(ns, name, backend string) *networkingv1.Ingress {
	return &networkingv1.Ingress{ObjectMeta: meta(ns, name), Spec: networkingv1.IngressSpec{Rules: []networkingv1.IngressRule{{Host: "x.example.com",
		IngressRuleValue: networkingv1.IngressRuleValue{HTTP: &networkingv1.HTTPIngressRuleValue{Paths: []networkingv1.HTTPIngressPath{{
			Backend: networkingv1.IngressBackend{Service: &networkingv1.IngressServiceBackend{Name: backend}}}}}}}}}}
}

func pod(ns, name string, owner ...string) *corev1.Pod {
	return &corev1.Pod{ObjectMeta: meta(ns, name, owner...)}
}

func TestExposureOf(t *testing.T) {
	f := fakeStore{}
	// web: ClusterIP Service behind an Ingress; api: LoadBalancer; db: headless (internal only); node: NodePort; gw: HTTPRoute.
	f.put(t, gvrServices, svc("n", "web", corev1.ServiceTypeClusterIP))
	f.put(t, gvrServices, svc("n", "api", corev1.ServiceTypeLoadBalancer))
	f.put(t, gvrServices, svc("n", "db", corev1.ServiceTypeClusterIP))
	f.put(t, gvrServices, svc("n", "node", corev1.ServiceTypeNodePort))
	f.put(t, gvrServices, svc("g", "gw", corev1.ServiceTypeClusterIP))
	f.put(t, gvrIngresses, ingress("n", "web", "web"))
	f.putRoute(t, `{"kind":"HTTPRoute","metadata":{"namespace":"g","name":"r","uid":"r1"},"spec":{"rules":[{"backendRefs":[{"name":"gw","port":80},{"kind":"ServiceImport","name":"other"}]}]}}`)
	f.put(t, gvrSlices, slice("n", "web", "web-5d8f-aaaaa"))
	f.put(t, gvrSlices, slice("n", "api", "web-5d8f-aaaaa", "api-0"))
	f.put(t, gvrSlices, slice("n", "db", "db-0"))
	f.put(t, gvrSlices, slice("n", "node", "solo"))
	f.put(t, gvrSlices, slice("g", "gw", "nightly-29000000-xyz"))
	f.put(t, gvrPods, pod("n", "web-5d8f-aaaaa", "ReplicaSet", "web-5d8f"))
	f.put(t, gvrPods, pod("n", "api-0", "StatefulSet", "api"))
	f.put(t, gvrPods, pod("n", "db-0", "StatefulSet", "db"))
	f.put(t, gvrPods, pod("n", "solo"))
	f.put(t, gvrPods, pod("n", "idle-0", "StatefulSet", "idle"))
	f.put(t, gvrPods, pod("g", "nightly-29000000-xyz", "Job", "nightly-29000000"))
	f.put(t, gvrJobs, &batchv1.Job{ObjectMeta: meta("g", "nightly-29000000", "CronJob", "nightly")})
	f.put(t, gvrSlices, slice("g", "gw", "db-migrate-abcde"))
	f.put(t, gvrPods, pod("g", "db-migrate-abcde", "Job", "db-migrate"))
	f.put(t, gvrJobs, &batchv1.Job{ObjectMeta: meta("g", "db-migrate")})

	ex, known := ExposureOf(f.scan, nil, "c")
	if !known {
		t.Fatal("unknown")
	}
	const S, NP, LB, IN = protocol.ExposureService, protocol.ExposureNodePort, protocol.ExposureLoadBalancer, protocol.ExposureIngress
	want := map[WorkloadRef]int{
		{"Deployment", "n", "web"}:            S | IN | LB,
		{"ReplicaSet", "n", "web-5d8f"}:       S | IN | LB,
		{"StatefulSet", "n", "api"}:           S | LB,
		{"StatefulSet", "n", "db"}:            S,
		{"Pod", "n", "solo"}:                  S | NP,
		{"StatefulSet", "n", "idle"}:          0,
		{"Job", "g", "nightly-29000000"}:      S | IN,
		{"CronJob", "g", "nightly"}:           S | IN,
		{"Deployment", "n", "missing"}:        0,
		{"Pod", "n", "web-5d8f-aaaaa"}:        S | IN | LB,
		{"StatefulSet", "other-ns", "db"}:     0,
		{"Deployment", "g", "nightly-290000"}: 0,
		{"Job", "g", "db-migrate"}:            S | IN,
		{"CronJob", "g", "db"}:                0, // a plain Job's name is not a CronJob's
	}
	for w, bits := range want {
		if got := ex[w]; got != bits {
			t.Errorf("%v: got %04b want %04b", w, got, bits)
		}
	}
}

func TestExposureNilWithoutServices(t *testing.T) {
	f := fakeStore{}
	f.put(t, gvrPods, pod("n", "p"))
	if ex, known := ExposureOf(f.scan, nil, "c"); ex != nil || !known {
		t.Fatalf("%v %v", ex, known)
	}
	if _, known := ExposureOf(nil, nil, "c"); known {
		t.Fatal("no scan is unknown")
	}
}

func TestExposureUnknownUntilInputsSync(t *testing.T) {
	f := fakeStore{}
	f.put(t, gvrServices, svc("n", "s", corev1.ServiceTypeNodePort))
	f.putRoute(t, `{"kind":"HTTPRoute","metadata":{"namespace":"n","name":"r","uid":"r1"},"spec":{"rules":[{"backendRefs":[{"name":"s"}]}]}}`)
	pending := map[string]bool{}
	synced := func(_, gvr string) bool { return !pending[gvr] }
	if _, known := ExposureOf(f.scan, synced, "c"); !known {
		t.Fatal("all synced: known")
	}
	for _, gvr := range append(append([]string{}, exposureInputs...), gvrRoutes[0]) {
		pending[gvr] = true
		if _, known := ExposureOf(f.scan, synced, "c"); known {
			t.Fatalf("%s not synced: want unknown", gvr)
		}
		pending[gvr] = false
	}
	pending[gvrRoutes[2]] = true // grpcroutes not served (not watched): does not matter
	if _, known := ExposureOf(f.scan, synced, "c"); !known {
		t.Fatal("unwatched route kind: known")
	}
}

func TestImagesCarryExposure(t *testing.T) {
	f := fakeStore{}
	f.put(t, gvrServices, svc("x", "lb", corev1.ServiceTypeLoadBalancer))
	f.put(t, gvrSlices, slice("x", "lb", "exposed-abc12-zzzzz"))
	f.put(t, gvrPods, pod("x", "exposed-abc12-zzzzz", "ReplicaSet", "exposed-abc12"))
	RegisterImageSource(func(cfg *Config, cluster string, emit func(ImageVuln)) {
		if cluster != "exp" {
			return
		}
		emit(ImageVuln{Source: "trivy", Cluster: cluster, Repository: "exp", Tag: "1", Workload: WorkloadRef{"Deployment", "x", "exposed"}})
		emit(ImageVuln{Source: "trivy", Cluster: cluster, Repository: "exp", Tag: "1", Workload: WorkloadRef{"Deployment", "x", "hidden"}})
		emit(ImageVuln{Source: "trivy", Cluster: cluster, Repository: "int", Tag: "1", Workload: WorkloadRef{"Deployment", "x", "hidden"}})
	})
	s := New(Config{Scan: f.scan})
	res, _ := s.Images(context.Background(), protocol.SecurityImagesParams{Clusters: []string{"exp"}})
	got := map[string]int{}
	for _, i := range res.Images {
		got[i.Image] = i.Exposure
	}
	if got["exp:1"] != protocol.ExposureService|protocol.ExposureLoadBalancer || got["int:1"] != 0 {
		t.Fatalf("%v", got)
	}
	s = New(Config{Scan: f.scan, Synced: func(_, gvr string) bool { return gvr != gvrPods }})
	res, _ = s.Images(context.Background(), protocol.SecurityImagesParams{Clusters: []string{"exp"}})
	for _, i := range res.Images {
		if i.Exposure != protocol.ExposureUnknown {
			t.Fatalf("pods not synced: %+v", i)
		}
	}
}
