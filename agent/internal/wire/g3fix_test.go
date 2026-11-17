package wire

import (
	"testing"
	"time"

	"github.com/zyno-io/v8s/agent/internal/fake"
	"github.com/zyno-io/v8s/agent/pkg/protocol"
)

// Regression: a repeated requestId returns the same job BEFORE Prepare runs again (params that would fail to
// decode or prepare are never looked at).
func TestJobsStartDedupesBeforePrepare(t *testing.T) {
	e := startEnv(t, fake.Config{Clusters: 1, Pods: 100}, nil)
	c := connect(t, e)
	selectBoth(t, e, c, "prod-us")
	var first protocol.JobsStartResult
	decodeResult(t, c.call("jobs.start", protocol.JobsStartParams{RequestID: "dd1", Type: "drain", Params: map[string]any{"cluster": "prod-us", "node": "node-3",
		"options": map[string]any{"force": true, "deleteEmptyDirData": true, "timeoutSeconds": 30}}}, nil), &first)
	c.waitJob(first.JobID, 30*time.Second)
	var again protocol.JobsStartResult
	decodeResult(t, c.call("jobs.start", protocol.JobsStartParams{RequestID: "dd1", Type: "drain", Params: map[string]any{"cluster": 5}}, nil), &again)
	if again.JobID != first.JobID {
		t.Fatalf("%s != %s", again.JobID, first.JobID)
	}
	// a different requestId with the same bad params is still validated
	if r := c.call("jobs.start", protocol.JobsStartParams{RequestID: "dd2", Type: "drain", Params: map[string]any{"cluster": 5}}, nil); r.Error == nil || r.Error.Code != "bad_request" {
		t.Fatalf("%+v", r.Error)
	}
}
