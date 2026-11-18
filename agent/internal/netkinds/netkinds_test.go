package netkinds

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/zyno-io/v8s/agent/internal/ingest"
	"k8s.io/apimachinery/pkg/runtime"
)

func read(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// toks formats a row's tokens as "Label=value".
func toks(r *ingest.Row) string {
	var out []string
	for _, tk := range r.IPs() {
		out = append(out, tk.Field.Label()+"="+tk.String())
	}
	return strings.Join(out, " ")
}

func rowOf(t *testing.T, dec func([]byte) (runtime.Object, error), file string) *ingest.Row {
	t.Helper()
	o, err := dec(read(t, file))
	if err != nil {
		t.Fatal(err)
	}
	return o.(*Obj).row
}

func TestDecoders(t *testing.T) {
	for _, c := range []struct {
		file   string
		dec    func([]byte) (runtime.Object, error)
		tokens string
		cells  []string // string cells, in column order
	}{
		{"servicecidr.json", decodeServiceCIDR, "Service CIDR=10.96.0.0/12 Service CIDR=2001:db8:42::/112", []string{"10.96.0.0/12,2001:db8:42::/112"}},
		{"metallb_pool.json", decodeIPAddressPool,
			"Pool range=192.168.0.5 Pool range=192.168.100.0/24 Pool range=192.168.200.10-192.168.200.50 Pool range=fd00:200::/120",
			[]string{"192.168.0.5,192.168.100.0/24,192.168.200.10-192.168.200.50,fd00:200::/120", "false"}},
		{"metallb_bgppeer.json", decodeBGPPeer, "BGP peer=192.168.0.1 BGP source address=192.168.0.2", []string{"192.168.0.1"}},
		{"cilium_lbpool.json", decodeCiliumLBPool, "LB pool block=172.18.250.0/24 LB pool block=172.18.251.10-172.18.251.20", []string{"172.18.250.0/24,172.18.251.10-172.18.251.20", "true"}},
		{"cilium_lbpool_legacy.json", decodeCiliumLBPool, "LB pool block=172.19.0.0/24", []string{"172.19.0.0/24", ""}},
		{"cilium_podippool.json", decodeCiliumPodIPPool, "Pod pool CIDR=10.100.0.0/16 Pod pool CIDR=fd00:100::/48", []string{"10.100.0.0/16,fd00:100::/48"}},
		// only CiliumInternalIP: InternalIP duplicates the Node
		{"cilium_node.json", decodeCiliumNode, "Cilium pod CIDR=10.64.2.0/24 Cilium internal IP=10.64.2.1 Allocated pool CIDR=10.100.5.0/24", []string{"10.64.2.1", "10.64.2.0/24"}},
		{"calico_ippool.json", decodeCalicoPool, "Pool CIDR=10.64.0.0/10", []string{"10.64.0.0/10", "VXLAN", "false"}},
	} {
		r := rowOf(t, c.dec, c.file)
		if got := toks(r); got != c.tokens {
			t.Errorf("%s tokens:\n got %s\nwant %s", c.file, got, c.tokens)
		}
		for i, want := range c.cells {
			if got := r.StrCell(i); got != want {
				t.Errorf("%s cell %d = %q, want %q", c.file, i, got, want)
			}
		}
	}
}

func TestBGPPeerNumbers(t *testing.T) {
	r := rowOf(t, decodeBGPPeer, "metallb_bgppeer.json")
	if r.Cell(1).N != 64512 || r.Cell(2).N != 64513 {
		t.Fatalf("asn cells: %d %d", r.Cell(1).N, r.Cell(2).N)
	}
	for _, tk := range r.IPs() {
		if tk.Field == FBGPSource && !tk.Field.Secondary() {
			t.Fatal("the source address ranks as secondary")
		}
	}
}

// nadRow wraps a testdata CNI configuration in a NetworkAttachmentDefinition.
func nadRow(t *testing.T, file string) *ingest.Row {
	t.Helper()
	cfg := string(read(t, file))
	raw, _ := json.Marshal(map[string]any{"apiVersion": "k8s.cni.cncf.io/v1", "kind": "NetworkAttachmentDefinition",
		"metadata": map[string]any{"name": "n", "namespace": "ns", "uid": "u", "resourceVersion": "1"}, "spec": map[string]any{"config": cfg}})
	o, err := decodeNAD(raw)
	if err != nil {
		t.Fatal(err)
	}
	return o.(*Obj).row
}

func TestNADConfigs(t *testing.T) {
	for _, c := range []struct {
		file, tokens, cni, ipam string
	}{
		{"nad_hostlocal_subnet_range.json", "IPAM range=192.168.50.0/24 IPAM range=192.168.50.2-192.168.50.251 Gateway=192.168.50.1", "macvlan", "host-local"},
		{"nad_hostlocal_ranges_dual.json", "IPAM range=192.168.60.0/24 IPAM range=fd00:60::/64 Gateway=fd00:60::1", "sriov", "host-local"},
		{"nad_static.json", "Static address=10.10.0.5 Gateway=10.10.0.1 Static address=fd00:10::5", "bridge", "static"},
		{"nad_whereabouts_range.json", "IPAM range=192.168.51.0/24", "macvlan", "whereabouts"}, // exclude is ignored
		{"nad_whereabouts_start_end.json", "IPAM range=192.168.52.10-192.168.52.20", "macvlan", "whereabouts"},
		{"nad_whereabouts_dash_mask.json", "IPAM range=192.168.53.10-192.168.53.20", "macvlan", "whereabouts"},
		{"nad_whereabouts_ipranges.json", "IPAM range=192.168.54.0/24 IPAM range=fd00:54::/64", "macvlan", "whereabouts"},
		{"nad_conflist.json", "IPAM range=192.168.55.0/24", "macvlan,tuning", "host-local"},
		{"nad_ovn.json", "IPAM range=10.200.0.0/16 IPAM range=fd00:200::/64", "ovn-k8s-cni-overlay", ""},
		{"nad_no_ipam.json", "", "bridge", ""},
		{"nad_invalid.txt", "", "", "invalid config"},
	} {
		r := nadRow(t, c.file)
		// the static file's dual-stack addresses sort v4 first; compare as sets for stable ordering
		got, want := strings.Fields(toks(r)), strings.Fields(c.tokens)
		if fmt.Sprint(sorted(got)) != fmt.Sprint(sorted(want)) {
			t.Errorf("%s tokens:\n got %v\nwant %v", c.file, got, want)
		}
		if r.StrCell(0) != c.cni || r.StrCell(1) != c.ipam {
			t.Errorf("%s: cni=%q ipam=%q, want %q %q", c.file, r.StrCell(0), r.StrCell(1), c.cni, c.ipam)
		}
	}
}

func sorted(s []string) []string {
	out := append([]string(nil), s...)
	for i := range out {
		for j := i + 1; j < len(out); j++ {
			if out[j] < out[i] {
				out[i], out[j] = out[j], out[i]
			}
		}
	}
	return out
}

func TestNADEmptyAndOversize(t *testing.T) {
	if set, _, _, ok := parseNADConfig(""); set != nil || !ok {
		t.Fatal("an empty config is fine and holds nothing")
	}
	if _, _, _, ok := parseNADConfig(`{"type":"x","pad":"` + strings.Repeat("a", maxNADConfig) + `"}`); ok {
		t.Fatal("an oversize config is invalid")
	}
}

func TestModulesAreOnDemandIPSources(t *testing.T) {
	for _, gvr := range []string{
		"networking.k8s.io/v1/servicecidrs", "metallb.io/v1beta1/ipaddresspools", "metallb.io/v1beta2/bgppeers",
		"k8s.cni.cncf.io/v1/network-attachment-definitions", "cilium.io/v2/ciliumloadbalancerippools",
		"cilium.io/v2alpha1/ciliumpodippools", "cilium.io/v2/ciliumnodes", "crd.projectcalico.org/v1/ippools",
	} {
		m := ingest.Lookup(gvr)
		if m == nil {
			t.Fatalf("%s not registered", gvr)
		}
		if !m.IPs || !m.IPOnDemand || m.Tier0 || !m.Optional || !m.Curated || m.IPOrder == 0 {
			t.Errorf("%s: %+v", gvr, m)
		}
		if m.Col("age") == nil && m.Col("createdAt") == nil {
			t.Errorf("%s has no age column", gvr)
		}
	}
	for _, mod := range ingest.Tier0Modules() {
		if mod.IPOnDemand {
			t.Fatalf("%s must never be tier-0", mod.GVR)
		}
	}
}

func TestOtherServedVersionsResolve(t *testing.T) {
	for _, c := range []struct{ group, version, resource, kind string }{
		{"networking.k8s.io", "v1beta1", "servicecidrs", "ServiceCIDR"},
		{"metallb.io", "v1beta1", "bgppeers", "BGPPeer"},
		{"cilium.io", "v2alpha1", "ciliumloadbalancerippools", "CiliumLoadBalancerIPPool"},
		{"cilium.io", "v2", "ciliumpodippools", "CiliumPodIPPool"},
	} {
		m := ingest.ResolveDiscovered(c.group, c.version, c.resource, c.kind, false)
		if m == nil || m.Version != c.version || !m.IPOnDemand {
			t.Errorf("%+v: %+v", c, m)
		}
	}
	if ingest.ResolveDiscovered("metallb.io", "v9", "bgppeers", "BGPPeer", true) != nil {
		t.Error("an unknown version must not resolve")
	}
}

func TestOVNLayer3SubnetsDropTheNodePrefix(t *testing.T) {
	cfg := `{"cniVersion":"1.0.0","type":"ovn-k8s-cni-overlay","topology":"layer3","subnets":"10.128.0.0/16/24,fd00:10:244::/48/64"}`
	set, cni, _, ok := parseNADConfig(cfg)
	if !ok || cni != "ovn-k8s-cni-overlay" {
		t.Fatalf("ok=%v cni=%q", ok, cni)
	}
	if got := spanCell(set); got != "10.128.0.0/16,fd00:10:244::/48" {
		t.Fatalf("ranges = %q", got)
	}
	set, _, _, _ = parseNADConfig(`{"type":"ovn-k8s-cni-overlay","topology":"layer2","subnets":"10.100.200.0/24"}`)
	if spanCell(set) != "10.100.200.0/24" {
		t.Fatalf("layer2: %q", spanCell(set))
	}
}

func TestOddNADConfigsHoldNothing(t *testing.T) {
	for _, cfg := range []string{
		`{"type":"macvlan","ipam":{"type":"host-local","subnet":"usePodCidr"}}`,
		`{"type":"x","subnets":["10.0.0.0/8"]}`,
		`{"type":"x","ipam":{"type":"static","addresses":["10.0.0.1/24"]}}`,
	} {
		if set, _, _, _ := parseNADConfig(cfg); set != nil && len(set.T) != 0 {
			t.Errorf("%s: unexpected tokens %q", cfg, spanCell(set))
		}
	}
}
