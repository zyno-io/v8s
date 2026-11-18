package ingest

import (
	"fmt"
	"strings"
	"testing"

	"github.com/zyno-io/v8s/agent/internal/ipx"
	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func tokStrings(s *ipx.Set) []string {
	var out []string
	if s == nil {
		return out
	}
	for _, t := range s.T {
		out = append(out, t.Field.Label()+"="+t.String())
	}
	return out
}

func want(t *testing.T, s *ipx.Set, w ...string) {
	t.Helper()
	got := tokStrings(s)
	if fmt.Sprint(got) != fmt.Sprint(w) && !(len(got) == 0 && len(w) == 0) {
		t.Fatalf("tokens %v, want %v", got, w)
	}
}

func TestPodIPs(t *testing.T) {
	p := &corev1.Pod{}
	want(t, podIPs(p)) // pending: none
	p.Status.PodIP = "10.0.0.5"
	want(t, podIPs(p), "Pod IP=10.0.0.5") // podIP fallback
	p.Status.PodIPs = []corev1.PodIP{{IP: "10.0.0.5"}, {IP: "fd00::5"}}
	want(t, podIPs(p), "Pod IP=10.0.0.5", "Pod IP=fd00::5")
	if s := podIPs(p); s.T[0].Single() == false || len(s.T) != 2 {
		t.Fatal("dual stack")
	}
}

func TestPodNetworkStatus(t *testing.T) {
	ann := func(v string) map[string]string { return map[string]string{netStatusKey: v} }
	p := &corev1.Pod{}
	p.Status.PodIPs = []corev1.PodIP{{IP: "10.0.0.5"}}
	p.Annotations = ann(`[{"name":"k8s-pod-network","ips":["10.0.0.5"],"default":true},{"name":"team-1/macvlan","interface":"net1","ips":["192.168.50.7","fd50::7"]}]`)
	want(t, podIPs(p), "Pod IP=10.0.0.5", "Secondary network IP=192.168.50.7", "Secondary network IP=fd50::7")
	p.Annotations = map[string]string{netStatusLegacyKey: `[{"name":"x","ips":["192.168.9.9"]}]`}
	want(t, podIPs(p), "Pod IP=10.0.0.5", "Secondary network IP=192.168.9.9")
	p.Annotations = ann(`not json`)
	want(t, podIPs(p), "Pod IP=10.0.0.5")
	p.Annotations = ann(`[{"ips":["` + strings.Repeat("1", 17<<10) + `"]}]`)
	want(t, podIPs(p), "Pod IP=10.0.0.5")
	var many []string
	for i := 0; i < 100; i++ {
		many = append(many, fmt.Sprintf(`"192.168.1.%d"`, i))
	}
	p.Annotations = ann(`[{"ips":[` + strings.Join(many, ",") + `]}]`)
	if n := len(podIPs(p).T); n != 1+maxNetStatusIPs {
		t.Fatalf("%d tokens, want %d", n, 1+maxNetStatusIPs)
	}
}

func TestServiceIPs(t *testing.T) {
	s := &corev1.Service{}
	s.Spec.ClusterIP = "None"
	want(t, serviceIPs(s))
	s.Spec.ClusterIP = "10.96.0.1"
	want(t, serviceIPs(s), "Cluster IP=10.96.0.1")
	s.Spec.ClusterIPs = []string{"10.96.0.1", "fd00:96::1"}
	s.Spec.ExternalIPs = []string{"203.0.113.9"}
	s.Spec.LoadBalancerIP = "192.168.100.250"
	s.Status.LoadBalancer.Ingress = []corev1.LoadBalancerIngress{{IP: "192.168.100.10"}, {Hostname: "lb.example.com"}}
	want(t, serviceIPs(s), "Cluster IP=10.96.0.1", "LB ingress IP=192.168.100.10", "Requested LB IP=192.168.100.250", "External IP=203.0.113.9", "Cluster IP=fd00:96::1")
}

func TestServiceIPsFields(t *testing.T) {
	s := &corev1.Service{}
	s.Spec.ClusterIPs = []string{"None"}
	s.Spec.ExternalIPs = []string{"203.0.113.9"}
	s.Spec.LoadBalancerIP = "192.168.100.250"
	s.Status.LoadBalancer.Ingress = []corev1.LoadBalancerIngress{{IP: "192.168.100.10"}, {Hostname: "lb.example.com"}}
	got := map[string]string{}
	for _, tk := range serviceIPs(s).T {
		got[tk.Field.Label()] = tk.String()
	}
	for l, v := range map[string]string{"External IP": "203.0.113.9", "Requested LB IP": "192.168.100.250", "LB ingress IP": "192.168.100.10"} {
		if got[l] != v {
			t.Errorf("%s = %q, want %q (%v)", l, got[l], v, got)
		}
	}
	if len(got) != 3 {
		t.Errorf("hostname or None leaked: %v", got)
	}
	for i, k := range IPAnnotationsLB {
		s := &corev1.Service{}
		s.Annotations = map[string]string{k: fmt.Sprintf("192.168.7.%d, 192.168.8.%d", i, i)}
		if n := len(serviceIPs(s).T); n != 2 {
			t.Errorf("annotation %s: %d tokens", k, n)
		}
	}
}

func TestEndpointSliceIPs(t *testing.T) {
	e := &discoveryv1.EndpointSlice{AddressType: discoveryv1.AddressTypeIPv4,
		Endpoints: []discoveryv1.Endpoint{{Addresses: []string{"10.1.0.1"}}, {Addresses: []string{"10.1.0.2", "10.1.0.1"}}}}
	want(t, endpointSliceIPs(e), "Endpoint=10.1.0.1", "Endpoint=10.1.0.2")
	f := &discoveryv1.EndpointSlice{AddressType: discoveryv1.AddressTypeFQDN, Endpoints: []discoveryv1.Endpoint{{Addresses: []string{"db.example.com"}}}}
	want(t, endpointSliceIPs(f))
}

func TestNodeIPs(t *testing.T) {
	n := &corev1.Node{}
	n.Status.Addresses = []corev1.NodeAddress{{Type: corev1.NodeInternalIP, Address: "10.0.0.10"}, {Type: corev1.NodeExternalIP, Address: "203.0.113.20"},
		{Type: corev1.NodeHostName, Address: "node-1"}}
	n.Spec.PodCIDR = "10.64.0.0/24"
	want(t, nodeIPs(n), "Node internal IP=10.0.0.10", "Pod CIDR=10.64.0.0/24", "Node external IP=203.0.113.20")
	n.Spec.PodCIDRs = []string{"10.64.0.0/24", "fd00:64::/64"}
	if got := tokStrings(nodeIPs(n)); len(got) != 4 {
		t.Fatalf("podCIDRs: %v", got)
	}
	n.Annotations = map[string]string{
		"projectcalico.org/IPv4Address":         "10.0.0.10/24",
		"projectcalico.org/IPv4VXLANTunnelAddr": "10.64.0.1",
		"network.cilium.io/ipv4-pod-cidr":       "10.65.0.0/24",
	}
	got := nodeIPs(n)
	var cni []string
	for _, tk := range got.T {
		if tk.Field == FNodeCNI {
			cni = append(cni, tk.String())
		}
	}
	if fmt.Sprint(cni) != "[10.0.0.10 10.64.0.1 10.65.0.0/24]" {
		t.Fatalf("cni: %v", cni)
	}
	for _, a := range IPAnnotationsNode {
		v := "10.9.9.9/24"
		n := &corev1.Node{}
		n.Annotations = map[string]string{a.Key: v}
		if s := nodeIPs(n); s == nil || len(s.T) != 1 {
			t.Errorf("annotation %s not indexed", a.Key)
		}
	}
}

func TestIngressIPs(t *testing.T) {
	in := &networkingv1.Ingress{}
	in.Status.LoadBalancer.Ingress = []networkingv1.IngressLoadBalancerIngress{{IP: "192.168.100.10"}, {Hostname: "x.elb.amazonaws.com"}}
	want(t, ingressIPs(in), "LB ingress IP=192.168.100.10")
}

func TestCellsEqualDetectsIPChange(t *testing.T) {
	mk := func(ip string) *Row {
		p := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "a", Namespace: "n", UID: "u"}}
		p.Status.PodIP = ip
		return Project(p)
	}
	a, b, c := mk("10.0.0.1"), mk("10.0.0.1"), mk("10.0.0.2")
	if !a.CellsEqual(b) {
		t.Fatal("same IP must be equal")
	}
	if a.CellsEqual(c) {
		t.Fatal("an IP-only change must not be equal")
	}
	if a.CellsEqual(mk("")) {
		t.Fatal("losing the IP must not be equal")
	}
}

