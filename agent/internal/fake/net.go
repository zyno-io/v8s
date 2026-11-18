package fake

import (
	"encoding/json"
	"fmt"

	_ "github.com/zyno-io/v8s/agent/internal/netkinds" // the modules that read the kinds below
)

// Network fixtures for IP search: LoadBalancer Services with MetalLB addresses, a Service that only requests one,
// a dual-stack Service, and the IP-only CRDs (ServiceCIDR, MetalLB, Multus, Cilium, Calico). Pod, node, endpoint
// and ingress addresses come from the base world (world.go).

func init() {
	def := func(gvr, apiVersion, kind, resource string, namespaced bool, short ...string) *kindDef {
		return &kindDef{gvr: gvr, apiVersion: apiVersion, kind: kind, resource: resource, namespaced: namespaced, short: short}
	}
	registerKinds(
		def("networking.k8s.io/v1/servicecidrs", "networking.k8s.io/v1", "ServiceCIDR", "servicecidrs", false),
		def("metallb.io/v1beta1/ipaddresspools", "metallb.io/v1beta1", "IPAddressPool", "ipaddresspools", true),
		def("metallb.io/v1beta2/bgppeers", "metallb.io/v1beta2", "BGPPeer", "bgppeers", true),
		def("k8s.cni.cncf.io/v1/network-attachment-definitions", "k8s.cni.cncf.io/v1", "NetworkAttachmentDefinition", "network-attachment-definitions", true, "net-attach-def"),
		def("cilium.io/v2/ciliumloadbalancerippools", "cilium.io/v2", "CiliumLoadBalancerIPPool", "ciliumloadbalancerippools", false),
		def("cilium.io/v2/ciliumnodes", "cilium.io/v2", "CiliumNode", "ciliumnodes", false),
		def("crd.projectcalico.org/v1/ippools", "crd.projectcalico.org/v1", "IPPool", "ippools", false),
	)
	registerGen(genNetwork)
	registerGen(genIPCRDs)
}

