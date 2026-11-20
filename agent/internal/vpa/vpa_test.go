package vpa

import (
	"fmt"
	"testing"

	"github.com/zyno-io/v8s/agent/internal/ingest"
)

func rowOf(t *testing.T, js string) *ingest.Row {
	t.Helper()
	o, err := decode([]byte(js))
	if err != nil {
		t.Fatal(err)
	}
	return o.(*Obj).row
}

func vpaJSON(mode, conds, recs string) string {
	spec := `"targetRef":{"apiVersion":"apps/v1","kind":"Deployment","name":"api"}`
	if mode != "" {
		spec += fmt.Sprintf(`,"updatePolicy":{"updateMode":%q}`, mode)
	}
	return `{"kind":"VerticalPodAutoscaler","metadata":{"name":"api","namespace":"team-1","uid":"u1","resourceVersion":"5","creationTimestamp":"2026-01-01T00:00:00Z"},
		"spec":{` + spec + `},"status":{"conditions":[` + conds + `],"recommendation":{"containerRecommendations":[` + recs + `]}}}`
}

const twoContainers = `
 {"containerName":"api","target":{"cpu":"250m","memory":"512Mi"},"lowerBound":{"cpu":"100m","memory":"256Mi"},"upperBound":{"cpu":"1","memory":"1Gi"}},
 {"containerName":"sidecar","target":{"cpu":"15m","memory":"33554432"},"lowerBound":{"cpu":"10m","memory":"16Mi"},"upperBound":{"cpu":"0.5","memory":"1500Ki"}}`

func cond(typ string) string { return fmt.Sprintf(`{"type":%q,"status":"True"}`, typ) }

func TestDecodeProject(t *testing.T) {
	r := rowOf(t, vpaJSON("Initial", cond("RecommendationProvided"), twoContainers))
	if r.Name() != "api" || r.NS().Value() != "team-1" {
		t.Fatalf("identity %q %q", r.Name(), r.NS().Value())
	}
	if r.StrCell(0) != "Deployment/api" || r.StrCell(1) != "Initial" || r.StrCell(2) != "Provided" || r.Tone() != ingest.ToneSuccess {
		t.Fatalf("cells %q %q %q tone %d", r.StrCell(0), r.StrCell(1), r.StrCell(2), r.Tone())
	}
	// sums over both containers: CPU in millicores, memory in MiB (floor)
	want := []int64{265, 110, 1500, 544, 272, 1024 + 1}
	for i, w := range want {
		if got := r.Cell(3 + i).N; got != w {
			t.Errorf("cell %d = %d, want %d", 3+i, got, w)
		}
	}
}

func TestModeDefaultsToAuto(t *testing.T) {
	if r := rowOf(t, vpaJSON("", cond("RecommendationProvided"), twoContainers)); r.StrCell(1) != "Auto" {
		t.Fatalf("mode %q", r.StrCell(1))
	}
}

func TestNoRecommendation(t *testing.T) {
	r := rowOf(t, vpaJSON("Auto", cond("NoPodsMatched"), ""))
	if r.StrCell(2) != "NoPodsMatched" || r.Tone() != ingest.ToneWarning {
		t.Fatalf("status %q tone %d", r.StrCell(2), r.Tone())
	}
	for i := 3; i < 9; i++ {
		if r.Cell(i).N != -1 {
			t.Errorf("cell %d = %d, want -1", i, r.Cell(i).N)
		}
	}
	// a recommendation with only CPU leaves memory at -1
	r = rowOf(t, vpaJSON("Auto", cond("RecommendationProvided"), `{"containerName":"a","target":{"cpu":"100m"}}`))
	if r.Cell(3).N != 100 || r.Cell(4).N != -1 || r.Cell(6).N != -1 {
		t.Fatalf("partial: %d %d %d", r.Cell(3).N, r.Cell(4).N, r.Cell(6).N)
	}
}

func TestStatusTone(t *testing.T) {
	for _, tc := range []struct {
		conds  string
		status string
		tone   uint8
	}{
		{cond("ConfigUnsupported") + "," + cond("RecommendationProvided"), "ConfigUnsupported", ingest.ToneDanger},
		{cond("NoPodsMatched"), "NoPodsMatched", ingest.ToneWarning},
		{cond("RecommendationProvided"), "Provided", ingest.ToneSuccess},
		{cond("RecommendationProvided") + "," + cond("LowConfidence"), "LowConfidence", ingest.ToneWarning},
		{`{"type":"RecommendationProvided","status":"False"}`, "Pending", ingest.ToneNeutral},
		{"", "Pending", ingest.ToneNeutral},
	} {
		r := rowOf(t, vpaJSON("Auto", tc.conds, ""))
		if r.StrCell(2) != tc.status || r.Tone() != tc.tone {
			t.Errorf("%s: got %q tone %d, want %q tone %d", tc.conds, r.StrCell(2), r.Tone(), tc.status, tc.tone)
		}
	}
}

func TestModuleSchema(t *testing.T) {
	m := ingest.Lookup(GVR)
	var keys []string
	for _, c := range m.Columns() {
		keys = append(keys, c.Key)
	}
	want := "cluster name namespace target mode status statusTone cpuTarget cpuLower cpuUpper memTarget memLower memUpper createdAt"
	if got := fmt.Sprint(keys); got != "["+want+"]" {
		t.Fatalf("columns %s", got)
	}
	if m.NCells != 9 || m.Tier0 || !m.Optional {
		t.Fatalf("NCells %d Tier0 %v Optional %v", m.NCells, m.Tier0, m.Optional)
	}
}

// The CRD allows int-or-string quantities: a number must decode (a decode error would fail the whole list).
func TestNumericQuantities(t *testing.T) {
	recs := `{"containerName":"a","target":{"cpu":1,"memory":1048576},"lowerBound":{"cpu":"500m","memory":524288},"upperBound":{"cpu":2,"memory":"2Mi"}},` +
		`{"containerName":"b","target":{"memory":1048576}}`
	r := rowOf(t, vpaJSON("Auto", cond("RecommendationProvided"), recs))
	for i, w := range []int64{1000, 500, 2000, 2, 0, 2} { // memory is summed in bytes, then floored to MiB
		if got := r.Cell(3 + i).N; got != w {
			t.Fatalf("cell %d = %d, want %d", 3+i, got, w)
		}
	}
}
