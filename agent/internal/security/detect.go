package security

import (
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/zyno-io/v8s/agent/internal/ingest"
	"github.com/zyno-io/v8s/agent/internal/problems"
	"github.com/zyno-io/v8s/agent/pkg/protocol"
)

// The vulnerability, misconfiguration, secret and RBAC problem classes are scanner-agnostic: each scanner package
// registers an adapter per report kind (a GR) that turns a row into the normalized record, and these detectors
// turn the records into problems tagged with the scanner as their source (docs/integrations.md).

// VulnInfo is one report's vulnerability counts for one image of one workload.
type VulnInfo struct {
	Source, Repository, Tag              string
	Critical, High, Medium, Low, Unknown int
	Workload                             WorkloadRef
	Since                                int64 // unix ms (report creation)
}

// ConfigInfo is one resource's failing configuration checks by severity (also used for RBAC and infra assessments).
type ConfigInfo struct {
	Source                      string
	Subject                     WorkloadRef
	Critical, High, Medium, Low int
	Since                       int64
}

// SecretInfo is one container's exposed secrets.
type SecretInfo struct {
	Source    string
	Subject   WorkloadRef
	Container string
	Count     int
	Since     int64
}

// Adapters read a row of one report kind. Nil: the row says nothing (wrong kind or nothing to report).
type (
	VulnAdapter   func(c *problems.Ctx, k problems.Key, r *ingest.Row) *VulnInfo
	ConfigAdapter func(c *problems.Ctx, k problems.Key, r *ingest.Row) *ConfigInfo
	SecretAdapter func(c *problems.Ctx, k problems.Key, r *ingest.Row) *SecretInfo
)

var ad = struct {
	sync.RWMutex
	vuln   map[string]VulnAdapter
	config map[string]ConfigAdapter
	secret map[string]SecretAdapter
	rbac   map[string]ConfigAdapter
}{vuln: map[string]VulnAdapter{}, config: map[string]ConfigAdapter{}, secret: map[string]SecretAdapter{}, rbac: map[string]ConfigAdapter{}}

var classes = map[string]*problems.Detector{}

func addInput(class, gr string) {
	d := classes[class]
	d.Optional = append(d.Optional, gr)
	for _, g := range d.Inputs {
		if g == gr {
			return
		}
	}
	d.Inputs = append(d.Inputs, gr)
}

// RegisterVuln registers the adapter of a vulnerability report kind (gr as problems.GR) for vuln.critical and vuln.high.
func RegisterVuln(gr string, a VulnAdapter) {
	ad.Lock()
	ad.vuln[gr] = a
	ad.Unlock()
	addInput("vuln.critical", gr)
	addInput("vuln.high", gr)
}

// RegisterConfig registers the adapter of a configuration scan kind for misconfig.critical and misconfig.high.
func RegisterConfig(gr string, a ConfigAdapter) {
	ad.Lock()
	ad.config[gr] = a
	ad.Unlock()
	addInput("misconfig.critical", gr)
	addInput("misconfig.high", gr)
}

// RegisterSecret registers the adapter of an exposed-secret report kind for secret.exposed.
func RegisterSecret(gr string, a SecretAdapter) {
	ad.Lock()
	ad.secret[gr] = a
	ad.Unlock()
	addInput("secret.exposed", gr)
}

// RegisterRBAC registers the adapter of an RBAC assessment kind for rbac.high.
func RegisterRBAC(gr string, a ConfigAdapter) {
	ad.Lock()
	ad.rbac[gr] = a
	ad.Unlock()
	addInput("rbac.high", gr)
}

// NormalizeWorkload rolls a ReplicaSet up to its Deployment (the pod-template hash is the last name segment).
func NormalizeWorkload(kind, ns, name string) WorkloadRef {
	if kind == "ReplicaSet" {
		if i := strings.LastIndexByte(name, '-'); i > 0 {
			return WorkloadRef{Kind: "Deployment", NS: ns, Name: name[:i]}
		}
	}
	return WorkloadRef{Kind: kind, NS: ns, Name: name}
}

func subjectOf(w WorkloadRef) problems.Subject {
	g, gvr := KindGVR(w.Kind)
	return problems.Subject{Group: g, Kind: w.Kind, NS: w.NS, Name: w.Name, GVR: gvr}
}

func sourceTitle(s string) string {
	switch s {
	case protocol.SourceKubescape:
		return "Kubescape"
	}
	return "Trivy"
}

// vulnLabel packs the counts into Draft.Label for VulnAggregate (the engine hands members over as drafts).
func vulnLabel(v *VulnInfo) string {
	return fmt.Sprintf("%d,%d,%d,%d,%d", v.Critical, v.High, v.Medium, v.Low, v.Unknown)
}

func parseVulnLabel(s string) (c, h, m, l, u int) {
	fmt.Sscanf(s, "%d,%d,%d,%d,%d", &c, &h, &m, &l, &u)
	return
}

func vulnClass(class string, def bool, sev problems.Severity, title string, level func(v *VulnInfo) int) *problems.Detector {
	d := &problems.Detector{Class: class, Family: problems.FamVulns, Default: def, Severity: sev, Title: title,
		Evaluate: func(c *problems.Ctx, k problems.Key, r *ingest.Row) []problems.Draft {
			ad.RLock()
			a := ad.vuln[k.GR]
			ad.RUnlock()
			if a == nil {
				return nil
			}
			v := a(c, k, r)
			if v == nil || level(v) == 0 {
				return nil
			}
			img := ImageRef(v.Repository, v.Tag)
			return []problems.Draft{{
				Subject: problems.Subject{Kind: "Image", Name: img}, Disc: v.Source, Source: v.Source, Since: v.Since, Label: vulnLabel(v),
				Count: level(v), Title: img,
				Evidence: []problems.Ref{{Kind: v.Workload.Kind, NS: v.Workload.NS, Name: v.Workload.Name}},
			}}
		},
		Aggregate: vulnAggregate(level)}
	classes[class] = d
	problems.Register(d)
	return d
}

