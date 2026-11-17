package hosted

import (
	"testing"
	"time"

	"github.com/vmihailenco/msgpack/v5"
	"github.com/zyno-io/v8s/agent/pkg/protocol"
)

// A hosted user engine is built by the same builder as the desktop agent: problems, security and Hubble are served.
func TestHostedEngineServesProblemsSecurityHubble(t *testing.T) {
	e := newEnv(t, nil)
	b := e.browser()
	b.login("alice-sub")
	c, _, err := b.dialWS(goodHeaders(e))
	if err != nil {
		t.Fatal(err)
	}
	c.hello()
	var st protocol.ClusterStates
	decode(t, c.call("clusters.select", protocol.ClustersSelectParams{IDs: []string{"cluster-1"}}), &st)
	c.waitReady("cluster-1")

	var sub protocol.ViewSubscribeResult
	decode(t, c.call("view.subscribe", protocol.ViewSubscribeParams{QID: 1, Gen: 1, Window: protocol.Window{Limit: 50},
		Query: protocol.ViewQuery{GVR: protocol.ProblemsGVR}}), &sub)
	if len(sub.Columns) == 0 {
		t.Fatal("problems view has no columns")
	}

	var classes protocol.ProblemsClassesResult
	deadline := time.Now().Add(30 * time.Second)
	total := 0
	for total == 0 && time.Now().Before(deadline) {
		time.Sleep(100 * time.Millisecond)
		classes = protocol.ProblemsClassesResult{}
		decode(t, c.call("problems.classes", nil), &classes)
		for _, ci := range classes.Classes {
			total += ci.Counts["cluster-1"].Total
		}
	}
	if len(classes.Classes) == 0 || total == 0 {
		t.Fatalf("problems.classes: %d classes, %d problems on cluster-1", len(classes.Classes), total)
	}
	gotRows := false
	for end := time.Now().Add(20 * time.Second); !gotRows && time.Now().Before(end); {
		f, ok := c.next(time.Second)
		if !ok || f.kind != protocol.KindPatch {
			continue
		}
		var vp protocol.ViewPatch
		if err := msgpack.Unmarshal(f.payload, &vp); err != nil {
			t.Fatal(err)
		}
		c.send(protocol.KindAck, 0, f.seq, nil)
		gotRows = len(vp.Rows) > 0
	}
	if !gotRows {
		t.Fatal("problems view never delivered rows")
	}

	var imgs protocol.SecurityImagesResult
	decode(t, c.call("security.images", protocol.SecurityImagesParams{}), &imgs)

	var hs protocol.HubbleStatus
	decode(t, c.call("hubble.status", protocol.HubbleStatusParams{Cluster: "cluster-1"}), &hs)
	if !hs.Available {
		t.Fatalf("hubble.status: %+v", hs)
	}
}