func TestRowIPText(t *testing.T) {
	p := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "a", UID: "u"}}
	p.Status.PodIPs = []corev1.PodIP{{IP: "fd00::5"}, {IP: "10.0.0.5"}}
	p.Annotations = map[string]string{netStatusKey: `[{"ips":["192.168.1.1"]}]`}
	r := Project(p)
	if got := r.IPText(FPodIP); got != "10.0.0.5,fd00::5" {
		t.Fatalf("IPText = %q", got)
	}
	if got := r.IPText(FPodNetwork); got != "192.168.1.1" {
		t.Fatalf("network IPText = %q", got)
	}
	if got := r.IPText(FClusterIP); got != "" {
		t.Fatalf("absent field: %q", got)
	}
	if u, ok := r.FirstIP(FPodIP); !ok || u.Addr(true).String() != "10.0.0.5" {
		t.Fatalf("FirstIP = %v %v", u, ok)
	}
	if _, ok := r.FirstIP(FClusterIP); ok {
		t.Fatal("FirstIP of an absent field")
	}
}

func BenchmarkPodIPs(b *testing.B) {
	p := &corev1.Pod{}
	p.Status.PodIP = "10.64.12.34"
	p.Status.PodIPs = []corev1.PodIP{{IP: "10.64.12.34"}}
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_ = podIPs(p)
	}
}

