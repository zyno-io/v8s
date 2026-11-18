package netkinds

import (
	"github.com/zyno-io/v8s/agent/internal/ingest"
	"github.com/zyno-io/v8s/agent/internal/ipx"
	"k8s.io/apimachinery/pkg/runtime"
)

// IP fields of the on-demand kinds.
var (
	FServiceCIDR = ipx.RegisterField("spec.cidrs", "Service CIDR", false)
	FPool        = ipx.RegisterField("spec.addresses", "Pool range", false)
	FBGPPeer     = ipx.RegisterField("spec.peerAddress", "BGP peer", false)
	FBGPSource   = ipx.RegisterField("spec.sourceAddress", "BGP source address", true)
	FCiliumLB    = ipx.RegisterField("spec.blocks", "LB pool block", false)
	FCiliumPool  = ipx.RegisterField("spec.ipv4.cidrs", "Pod pool CIDR", false)
	FCiliumNode  = ipx.RegisterField("spec.addresses", "Cilium internal IP", false)
	FCiliumPodCI = ipx.RegisterField("spec.ipam.podCIDRs", "Cilium pod CIDR", false)
	FCiliumAlloc = ipx.RegisterField("spec.ipam.pools.allocated", "Allocated pool CIDR", false)
	FCalicoPool  = ipx.RegisterField("spec.cidr", "Pool CIDR", false)
)

func init() {
	register(kind{group: "networking.k8s.io", resource: "servicecidrs", kind: "ServiceCIDR", versions: []string{"v1", "v1beta1"}, order: 60,
		cols: []ingest.Col{ingest.ColStr("cidrs", "CIDRs", 260)}, decode: decodeServiceCIDR})
	register(kind{group: "metallb.io", resource: "ipaddresspools", kind: "IPAddressPool", versions: []string{"v1beta1"}, namespaced: true, order: 70,
		cols: []ingest.Col{ingest.ColStr("addresses", "Addresses", 300), ingest.ColDict("autoAssign", "Auto-assign", 100)}, decode: decodeIPAddressPool})
	register(kind{group: "metallb.io", resource: "bgppeers", kind: "BGPPeer", versions: []string{"v1beta2", "v1beta1"}, namespaced: true, order: 90,
		cols: []ingest.Col{ingest.ColStr("peerAddress", "Peer address", 160), ingest.ColInt("peerASN", "Peer ASN", 90), ingest.ColInt("myASN", "My ASN", 90)}, decode: decodeBGPPeer})
	register(kind{group: "cilium.io", resource: "ciliumloadbalancerippools", kind: "CiliumLoadBalancerIPPool", versions: []string{"v2", "v2alpha1"}, order: 71,
		cols: []ingest.Col{ingest.ColStr("blocks", "Blocks", 300), ingest.ColDict("disabled", "Disabled", 90)}, decode: decodeCiliumLBPool})
	register(kind{group: "cilium.io", resource: "ciliumpodippools", kind: "CiliumPodIPPool", versions: []string{"v2alpha1", "v2"}, order: 96,
		cols: []ingest.Col{ingest.ColStr("cidrs", "CIDRs", 300)}, decode: decodeCiliumPodIPPool})
	register(kind{group: "cilium.io", resource: "ciliumnodes", kind: "CiliumNode", versions: []string{"v2"}, order: 95,
		cols: []ingest.Col{ingest.ColStr("ciliumIP", "Cilium IP", 140), ingest.ColStr("podCIDRs", "Pod CIDRs", 220)}, decode: decodeCiliumNode})
	register(kind{group: "crd.projectcalico.org", resource: "ippools", kind: "IPPool", versions: []string{"v1"}, order: 97,
		cols: []ingest.Col{ingest.ColStr("cidr", "CIDR", 200), ingest.ColDict("encapsulation", "Encapsulation", 120), ingest.ColDict("disabled", "Disabled", 90)}, decode: decodeCalicoPool})
}

func decodeServiceCIDR(raw []byte) (runtime.Object, error) {
	var w struct {
		meta
		Spec struct {
			CIDRs []string `json:"cidrs"`
		} `json:"spec"`
	}
	if err := decodeInto(raw, &w); err != nil {
		return nil, err
	}
	var b ipx.Builder
	for _, c := range w.Spec.CIDRs {
		b.Span(c, FServiceCIDR)
	}
	set := b.Set()
	r := w.newRow(1)
	r.PutS(spanCell(set))
	r.SetIPs(set)
	return w.obj(r), nil
}

func decodeIPAddressPool(raw []byte) (runtime.Object, error) {
	var w struct {
		meta
		Spec struct {
			Addresses  []string `json:"addresses"`
			AutoAssign *bool    `json:"autoAssign"`
		} `json:"spec"`
	}
	if err := decodeInto(raw, &w); err != nil {
		return nil, err
	}
	var b ipx.Builder
	for _, a := range w.Spec.Addresses {
		b.Span(a, FPool) // CIDR or "a-b", IPv4 or IPv6
	}
	set := b.Set()
	r := w.newRow(2)
	r.PutS(spanCell(set))
	if w.Spec.AutoAssign == nil {
		r.PutS("true") // MetalLB's default
	} else {
		r.PutS(boolCell(w.Spec.AutoAssign))
	}
	r.SetIPs(set)
	return w.obj(r), nil
}

