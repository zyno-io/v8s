package problems

import (
	"github.com/zyno-io/v8s/agent/internal/ingest"
	"github.com/zyno-io/v8s/agent/pkg/protocol"
)

func sortDefault() []protocol.Sort {
	return []protocol.Sort{{Col: "severityRank"}, {Col: "lastChanged", Desc: true}, {Col: "name"}}
}

// GVR of the synthetic resource the problems are served as.
const GVR = "v8s/v1/problems"

// Cell indexes of the problems module (after cluster, name, namespace).
const (
	CellSeverity = iota
	CellSeverityRank
	CellClass
	CellFamily
	CellKind
	CellTitle
	CellMessage
	CellSince
	CellChanged
	CellCount
	CellSource
	CellAcked
	CellProblemID
	numCells
)

func init() {
	ingest.Register(&ingest.Module{
		GVR: GVR, Kind: "Problem", Namespaced: true, Curated: true, Virtual: true, Synthetic: true,
		DefaultSort: sortDefault(),
		Cols: []ingest.Col{
			ingest.ColCluster(), ingest.ColName(), ingest.ColNS(),
			ingest.ColDict("severity", "Severity", 90),
			ingest.ColInt("severityRank", "Severity rank", 60),
			ingest.ColDict("class", "Class", 150),
			ingest.ColDict("family", "Family", 110),
			ingest.ColDict("kind", "Kind", 120),
			ingest.ColStr("title", "Title", 360),
			ingest.ColStr("message", "Message", 420),
			ingest.ColTimeCell("since", "Since", 90),
			ingest.ColTimeCell("lastChanged", "Last changed", 90),
			ingest.ColInt("count", "Count", 70),
			ingest.ColDict("source", "Source", 90),
			ingest.ColDict("acked", "Acked", 70),
			ingest.ColStr("problemId", "Problem ID", 0),
			ingest.ColTone(),
		},
	})
}