func TestDefaultOnlySkipIsSafe(t *testing.T) {
	for _, c := range []struct {
		v    string
		skip bool
	}{
		{`[{"name":"k8s-pod-network","ips":["10.0.0.5"],"default":true,"dns":{}}]`, true},
		{`[{"name": "x", "ips": ["10.0.0.5"], "default": true}]`, false}, // spaced JSON is parsed, never skipped
		{`[{"name":"x","ips":["10.0.0.5"],"default":true},{"name":"y","ips":["192.168.1.1"]}]`, false},
		{`[{"name":"y","ips":["192.168.1.1"]}]`, false},
		{`[{"name":"x","default":true},{"name":"y","ips":["192.168.1.1"]}]`, false}, // one ips, one default, different objects
		{`[{"name":"y","ips":["192.168.1.1"],"default":false}]`, false},
		{`[{"name":"y","ips":["192.168.1.1"]},{"name":"x","default":true}]`, false},
		{`[{"name":"x"}]`, true},
		{`[]`, true},
	} {
		if got := defaultOnly(c.v); got != c.skip {
			t.Errorf("defaultOnly(%s) = %v, want %v", c.v, got, c.skip)
		}
	}
}

func TestNetworkStatusKeys(t *testing.T) {
	// the legacy key is read only when the current one is absent
	p := &corev1.Pod{}
	p.Annotations = map[string]string{
		netStatusKey:       `[{"ips":["192.168.1.1"]}]`,
		netStatusLegacyKey: `[{"ips":["192.168.2.2"]}]`,
	}
	want(t, podIPs(p), "Secondary network IP=192.168.1.1")
	// a default-only current key means nothing to add
	p.Annotations[netStatusKey] = `[{"name":"k8s-pod-network","ips":["10.0.0.5"],"default":true}]`
	want(t, podIPs(p))
}

func TestNodeDualStackAnnotation(t *testing.T) {
	n := &corev1.Node{}
	n.Annotations = map[string]string{"ovn.kubernetes.io/ip_address": "100.64.0.2, fd00:100:64::2", "projectcalico.org/IPv4Address": "10.0.0.10/24,10.0.1.10/24"}
	want(t, nodeIPs(n), "CNI node address=10.0.0.10", "CNI node address=10.0.1.10", "CNI node address=100.64.0.2", "CNI node address=fd00:100:64::2")
}

func BenchmarkPodIPsDefaultOnlyAnnotation(b *testing.B) {
	p := &corev1.Pod{}
	p.Status.PodIP = "10.64.12.34"
	p.Status.PodIPs = []corev1.PodIP{{IP: "10.64.12.34"}}
	p.Annotations = map[string]string{
		netStatusKey:       `[{"name":"k8s-pod-network","ips":["10.64.12.34"],"default":true,"dns":{}}]`,
		netStatusLegacyKey: `[{"name":"k8s-pod-network","ips":["10.64.12.34"],"default":true,"dns":{}}]`,
	}
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_ = podIPs(p)
	}
}

func BenchmarkDefaultOnly(b *testing.B) {
	v := `[{"name":"k8s-pod-network","ips":["10.64.12.34"],"default":true,"dns":{}}]`
	for i := 0; i < b.N; i++ {
		if !defaultOnly(v) {
			b.Fatal("must skip")
		}
	}
}