// genIPCRDs adds the IP-only CRD objects: on every cluster the ServiceCIDR, two MetalLB pools (`public` covers
// 192.168.100.10) and a BGP peer and four NetworkAttachmentDefinitions; even-index clusters run Cilium (a
// LoadBalancer pool and one CiliumNode per node), odd-index clusters Calico (an IPPool).
func genIPCRDs(w *World, c *Cluster) {
	w.putLocked("networking.k8s.io/v1/servicecidrs", map[string]any{"metadata": objMeta("", "kubernetes", nil),
		"spec": map[string]any{"cidrs": []any{"10.96.0.0/12"}}})
	w.putLocked("metallb.io/v1beta1/ipaddresspools", map[string]any{"metadata": objMeta("metallb-system", "public", nil),
		"spec": map[string]any{"addresses": []any{"192.168.100.0/24"}}})
	w.putLocked("metallb.io/v1beta1/ipaddresspools", map[string]any{"metadata": objMeta("metallb-system", "reserved", nil),
		"spec": map[string]any{"addresses": []any{"192.168.200.10-192.168.200.50", "fd00:200::/120"}, "autoAssign": false}})
	w.putLocked("metallb.io/v1beta2/bgppeers", map[string]any{"metadata": objMeta("metallb-system", "tor-a", nil),
		"spec": map[string]any{"peerAddress": "192.168.0.1", "sourceAddress": "192.168.0.2", "peerASN": int64(64512), "myASN": int64(64513)}})

	nad := func(ns, name string, cfg map[string]any) {
		b, _ := json.Marshal(cfg)
		w.putLocked("k8s.cni.cncf.io/v1/network-attachment-definitions", map[string]any{"metadata": objMeta(ns, name, nil),
			"spec": map[string]any{"config": string(b)}})
	}
	nad("team-1", "macvlan-conf", map[string]any{"cniVersion": "0.3.1", "type": "macvlan", "master": "eth0", "ipam": map[string]any{
		"type": "host-local", "subnet": "192.168.50.0/24", "rangeStart": "192.168.50.2", "rangeEnd": "192.168.50.251", "gateway": "192.168.50.1"}})
	nad("team-2", "whereabouts-net", map[string]any{"cniVersion": "0.3.1", "type": "macvlan", "ipam": map[string]any{
		"type": "whereabouts", "range": "192.168.51.0/24"}})
	nad("team-3", "static-net", map[string]any{"cniVersion": "0.3.1", "plugins": []any{map[string]any{"type": "bridge", "ipam": map[string]any{
		"type": "static", "addresses": []any{map[string]any{"address": "10.10.0.5/24", "gateway": "10.10.0.1"}}}}}})
	nad("team-4", "sriov-net", map[string]any{"cniVersion": "0.3.1", "type": "sriov", "ipam": map[string]any{
		"type": "host-local", "ranges": []any{[]any{map[string]any{"subnet": "192.168.60.0/24"}}, []any{map[string]any{"subnet": "fd00:60::/64"}}}}})

	if clusterIndex(c.name)%2 == 0 {
		w.putLocked("cilium.io/v2/ciliumloadbalancerippools", map[string]any{"metadata": objMeta("", "lb-pool", nil),
			"spec": map[string]any{"blocks": []any{map[string]any{"cidr": "172.18.250.0/24"}, map[string]any{"start": "172.18.251.10", "stop": "172.18.251.20"}}}})
		for i := 0; i < nodes; i++ {
			w.putLocked("cilium.io/v2/ciliumnodes", map[string]any{"metadata": objMeta("", fmt.Sprintf("node-%d", i), nil),
				"spec": map[string]any{"addresses": []any{map[string]any{"type": "InternalIP", "ip": nodeIP(i)}, map[string]any{"type": "CiliumInternalIP", "ip": nodeCNIAddr(i)}},
					"ipam": map[string]any{"podCIDRs": []any{nodePodCIDR(i)}}}})
		}
	} else {
		w.putLocked("crd.projectcalico.org/v1/ippools", map[string]any{"metadata": objMeta("", "default-ipv4-ippool", nil),
			"spec": map[string]any{"cidr": "10.64.0.0/10", "ipipMode": "Never", "vxlanMode": "Always"}})
	}
}

func genNetwork(w *World, c *Cluster) {
	svcPorts := []any{map[string]any{"port": int64(80), "protocol": "TCP", "targetPort": int64(8080)}}
	w.putLocked("v1/services", map[string]any{
		"metadata": withAnnotations(objMeta("ingress-nginx", "ingress-nginx-controller", map[string]string{"app.kubernetes.io/name": "ingress-nginx"}),
			map[string]any{"metallb.io/loadBalancerIPs": "192.168.100.10"}),
		"spec": map[string]any{"type": "LoadBalancer", "clusterIP": "10.96.250.1", "clusterIPs": []any{"10.96.250.1"},
			"selector": map[string]any{"app.kubernetes.io/name": "ingress-nginx"}, "ports": svcPorts},
		"status": map[string]any{"loadBalancer": map[string]any{"ingress": []any{map[string]any{"ip": "192.168.100.10"}}}}})
	w.putLocked("v1/services", map[string]any{"metadata": objMeta("team-2", "legacy-lb", nil),
		"spec": map[string]any{"type": "LoadBalancer", "clusterIP": "10.96.250.2", "clusterIPs": []any{"10.96.250.2"}, "loadBalancerIP": "192.168.100.250",
			"selector": map[string]any{"app": "svc-2"}, "ports": svcPorts}})
	if clusterIndex(c.name) == 0 {
		w.putLocked("v1/services", map[string]any{"metadata": objMeta("team-0", "dual", nil),
			"spec": map[string]any{"type": "ClusterIP", "clusterIP": "10.96.251.1", "clusterIPs": []any{"10.96.251.1", "fd00:10:96::1"},
				"ipFamilies": []any{"IPv4", "IPv6"}, "ipFamilyPolicy": "RequireDualStack", "selector": map[string]any{"app": "svc-0"}, "ports": svcPorts}})
	}
}

func withAnnotations(md map[string]any, ann map[string]any) map[string]any {
	md["annotations"] = ann
	return md
}
