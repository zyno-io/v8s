package flux

import (
	"regexp"
	"strings"
	"time"

	"github.com/zyno-io/v8s/agent/internal/ingest"
	"k8s.io/apimachinery/pkg/runtime"
)

// Cell indexes (after cluster, name, namespace); identical for every Flux kind.
const (
	CellReady = iota
	CellSuspended
	CellRevision
	CellSource
	CellInterval
	CellLastReconcile
	CellMessage
	CellRequestedAt
	CellLastHandled
	CellDependsOn
	numCells
)

const maxMessage = 240

func cols() []ingest.Col {
	return []ingest.Col{
		ingest.ColCluster(), ingest.ColName(), ingest.ColNS(),
		ingest.ColDict("ready", "Ready", 150),
		ingest.ColDict("suspended", "Suspended", 90),
		ingest.ColStr("revision", "Revision", 200),
		ingest.ColStr("source", "Source", 220),
		ingest.ColStr("interval", "Interval", 80),
		ingest.ColTimeCell("lastReconcile", "Last reconcile", 110),
		ingest.ColStr("message", "Message", 360),
		ingest.ColStr("requestedAt", "Requested", 160),
		ingest.ColStr("lastHandled", "Handled", 160),
		ingest.ColStr("dependsOn", "Depends on", 220),
		ingest.ColTone(), ingest.ColAge(),
	}
}

func newModule(group, version, resource, kind string, short []string) *ingest.Module {
	m := &ingest.Module{
		GVR: group + "/" + version + "/" + resource, Kind: kind, Namespaced: true, Short: short,
		Curated: true, Optional: true, Tier0: true, NoWatchList: true,
		Cols:       cols(),
		NewAPI:     ingest.NewPartialAPI(Decode),
		DecodeJSON: Decode,
	}
	m.ProjectObj = func(o runtime.Object) *ingest.Row {
		if x, ok := o.(*Object); ok {
			return Project(x)
		}
		return nil
	}
	m.Each = ingest.PartialEach(m)
	return m
}

var shaRe = regexp.MustCompile(`([0-9a-f]{7})[0-9a-f]{5,}`)

// ShortRevision keeps the branch/tag part and the first 7 hex chars of any sha.
func ShortRevision(r string) string { return shaRe.ReplaceAllString(r, "$1") }

func cond(o *Object, typ string) *Condition {
	for i := range o.Conditions {
		if o.Conditions[i].Type == typ {
			return &o.Conditions[i]
		}
	}
	return nil
}

func readyText(o *Object) string {
	c := cond(o, "Ready")
	if c == nil || c.Status == "" {
		return "Unknown"
	}
	if c.Reason != "" {
		return c.Status + " · " + c.Reason
	}
	return c.Status
}

// Tone computes the status tone: suspended is neutral regardless, Stalled danger, Ready success,
// not-ready danger unless progressing, anything else warning.
func Tone(o *Object) uint8 {
	if o.Suspend {
		return ingest.ToneNeutral
	}
	if s := cond(o, "Stalled"); s != nil && s.Status == "True" {
		return ingest.ToneDanger
	}
	c := cond(o, "Ready")
	if c == nil {
		return ingest.ToneWarning
	}
	switch c.Status {
	case "True":
		return ingest.ToneSuccess
	case "False":
		if r := cond(o, "Reconciling"); r != nil && r.Status == "True" {
			return ingest.ToneWarning
		}
		switch c.Reason {
		case "Progressing", "DependencyNotReady", "Reconciling":
			return ingest.ToneWarning
		}
		return ingest.ToneDanger
	}
	return ingest.ToneWarning
}

func sourceText(o *Object) string {
	if o.Source == nil || o.Source.Name == "" {
		return ""
	}
	if o.Source.Namespace != "" && o.Source.Namespace != o.Namespace {
		return o.Source.Kind + "/" + o.Source.Namespace + "/" + o.Source.Name
	}
	return o.Source.Kind + "/" + o.Source.Name
}

func dependsText(o *Object) string {
	if len(o.DependsOn) == 0 {
		return ""
	}
	parts := make([]string, 0, len(o.DependsOn))
	for _, d := range o.DependsOn {
		ns := d.Namespace
		if ns == "" {
			ns = o.Namespace
		}
		parts = append(parts, ns+"/"+d.Name)
	}
	return strings.Join(parts, ",")
}

func revisionOf(o *Object) string {
	switch {
	case o.Applied != "":
		return ShortRevision(o.Applied)
	case o.ArtifactRev != "":
		return ShortRevision(o.ArtifactRev)
	case o.ChartVersion != "":
		return o.ChartVersion
	}
	return ""
}

func lastReconcileMs(o *Object) int64 {
	if o.LastHandled != "" {
		if t, err := time.Parse(time.RFC3339Nano, o.LastHandled); err == nil {
			return t.UnixMilli()
		}
	}
	if c := cond(o, "Ready"); c != nil && !c.LastTransitionTime.IsZero() {
		return c.LastTransitionTime.UnixMilli()
	}
	return 0
}

func truncMsg(s string) string {
	if len(s) <= maxMessage {
		return s
	}
	return strings.ToValidUTF8(s[:maxMessage], "") + "..."
}

// Project builds the row of one Flux object.
func Project(o *Object) *ingest.Row {
	var created int64
	if !o.CreationTimestamp.IsZero() {
		created = o.CreationTimestamp.UnixMilli()
	}
	r := ingest.NewRow(string(o.UID), o.Name, o.ResourceVersion, o.Namespace, created, o.DeletionTimestamp != nil, numCells)
	susp := ""
	if o.Suspend {
		susp = "true"
	}
	msg := ""
	if c := cond(o, "Ready"); c != nil {
		msg = truncMsg(c.Message)
	}
	r.PutS(readyText(o))
	r.PutS(susp)
	r.PutS(revisionOf(o))
	r.PutS(sourceText(o))
	r.PutS(o.Interval)
	r.PutN(lastReconcileMs(o))
	r.PutS(msg)
	r.PutS(o.RequestedAt)
	r.PutS(o.LastHandled)
	r.PutS(dependsText(o))
	r.SetTone(Tone(o))
	r.SetFacts(factsOf(o))
	return r
}
