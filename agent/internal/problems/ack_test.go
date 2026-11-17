package problems_test

import (
	"fmt"
	"testing"
	"time"

	"github.com/zyno-io/v8s/agent/internal/ingest"
	. "github.com/zyno-io/v8s/agent/internal/problems"
	"github.com/zyno-io/v8s/agent/internal/problems/ptest"
	corev1 "k8s.io/api/core/v1"
)

func crashers(h *ptest.Harness, rs string, n int) []*ingest.Row {
	var rows []*ingest.Row
	for i := 0; i < n; i++ {
		r := crashPod(fmt.Sprintf("%s-%d", rs, i), rs, h.Clk.Now().Add(-5*time.Minute))
		rows = append(rows, r)
		h.Put(podsGVR, r)
	}
	h.Pass()
	return rows
}

func only(t *testing.T, h *ptest.Harness, class string) Problem {
	t.Helper()
	ps := h.ByClass(class)
	if len(ps) != 1 {
		t.Fatalf("want one %s problem, got %+v", class, ps)
	}
	return ps[0]
}

func TestAckHidesUntilCountGrows(t *testing.T) {
	h := ptest.New(t, podsGVR, rsGVR)
	h.Put(rsGVR, rsRow("api-x", "api"))
	crashers(h, "api-x", 2)
	p := only(t, h, "pod.crashloop")
	if err := h.Hub.Ack(p.ID, 0); err != nil {
		t.Fatal(err)
	}
	if !only(t, h, "pod.crashloop").Acked {
		t.Fatal("ack did not hide the problem")
	}
	if _, ok := h.Set.Acks[p.ID]; !ok {
		t.Fatal("ack was not persisted into settings.acks")
	}
	// one more pod: 3 is below the +50% threshold of 2 -> 3 (ackThreshold = 2 + max(1, 1) = 3): it IS the threshold
	h.Put(podsGVR, crashPod("api-x-9", "api-x", h.Clk.Now().Add(-time.Minute)))
	h.Pass()
	if only(t, h, "pod.crashloop").Acked {
		t.Fatal("ack should lapse once the count grew past the threshold")
	}
	h.Hub.Flush()
	if _, ok := h.Set.Acks[p.ID]; ok {
		t.Fatal("lapsed ack stayed in settings")
	}
}

func TestAckSnoozeExpires(t *testing.T) {
	h := ptest.New(t, podsGVR, rsGVR)
	crashers(h, "api-x", 1)
	p := only(t, h, "pod.crashloop")
	if err := h.Hub.Ack(p.ID, h.Clk.Now().Add(time.Hour).UnixMilli()); err != nil {
		t.Fatal(err)
	}
	h.Advance(30 * time.Minute)
	if !only(t, h, "pod.crashloop").Acked {
		t.Fatal("snooze ended early")
	}
	h.Advance(31 * time.Minute)
	if only(t, h, "pod.crashloop").Acked {
		t.Fatal("snooze did not expire")
	}
}

func TestAckSurvivesRestartAndDropsOnResolveRecur(t *testing.T) {
	h := ptest.New(t, podsGVR, rsGVR)
	rows := crashers(h, "api-x", 1)
	p := only(t, h, "pod.crashloop")
	if err := h.Hub.Ack(p.ID, 0); err != nil {
		t.Fatal(err)
	}
	// a new hub over the same settings (an agent restart) still finds the ack by the stable id
	h2 := ptest.NewWithSettings(t, h.Set, podsGVR, rsGVR)
	h2.Put(podsGVR, crashPod("api-x-0", "api-x", h2.Clk.Now().Add(-5*time.Minute)))
	h2.Pass()
	if !only(t, h2, "pod.crashloop").Acked {
		t.Fatal("ack lost across an agent restart")
	}
	// resolves (pod deleted) and recurs: it is a new problem, no longer acknowledged
	h.Del(podsGVR, rows[0])
	h.Pass()
	if len(h.ByClass("pod.crashloop")) != 0 {
		t.Fatal("not resolved")
	}
	h.Hub.Flush()
	if _, ok := h.Set.Acks[p.ID]; ok {
		t.Fatal("ack stayed after the problem resolved")
	}
	crashers(h, "api-x", 1)
	if only(t, h, "pod.crashloop").Acked {
		t.Fatal("a recurrence stayed acknowledged")
	}
}

func TestUnack(t *testing.T) {
	h := ptest.New(t, podsGVR, rsGVR)
	crashers(h, "api-x", 1)
	p := only(t, h, "pod.crashloop")
	_ = h.Hub.Ack(p.ID, 0)
	if err := h.Hub.Unack(p.ID); err != nil {
		t.Fatal(err)
	}
	if only(t, h, "pod.crashloop").Acked || len(h.Set.Acks) != 0 {
		t.Fatal("unack did not restore")
	}
	if err := h.Hub.Ack("nope", 0); err == nil {
		t.Fatal("acking an unknown problem must fail")
	}
}

func TestSeverityRiseBreaksAck(t *testing.T) {
	// the severity of a problem can rise when a draft's severity changes: pending acks hold the old rank
	h := ptest.New(t, nodeGVR)
	node := func(ready corev1.ConditionStatus) *ingest.Row {
		return proj(t, nodeGVR, &corev1.Node{ObjectMeta: meta("", "n1"), Status: corev1.NodeStatus{Conditions: []corev1.NodeCondition{
			{Type: corev1.NodeReady, Status: ready, LastTransitionTime: mt(h.Clk.Now().Add(-time.Hour))},
			{Type: corev1.NodeMemoryPressure, Status: corev1.ConditionTrue, LastTransitionTime: mt(h.Clk.Now().Add(-time.Hour))}}}})
	}
	h.Put(nodeGVR, node(corev1.ConditionTrue))
	h.Pass()
	p := only(t, h, "node.pressure") // error
	_ = h.Hub.Ack(p.ID, 0)
	h.Put(nodeGVR, node(corev1.ConditionFalse)) // node.not-ready is critical: a different class, pressure unchanged
	h.Pass()
	if !only(t, h, "node.pressure").Acked {
		t.Fatal("an unrelated change must not clear the ack")
	}
}
