package ingest

import (
	"encoding/json"
	"strings"

	"github.com/zyno-io/v8s/agent/internal/ipx"
	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	networkingv1 "k8s.io/api/networking/v1"
)

// IP fields of the built-in kinds. Where an address was found shows in IP search hits.
var (
	FPodIP        = ipx.RegisterField("status.podIPs", "Pod IP", false)
	FPodNetwork   = ipx.RegisterField("metadata.annotations[k8s.v1.cni.cncf.io/network-status]", "Secondary network IP", false)
	FClusterIP    = ipx.RegisterField("spec.clusterIPs", "Cluster IP", false)
	FExternalIP   = ipx.RegisterField("spec.externalIPs", "External IP", false)
	FLBRequested  = ipx.RegisterField("spec.loadBalancerIP", "Requested LB IP", false)
	FLBIngress    = ipx.RegisterField("status.loadBalancer.ingress", "LB ingress IP", false)
	FEndpoint     = ipx.RegisterField("endpoints[].addresses", "Endpoint", false)
	FNodeInternal = ipx.RegisterField("status.addresses", "Node internal IP", false)
	FNodeExternal = ipx.RegisterField("status.addresses", "Node external IP", false)
	FNodeCNI      = ipx.RegisterField("metadata.annotations", "CNI node address", false)
	FPodCIDR      = ipx.RegisterField("spec.podCIDRs", "Pod CIDR", false)
)

// IPAnnotationsLB are the Service annotations that request load balancer addresses (comma lists).
var IPAnnotationsLB = []string{
	"metallb.universe.tf/loadBalancerIPs",
	"metallb.io/loadBalancerIPs",
	"kube-vip.io/loadbalancerIPs",
	"lbipam.cilium.io/ips",
	"io.cilium/lb-ipam-ips",
}

// IPAnnotation is a Node annotation that holds an address (host part of the CIDR form) or, with Span, a CIDR.
type IPAnnotation struct {
	Key  string
	Span bool
}

// IPAnnotationsNode are the CNI annotations that name node-level addresses.
var IPAnnotationsNode = []IPAnnotation{
	{Key: "projectcalico.org/IPv4Address"},
	{Key: "projectcalico.org/IPv6Address"},
	{Key: "projectcalico.org/IPv4IPIPTunnelAddr"},
	{Key: "projectcalico.org/IPv4VXLANTunnelAddr"},
	{Key: "projectcalico.org/IPv6VXLANTunnelAddr"},
	{Key: "projectcalico.org/IPv4WireguardInterfaceAddr"},
	{Key: "flannel.alpha.coreos.com/public-ip"},
	{Key: "network.cilium.io/ipv4-cilium-host"},
	{Key: "network.cilium.io/ipv6-cilium-host"},
	{Key: "network.cilium.io/ipv4-health-ip"},
	{Key: "network.cilium.io/ipv4-pod-cidr", Span: true},
	{Key: "ovn.kubernetes.io/ip_address"},
}

const (
	netStatusKey       = "k8s.v1.cni.cncf.io/network-status"
	netStatusLegacyKey = "k8s.v1.cni.cncf.io/networks-status"
	maxNetStatusBytes  = 16 << 10
	maxNetStatusIPs    = 32
)

// networkStatus adds the addresses of the secondary networks a Multus annotation lists (the default network is
// the pod IP already). Oversize or malformed annotations are skipped.
func networkStatus(ann map[string]string, b *ipx.Builder) {
	v := ann[netStatusKey]
	if v == "" { // older Multus writes only the legacy key
		v = ann[netStatusLegacyKey]
	}
	if v == "" || len(v) > maxNetStatusBytes || defaultOnly(v) {
		return
	}
	var st []struct {
		IPs     []string `json:"ips"`
		Default bool     `json:"default"`
	}
	if json.Unmarshal([]byte(v), &st) != nil {
		return
	}
	n := 0
	for i := range st {
		if st[i].Default {
			continue
		}
		for _, ip := range st[i].IPs {
			if n++; n > maxNetStatusIPs {
				return
			}
			b.Addr(ip, FPodNetwork)
		}
	}
}

