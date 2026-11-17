// Package problems is the problems engine (docs/problems.md): detectors registered by modules evaluate the
// agent's projected stores incrementally (a dirty set every 250 ms, a deadline heap for time-based
// conditions, grace and hysteresis), roll pod problems up to their workloads, and publish the result as the
// synthetic resource "v8s/problems" through the ordinary view engine.
package problems

import (
	"crypto/sha256"
	"encoding/hex"
	"sort"
	"sync"
	"time"

	"github.com/zyno-io/v8s/agent/internal/ingest"
)

// Severity of a problem. The zero value means "the detector's default".
type Severity uint8

const (
	SevDefault  Severity = 0
	SevCritical Severity = 1
	SevError    Severity = 2
	SevWarning  Severity = 3
	SevInfo     Severity = 4
)

// Rank orders severities for sorting: 0 critical ... 3 info.
func (s Severity) Rank() int { return int(s) - 1 }

func (s Severity) String() string {
	switch s {
	case SevCritical:
		return "critical"
	case SevError:
		return "error"
	case SevWarning:
		return "warning"
	case SevInfo:
		return "info"
	}
	return ""
}

// Tone maps the severity to a row tone.
func (s Severity) Tone() uint8 {
	switch s {
	case SevCritical, SevError:
		return ingest.ToneDanger
	case SevWarning:
		return ingest.ToneWarning
	}
	return ingest.ToneInfo
}

// Families group classes in the UI's filter chips.
const (
	FamPods      = "pods"
	FamWorkloads = "workloads"
	FamJobs      = "jobs"
	FamNodes     = "nodes"
	FamNetwork   = "network"
	FamStorage   = "storage"
	FamFlux      = "flux"
	FamHelm      = "helm"
	FamPolicy    = "policy"
	FamVulns     = "vulnerabilities"
	FamFlows     = "flows"
	FamPlatform  = "platform"
)

// Key addresses one object the engine holds. GR is "<group>/<resource>" without the version ("pods",
// "apps/deployments", "helm.toolkit.fluxcd.io/helmreleases"), so a kind is the same key whatever version a
// cluster serves.
type Key struct{ GR, NS, Name string }

// GR builds the version-less key of a GVR ("v1/pods" -> "pods", "apps/v1/deployments" -> "apps/deployments").
func GR(gvr string) string {
	g, _, r, err := ingest.ParseGVR(gvr)
	if err != nil {
		return gvr
	}
	if g == "" {
		return r
	}
	return g + "/" + r
}

// Subject is the object a problem is about.
type Subject struct {
	Group, Kind, NS, Name, UID string
	// GVR ("apps/v1/deployments") lets problems.evidence fetch the live object; empty for synthetic subjects (an image).
	GVR string
}

func (s Subject) key() string { return s.Group + "/" + s.Kind + "/" + s.NS + "/" + s.Name }

// Ref is a reference to related evidence: an object (with a short note), never a copy of it.
type Ref struct {
	Group string `json:"group,omitempty" msgpack:"group,omitempty"`
	Kind  string `json:"kind" msgpack:"kind"`
	NS    string `json:"namespace,omitempty" msgpack:"namespace,omitempty"`
	Name  string `json:"name" msgpack:"name"`
	Note  string `json:"note,omitempty" msgpack:"note,omitempty"`
}

// Draft is one detector finding about one object.
type Draft struct {
	// Subject is what the finding is about (a pod). Owner, when set, is the identity the problem rolls up to
	// (the pod's workload): the problem id hashes the owner, so pod churn does not create new problems.
	Subject Subject
	Owner   *Subject
	// Disc is the class-specific discriminator of the id (container name, image repository:tag, ...).
	Disc     string
	Severity Severity
	Title    string
	Message  string
	// Label is a short name of the condition ("CrashLoopBackOff"), used in rolled-up titles.
	Label string
	// Since is when the condition began (unix ms), from the evidence. 0: the engine uses when it first saw it.
	Since int64
	Count int
	// Source names the origin of the finding ("trivy", "kubescape", "kyverno", "hubble").
	Source string
	// Grace delays raising: the problem appears once Since+Grace has passed (a deadline wakes the key then).
	Grace time.Duration
	// TTL ends the finding Since+TTL after it began (oomkilled within 1 h).
	TTL time.Duration
	// Wake lists times at which the key is evaluated again although nothing changed (a title or severity that
	// depends on the clock: a certificate nearing expiry). Past times are ignored.
	Wake []time.Time
	// Evidence refs (related objects).
	Evidence []Ref
}

