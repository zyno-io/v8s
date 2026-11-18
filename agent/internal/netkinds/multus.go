package netkinds

import (
	"encoding/json"
	"strings"

	"github.com/zyno-io/v8s/agent/internal/ingest"
	"github.com/zyno-io/v8s/agent/internal/ipx"
	"k8s.io/apimachinery/pkg/runtime"
)

// IP fields of NetworkAttachmentDefinitions (their spec.config is a CNI configuration in a string).
var (
	FNADRange   = ipx.RegisterField("spec.config (ipam range)", "IPAM range", false)
	FNADStatic  = ipx.RegisterField("spec.config (ipam addresses)", "Static address", false)
	FNADGateway = ipx.RegisterField("spec.config (ipam gateway)", "Gateway", true)
)

const maxNADConfig = 64 << 10

func init() {
	register(kind{group: "k8s.cni.cncf.io", resource: "network-attachment-definitions", kind: "NetworkAttachmentDefinition", versions: []string{"v1"},
		namespaced: true, short: []string{"net-attach-def"}, order: 80,
		cols:   []ingest.Col{ingest.ColDict("cni", "CNI", 120), ingest.ColDict("ipam", "IPAM", 120), ingest.ColStr("ranges", "Ranges", 280)},
		decode: decodeNAD})
}

// cniConf is the part of a CNI configuration (or conflist) that carries addresses.
type cniConf struct {
	Type    string    `json:"type"`
	IPAM    *cniIPAM  `json:"ipam"`
	Plugins []cniConf `json:"plugins"`
	Subnets string    `json:"subnets"` // ovn-k8s: comma-separated
}

type cniRange struct {
	Subnet     string `json:"subnet"`
	RangeStart string `json:"rangeStart"`
	RangeEnd   string `json:"rangeEnd"`
	Gateway    string `json:"gateway"`
}

type cniIPAM struct {
	Type       string       `json:"type"`
	Subnet     string       `json:"subnet"`
	RangeStart string       `json:"rangeStart"`
	RangeEnd   string       `json:"rangeEnd"`
	Gateway    string       `json:"gateway"`
	Ranges     [][]cniRange `json:"ranges"`
	Addresses  []struct {
		Address string `json:"address"`
		Gateway string `json:"gateway"`
	} `json:"addresses"`
	Range       string `json:"range"` // whereabouts: a CIDR or "a-b"
	RangeStartW string `json:"range_start"`
	RangeEndW   string `json:"range_end"`
	IPRanges    []struct {
		Range      string `json:"range"`
		RangeStart string `json:"range_start"`
		RangeEnd   string `json:"range_end"`
	} `json:"ipRanges"`
}

func (c *cniIPAM) collect(b *ipx.Builder) {
	// host-local: the subnet, or an explicit start-end, plus the gateway; also per entry of ranges[][]
	one := func(r cniRange) {
		if r.Subnet != "" {
			b.Span(r.Subnet, FNADRange)
		}
		if r.RangeStart != "" && r.RangeEnd != "" {
			b.Pair(r.RangeStart, r.RangeEnd, FNADRange)
		}
		b.Addr(r.Gateway, FNADGateway)
	}
	one(cniRange{c.Subnet, c.RangeStart, c.RangeEnd, c.Gateway})
	for _, set := range c.Ranges {
		for _, r := range set {
			one(r)
		}
	}
	// static
	for _, a := range c.Addresses {
		b.Host(a.Address, FNADStatic)
		b.Addr(a.Gateway, FNADGateway)
	}
	// whereabouts: range (CIDR, "a-b" or "a-b/n") with optional range_start and range_end; exclude is ignored
	pair := func(rng, start, end string) {
		if start != "" && end != "" {
			b.Pair(start, end, FNADRange)
		} else if rng != "" {
			b.Span(rng, FNADRange)
		}
	}
	if c.Range != "" || c.RangeStartW != "" {
		pair(c.Range, c.RangeStartW, c.RangeEndW)
	}
	for _, r := range c.IPRanges {
		pair(r.Range, r.RangeStart, r.RangeEnd)
	}
}

// parseNADConfig reads a NetworkAttachmentDefinition's config string: the addresses it holds, the CNI type
// and the IPAM type. An invalid configuration yields no tokens and ok=false; it is never an error, so the row
// still exists.
func parseNADConfig(cfg string) (set *ipx.Set, cni, ipam string, ok bool) {
	if cfg == "" {
		return nil, "", "", true
	}
	if len(cfg) > maxNADConfig {
		return nil, "", "", false
	}
	var c cniConf
	if json.Unmarshal([]byte(cfg), &c) != nil {
		return nil, "", "", false
	}
	var b ipx.Builder
	cni = c.Type
	if c.IPAM != nil {
		ipam = c.IPAM.Type
		c.IPAM.collect(&b)
	}
	for _, s := range strings.Split(c.Subnets, ",") {
		b.Span(strings.TrimSpace(s), FNADRange)
	}
	var types []string
	for i := range c.Plugins { // a conflist: one level of plugins
		p := &c.Plugins[i]
		if p.Type != "" {
			types = append(types, p.Type)
		}
		if p.IPAM != nil {
			if ipam == "" {
				ipam = p.IPAM.Type
			}
			p.IPAM.collect(&b)
		}
	}
	if cni == "" {
		cni = strings.Join(types, ",")
	}
	return b.Set(), cni, ipam, true
}

func decodeNAD(raw []byte) (runtime.Object, error) {
	var w struct {
		meta
		Spec struct {
			Config string `json:"config"`
		} `json:"spec"`
	}
	if err := decodeInto(raw, &w); err != nil {
		return nil, err
	}
	set, cni, ipam, ok := parseNADConfig(w.Spec.Config)
	r := w.newRow(3)
	r.PutS(cni)
	if !ok {
		ipam = "invalid config"
	}
	r.PutS(ipam)
	r.PutS(spanCell(set))
	r.SetIPs(set)
	return w.obj(r), nil
}
