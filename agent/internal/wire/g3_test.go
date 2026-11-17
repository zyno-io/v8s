package wire

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/zyno-io/v8s/agent/internal/fake"
	"github.com/zyno-io/v8s/agent/internal/problems"
	"github.com/zyno-io/v8s/agent/internal/rpc"
	"github.com/zyno-io/v8s/agent/pkg/protocol"
)

// The problems view, problems.classes / evidence / ack over the wire, and the extension handler hook.
func TestG3ProblemsOverWire(t *testing.T) {
	var hub *problems.Hub
	e := startEnv(t, fake.Config{Clusters: 2, Pods: 800}, func(o *Options) {
		hub = problems.NewHub(problems.HubConfig{Settings: o.Settings.Get, Patch: o.Settings.Patch, Interval: 50 * time.Millisecond})
		o.Settings.OnChange(hub.SettingsChanged)
		o.Manager.SetProblems(hub)
		o.Problems = hub
		o.Handlers = map[string]rpc.Handler{"x.echo": func(ctx context.Context, decode func(any) error) (any, error) {
			var p struct{ Text string }
			if err := decode(&p); err != nil {
				return nil, err
			}
			return map[string]string{"text": strings.ToUpper(p.Text)}, nil
		}}
	})
	c, _, err := e.dial(t, nil, protocol.Subprotocol, "token."+testToken)
	if err != nil {
		t.Fatal(err)
	}
	if h, ok := c.next(5 * time.Second); !ok || h.kind != protocol.KindHello {
		t.Fatal("no hello")
	}
	decodeResult(t, c.call("clusters.select", protocol.ClustersSelectParams{IDs: []string{"prod-us", "prod-eu"}}, nil), &protocol.ClusterStates{})
	c.waitReady(2)

	var sub protocol.ViewSubscribeResult
	decodeResult(t, c.call("view.subscribe", protocol.ViewSubscribeParams{QID: 1, Gen: 1, Window: protocol.Window{Limit: 50},
		Query: protocol.ViewQuery{GVR: protocol.ProblemsGVR, Filters: []protocol.Filter{{Col: "family", Op: "in", Value: []string{"pods", "nodes"}}}}}, nil), &sub)
	if got := colKeys(sub.Columns); got != "cluster,name,namespace,severity,severityRank,class,family,kind,title,message,since,lastChanged,count,source,acked,problemId,statusTone" {
		t.Fatalf("problem columns: %s", got)
	}
	var id string
	deadline := time.Now().Add(20 * time.Second)
	for id == "" && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
		if en := hub.Engine("prod-us"); en != nil {
			for _, p := range en.Problems() {
				if p.Class == "node.not-ready" {
					id = p.ID
				}
			}
		}
	}
	if id == "" {
		t.Fatal("no node.not-ready problem in the fake cluster")
	}

	var classes protocol.ProblemsClassesResult
	decodeResult(t, c.call("problems.classes", nil, nil), &classes)
	found := false
	for _, ci := range classes.Classes {
		if ci.Class == "node.not-ready" {
			found = ci.Family == "nodes" && ci.Default && ci.Enabled && ci.Counts["prod-us"].Total > 0
		}
	}
	if !found {
		t.Fatalf("problems.classes lacks node.not-ready with a count: %+v", classes.Classes)
	}

	var ev protocol.ProblemsEvidenceResult
	decodeResult(t, c.call("problems.evidence", protocol.ProblemsEvidenceParams{ID: id}, nil), &ev)
	if ev.ID != id || ev.Class != "node.not-ready" || ev.Subject.Kind != "Node" {
		t.Fatalf("evidence: %+v", ev)
	}
	if r := c.call("problems.evidence", protocol.ProblemsEvidenceParams{ID: "nope"}, nil); r.Error == nil || r.Error.Code != "not_found" {
		t.Fatalf("unknown problem: %+v", r.Error)
	}

	if r := c.call("problems.ack", protocol.ProblemsAckParams{ID: id}, nil); r.Error != nil {
		t.Fatal(r.Error)
	}
	if p, _ := hub.Engine("prod-us").Problem(id); !p.Acked {
		t.Fatal("ack did not hide the problem")
	}
	if r := c.call("problems.unack", protocol.ProblemsUnackParams{ID: id}, nil); r.Error != nil {
		t.Fatal(r.Error)
	}
	if p, _ := hub.Engine("prod-us").Problem(id); p.Acked {
		t.Fatal("unack did not restore it")
	}

	var echo map[string]string
	decodeResult(t, c.call("x.echo", map[string]string{"Text": "hi"}, nil), &echo)
	if echo["text"] != "HI" {
		t.Fatalf("extension handler: %v", echo)
	}
}