func decodeBGPPeer(raw []byte) (runtime.Object, error) {
	var w struct {
		meta
		Spec struct {
			PeerAddress   string `json:"peerAddress"`
			SourceAddress string `json:"sourceAddress"`
			PeerASN       int64  `json:"peerASN"`
			MyASN         int64  `json:"myASN"`
		} `json:"spec"`
	}
	if err := decodeInto(raw, &w); err != nil {
		return nil, err
	}
	var b ipx.Builder
	b.Addr(w.Spec.PeerAddress, FBGPPeer)
	b.Addr(w.Spec.SourceAddress, FBGPSource)
	r := w.newRow(3)
	r.PutS(w.Spec.PeerAddress)
	r.PutN(w.Spec.PeerASN)
	r.PutN(w.Spec.MyASN)
	r.SetIPs(b.Set())
	return w.obj(r), nil
}

func decodeCiliumLBPool(raw []byte) (runtime.Object, error) {
	var w struct {
		meta
		Spec struct {
			Blocks []struct {
				CIDR  string `json:"cidr"`
				Start string `json:"start"`
				Stop  string `json:"stop"`
			} `json:"blocks"`
			Cidrs []struct { // legacy (cilium.io/v2alpha1 before blocks)
				CIDR string `json:"cidr"`
			} `json:"cidrs"`
			Disabled *bool `json:"disabled"`
		} `json:"spec"`
	}
	if err := decodeInto(raw, &w); err != nil {
		return nil, err
	}
	var b ipx.Builder
	for _, bl := range w.Spec.Blocks {
		if bl.CIDR != "" {
			b.Span(bl.CIDR, FCiliumLB)
		} else {
			b.Pair(bl.Start, bl.Stop, FCiliumLB)
		}
	}
	for _, c := range w.Spec.Cidrs {
		b.Span(c.CIDR, FCiliumLB)
	}
	set := b.Set()
	r := w.newRow(2)
	r.PutS(spanCell(set))
	r.PutS(boolCell(w.Spec.Disabled))
	r.SetIPs(set)
	return w.obj(r), nil
}

func decodeCiliumPodIPPool(raw []byte) (runtime.Object, error) {
	var w struct {
		meta
		Spec struct {
			IPv4 struct {
				CIDRs []string `json:"cidrs"`
			} `json:"ipv4"`
			IPv6 struct {
				CIDRs []string `json:"cidrs"`
			} `json:"ipv6"`
		} `json:"spec"`
	}
	if err := decodeInto(raw, &w); err != nil {
		return nil, err
	}
	var b ipx.Builder
	for _, c := range w.Spec.IPv4.CIDRs {
		b.Span(c, FCiliumPool)
	}
	for _, c := range w.Spec.IPv6.CIDRs {
		b.Span(c, FCiliumPool)
	}
	set := b.Set()
	r := w.newRow(1)
	r.PutS(spanCell(set))
	r.SetIPs(set)
	return w.obj(r), nil
}

func decodeCiliumNode(raw []byte) (runtime.Object, error) {
	var w struct {
		meta
		Spec struct {
			Addresses []struct {
				Type string `json:"type"`
				IP   string `json:"ip"`
			} `json:"addresses"`
			IPAM struct {
				PodCIDRs []string `json:"podCIDRs"`
				Pools    struct {
					Allocated []struct {
						CIDRs []string `json:"cidrs"`
					} `json:"allocated"`
				} `json:"pools"`
			} `json:"ipam"`
		} `json:"spec"`
	}
	if err := decodeInto(raw, &w); err != nil {
		return nil, err
	}
	var b ipx.Builder
	ciliumIP := ""
	for _, a := range w.Spec.Addresses {
		if a.Type == "CiliumInternalIP" { // InternalIP and ExternalIP duplicate the Node
			b.Addr(a.IP, FCiliumNode)
			if ciliumIP == "" {
				ciliumIP = a.IP
			}
		}
	}
	for _, c := range w.Spec.IPAM.PodCIDRs {
		b.Span(c, FCiliumPodCI)
	}
	for _, p := range w.Spec.IPAM.Pools.Allocated {
		for _, c := range p.CIDRs {
			b.Span(c, FCiliumAlloc)
		}
	}
	var pc ipx.Builder
	for _, c := range w.Spec.IPAM.PodCIDRs {
		pc.Span(c, FCiliumPodCI)
	}
	r := w.newRow(2)
	r.PutS(ciliumIP)
	r.PutS(spanCell(pc.Set()))
	r.SetIPs(b.Set())
	return w.obj(r), nil
}

func decodeCalicoPool(raw []byte) (runtime.Object, error) {
	var w struct {
		meta
		Spec struct {
			CIDR      string `json:"cidr"`
			IPIPMode  string `json:"ipipMode"`
			VXLANMode string `json:"vxlanMode"`
			Disabled  *bool  `json:"disabled"`
		} `json:"spec"`
	}
	if err := decodeInto(raw, &w); err != nil {
		return nil, err
	}
	var b ipx.Builder
	b.Span(w.Spec.CIDR, FCalicoPool)
	enc := "None"
	switch {
	case w.Spec.IPIPMode != "" && w.Spec.IPIPMode != "Never":
		enc = "IPIP"
	case w.Spec.VXLANMode != "" && w.Spec.VXLANMode != "Never":
		enc = "VXLAN"
	}
	r := w.newRow(3)
	r.PutS(w.Spec.CIDR)
	r.PutS(enc)
	r.PutS(boolCell(w.Spec.Disabled))
	r.SetIPs(b.Set())
	return w.obj(r), nil
}