// defaultOnly reports, without parsing, that a network-status value cannot contribute an address: it has no
// "ips" key at all, or exactly one "ips" and one "default":true (the compact form Multus writes) in the same
// object: the usual annotation of a pod with only its default network, whose address is the pod IP already.
// Anything else, including spaced JSON, is parsed. Same object: no brace sits between the two keys, and a key
// inside a string would be escaped, so the keys found here are real ones.
func defaultOnly(v string) bool {
	i := strings.Index(v, `"ips"`)
	if i < 0 {
		return true
	}
	if strings.LastIndex(v, `"ips"`) != i {
		return false
	}
	j := strings.Index(v, `"default":true`)
	if j < 0 || strings.LastIndex(v, `"default"`) != j {
		return false
	}
	return !strings.ContainsAny(v[min(i, j):max(i, j)], "{}")
}

func podIPs(p *corev1.Pod) *ipx.Set {
	var b ipx.Builder
	collectPodIPs(p, &b)
	return b.Set()
}

func collectPodIPs(p *corev1.Pod, b *ipx.Builder) {
	if len(p.Status.PodIPs) > 0 {
		for i := range p.Status.PodIPs {
			b.Addr(p.Status.PodIPs[i].IP, FPodIP)
		}
	} else {
		b.Addr(p.Status.PodIP, FPodIP)
	}
	if len(p.Annotations) > 0 {
		networkStatus(p.Annotations, b)
	}
}

func serviceIPs(s *corev1.Service) *ipx.Set {
	var b ipx.Builder
	if len(s.Spec.ClusterIPs) > 0 {
		for _, ip := range s.Spec.ClusterIPs {
			b.Addr(ip, FClusterIP)
		}
	} else {
		b.Addr(s.Spec.ClusterIP, FClusterIP)
	}
	for _, ip := range s.Spec.ExternalIPs {
		b.Addr(ip, FExternalIP)
	}
	b.Addr(s.Spec.LoadBalancerIP, FLBRequested)
	if len(s.Annotations) > 0 {
		for _, k := range IPAnnotationsLB {
			if v := s.Annotations[k]; v != "" {
				b.List(v, FLBRequested)
			}
		}
	}
	for i := range s.Status.LoadBalancer.Ingress {
		b.Addr(s.Status.LoadBalancer.Ingress[i].IP, FLBIngress)
	}
	return b.Set()
}

func endpointSliceIPs(e *discoveryv1.EndpointSlice) *ipx.Set {
	if e.AddressType == discoveryv1.AddressTypeFQDN {
		return nil
	}
	var b ipx.Builder
	for i := range e.Endpoints {
		for _, a := range e.Endpoints[i].Addresses {
			b.Addr(a, FEndpoint)
		}
	}
	return b.Set()
}

func nodeIPs(n *corev1.Node) *ipx.Set {
	var b ipx.Builder
	for i := range n.Status.Addresses {
		switch a := &n.Status.Addresses[i]; a.Type {
		case corev1.NodeInternalIP:
			b.Addr(a.Address, FNodeInternal)
		case corev1.NodeExternalIP:
			b.Addr(a.Address, FNodeExternal)
		}
	}
	if len(n.Spec.PodCIDRs) > 0 {
		for _, c := range n.Spec.PodCIDRs {
			b.Span(c, FPodCIDR)
		}
	} else {
		b.Span(n.Spec.PodCIDR, FPodCIDR)
	}
	if len(n.Annotations) > 0 {
		for _, a := range IPAnnotationsNode {
			v := n.Annotations[a.Key]
			if v == "" {
				continue
			}
			for _, one := range strings.Split(v, ",") { // Kube-OVN writes dual stack as "v4,v6"
				if a.Span {
					b.Span(one, FNodeCNI)
				} else {
					b.Host(strings.TrimSpace(one), FNodeCNI)
				}
			}
		}
	}
	return b.Set()
}

func ingressIPs(in *networkingv1.Ingress) *ipx.Set {
	var b ipx.Builder
	for i := range in.Status.LoadBalancer.Ingress {
		b.Addr(in.Status.LoadBalancer.Ingress[i].IP, FLBIngress)
	}
	return b.Set()
}