// vulnAggregate merges the reports of one image into one problem: "nginx:1.25 · 4 critical, 31 high · 12 workloads".
func vulnAggregate(level func(v *VulnInfo) int) func(c *problems.Ctx, ms []problems.Draft) problems.Draft {
	return func(c *problems.Ctx, ms []problems.Draft) problems.Draft {
		out := ms[0]
		var crit, high, med, low, unk int
		seen := map[problems.Ref]struct{}{}
		var refs []problems.Ref
		out.Since = 0
		for i := range ms {
			cr, h, m, l, u := parseVulnLabel(ms[i].Label)
			crit, high, med, low, unk = max(crit, cr), max(high, h), max(med, m), max(low, l), max(unk, u)
			for _, e := range ms[i].Evidence {
				if _, dup := seen[e]; !dup {
					seen[e] = struct{}{}
					refs = append(refs, e)
				}
			}
			if s := ms[i].Since; s != 0 && (out.Since == 0 || s < out.Since) {
				out.Since = s
			}
		}
		sort.Slice(refs, func(i, j int) bool {
			a, b := refs[i], refs[j]
			return a.NS+"/"+a.Kind+"/"+a.Name < b.NS+"/"+b.Kind+"/"+b.Name
		})
		out.Count = level(&VulnInfo{Critical: crit, High: high, Medium: med, Low: low, Unknown: unk})
		wl := fmt.Sprintf("%d workloads", len(refs))
		if len(refs) == 1 {
			wl = "1 workload"
		}
		out.Title = fmt.Sprintf("%s · %d critical, %d high · %s", ms[0].Subject.Name, crit, high, wl)
		out.Message = fmt.Sprintf("%s: %d medium, %d low, %d unknown", sourceTitle(out.Source), med, low, unk)
		if len(refs) > 25 {
			refs = refs[:25]
		}
		out.Evidence = refs
		out.Label = "vulnerabilities"
		return out
	}
}

func configClass(class string, def bool, sev problems.Severity, title string, ad_ func() map[string]ConfigAdapter, level func(v *ConfigInfo) int, what string) *problems.Detector {
	d := &problems.Detector{Class: class, Family: problems.FamVulns, Default: def, Severity: sev, Title: title,
		Evaluate: func(c *problems.Ctx, k problems.Key, r *ingest.Row) []problems.Draft {
			ad.RLock()
			a := ad_()[k.GR]
			ad.RUnlock()
			if a == nil {
				return nil
			}
			v := a(c, k, r)
			if v == nil || level(v) == 0 {
				return nil
			}
			n := level(v)
			return []problems.Draft{{Subject: subjectOf(v.Subject), Disc: v.Source, Source: v.Source, Since: v.Since, Count: n,
				Title:   fmt.Sprintf("%s %s: %d %s", v.Subject.Kind, v.Subject.Name, n, what),
				Message: fmt.Sprintf("%s: %d critical, %d high, %d medium, %d low", sourceTitle(v.Source), v.Critical, v.High, v.Medium, v.Low)}}
		}}
	classes[class] = d
	problems.Register(d)
	return d
}

func init() {
	vulnClass("vuln.critical", true, problems.SevError, "Images with critical vulnerabilities", func(v *VulnInfo) int { return v.Critical })
	vulnClass("vuln.high", false, problems.SevWarning, "Images with high vulnerabilities", func(v *VulnInfo) int { return v.High })
	configClass("misconfig.critical", false, problems.SevError, "Critical misconfigurations", func() map[string]ConfigAdapter { return ad.config },
		func(v *ConfigInfo) int { return v.Critical }, "critical misconfigurations")
	configClass("misconfig.high", false, problems.SevWarning, "High misconfigurations", func() map[string]ConfigAdapter { return ad.config },
		func(v *ConfigInfo) int { return v.High }, "high misconfigurations")
	configClass("rbac.high", false, problems.SevWarning, "High-severity RBAC findings", func() map[string]ConfigAdapter { return ad.rbac },
		func(v *ConfigInfo) int { return v.Critical + v.High }, "critical or high RBAC findings")

	d := &problems.Detector{Class: "secret.exposed", Family: problems.FamVulns, Default: true, Severity: problems.SevError, Title: "Exposed secrets in images",
		Evaluate: func(c *problems.Ctx, k problems.Key, r *ingest.Row) []problems.Draft {
			ad.RLock()
			a := ad.secret[k.GR]
			ad.RUnlock()
			if a == nil {
				return nil
			}
			v := a(c, k, r)
			if v == nil || v.Count == 0 {
				return nil
			}
			return []problems.Draft{{Subject: subjectOf(v.Subject), Disc: v.Source + "/" + v.Container, Source: v.Source, Since: v.Since, Count: v.Count,
				Title:   fmt.Sprintf("%s %s: %d exposed secret(s) in container %s", v.Subject.Kind, v.Subject.Name, v.Count, v.Container),
				Message: sourceTitle(v.Source) + " found secrets baked into the image"}}
		}}
	classes["secret.exposed"] = d
	problems.Register(d)
}