// Ctx is what detectors read: the engine's tables and indexes of one cluster. It is only valid during an
// Evaluate / Dependents call.
type Ctx struct{ e *Engine }

// Detector is one problem class.
type Detector struct {
	Class   string
	Family  string
	Default bool // enabled by default
	// Severity is the default severity of drafts that do not set one.
	Severity Severity
	// Title is the human name of the class ("CrashLoopBackOff pods").
	Title string
	// Inputs are the GRs whose keys Evaluate is called for. Triggers are the GRs whose changes call Dependents
	// (to find the Inputs keys to re-evaluate: joins). A nil Inputs makes the class external: findings come
	// from Engine.Inject (cluster.unreachable, the Hubble classes).
	Inputs   []string
	Triggers []string
	// Optional lists GRs of Inputs or Triggers that do not hold the detector back while they are not watched (a
	// class fed by several report kinds of which a cluster serves only some). A watched optional kind must still
	// be synced before the detector runs.
	Optional []string
	// Evaluate returns every finding about key k (row is its current row, never nil). It must be a pure function
	// of the tables, the indexes and c.Now().
	Evaluate func(c *Ctx, k Key, row *ingest.Row) []Draft
	// Dependents returns keys of Inputs to re-evaluate because key k of a Trigger GR changed (old or new may be nil).
	Dependents func(c *Ctx, k Key, old, new *ingest.Row) []Key
	// Aggregate merges the drafts that share one problem id (rollups, per-image counts) into the one problem.
	// Nil: the first member (ordered by subject) is the problem. members is never empty.
	Aggregate func(c *Ctx, members []Draft) Draft
	// Gate names a GR whose presence in discovery turns the class on ("cert-manager is present"); "" none.
	Gate string
}

func (d *Detector) severity(dr *Draft) Severity {
	if dr.Severity != SevDefault {
		return dr.Severity
	}
	if d.Severity != SevDefault {
		return d.Severity
	}
	return SevWarning
}

// ---- registry -----------------------------------------------------------------------------

var registry = struct {
	sync.RWMutex
	list  []*Detector
	byCls map[string]*Detector
}{byCls: map[string]*Detector{}}

// Register adds a detector (at init time, from the module that owns the class).
func Register(d *Detector) {
	registry.Lock()
	defer registry.Unlock()
	if _, dup := registry.byCls[d.Class]; dup {
		panic("problems: duplicate class " + d.Class)
	}
	registry.byCls[d.Class] = d
	registry.list = append(registry.list, d)
	sort.SliceStable(registry.list, func(i, j int) bool { return registry.list[i].Class < registry.list[j].Class })
}

// Detectors returns the registered detectors sorted by class.
func Detectors() []*Detector {
	registry.RLock()
	defer registry.RUnlock()
	return append([]*Detector(nil), registry.list...)
}

// Lookup returns the detector of a class.
func Lookup(class string) *Detector {
	registry.RLock()
	defer registry.RUnlock()
	return registry.byCls[class]
}

// ID is the stable problem id: hash(cluster, class, group/kind, namespace, name, discriminator). It hashes
// the subject's name, not its uid, so an object recreated by GitOps keeps its problem and acknowledgement.
func ID(cluster, class string, s Subject, disc string) string {
	h := sha256.New()
	for _, p := range []string{cluster, class, s.Group + "/" + s.Kind, s.NS, s.Name, disc} {
		h.Write([]byte(p))
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil)[:12])
}

func msToTime(ms int64) time.Time {
	if ms <= 0 {
		return time.Time{}
	}
	return time.UnixMilli(ms)
}
