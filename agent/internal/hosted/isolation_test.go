package hosted

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/zyno-io/v8s/agent/internal/hosted/config"
	"github.com/zyno-io/v8s/agent/internal/jobs"
	"github.com/zyno-io/v8s/agent/pkg/protocol"
)

func listIDs(t *testing.T, c *wsClient) string {
	t.Helper()
	var list protocol.ClustersListResult
	decode(t, c.call("clusters.list", nil), &list)
	var ids []string
	for _, cl := range list.Clusters {
		ids = append(ids, cl.ID)
	}
	return strings.Join(ids, ",")
}

func TestPerUserEnginesAreIsolated(t *testing.T) {
	e := newEnv(t, nil)
	alice, bob := e.browser(), e.browser()
	alice.login("alice-sub")
	bob.login("bob-sub")
	ca, _, err := alice.dialWS(goodHeaders(e))
	if err != nil {
		t.Fatal(err)
	}
	cb, _, err := bob.dialWS(goodHeaders(e))
	if err != nil {
		t.Fatal(err)
	}
	ca.hello()
	cb.hello()

	// Group-filtered cluster lists.
	if got := listIDs(t, ca); got != "cluster-1,cluster-3" {
		t.Fatalf("alice (devs) sees %s", got)
	}
	if got := listIDs(t, cb); got != "cluster-2,cluster-3" {
		t.Fatalf("bob (ops) sees %s", got)
	}

	// Alice cannot select a cluster that is only mapped to Bob's group.
	r := ca.call("clusters.select", protocol.ClustersSelectParams{IDs: []string{"cluster-2"}})
	if r.Error == nil {
		t.Fatal("alice selected a cluster outside her groups")
	}
	// ... nor reach it through any other API: the source refuses the id.
	ueA, ueB := e.srv.pool.Get(alice.session()), e.srv.pool.Get(bob.session())
	if ueA == nil || ueB == nil || ueA == ueB || e.srv.pool.Active() != 2 {
		t.Fatalf("expected two distinct engines, got %p %p (%d active)", ueA, ueB, e.srv.pool.Active())
	}
	if _, err := ueA.src.Backend("cluster-2"); err == nil {
		t.Fatal("alice's source opened bob's cluster")
	}
	if _, err := ueB.src.Backend("cluster-1"); err == nil {
		t.Fatal("bob's source opened alice's cluster")
	}

	// Bob's selection works, and builds credentials for Bob, not Alice.
	var st protocol.ClusterStates
	decode(t, cb.call("clusters.select", protocol.ClustersSelectParams{IDs: []string{"cluster-2"}}), &st)
	cb.waitReady("cluster-2")
	e.mu.Lock()
	cr := e.creds["bob@example.com/cluster-2"]
	e.mu.Unlock()
	if cr.Identity.Username != "bob@example.com" || len(cr.Identity.Groups) != 1 || cr.Identity.Groups[0] != "ops" {
		t.Fatalf("creds identity: %+v", cr.Identity)
	}

	// Per-user settings: Alice's change does not appear for Bob, and the files differ.
	decode(t, ca.call("settings.set", protocol.SettingsSetParams{Patch: map[string]any{"stars": []any{"v1/pods"}}}), &protocol.Settings{})
	if len(ueA.set.Get().Stars) != 1 || len(ueB.set.Get().Stars) != 0 {
		t.Fatalf("settings leaked: alice=%v bob=%v", ueA.set.Get().Stars, ueB.set.Get().Stars)
	}
	if e.srv.settingsPath("alice-sub") == e.srv.settingsPath("bob-sub") {
		t.Fatal("users share a settings file")
	}
	// Alice's correlation id is not Bob's.
	if ueA.corr == ueB.corr {
		t.Fatal("correlation ids collide across users")
	}

	// A second session of Alice shares her engine, not Bob's.
	alice2 := e.browser()
	alice2.login("alice-sub")
	c2, _, err := alice2.dialWS(goodHeaders(e))
	if err != nil {
		t.Fatal(err)
	}
	c2.hello()
	if e.srv.pool.Get(alice2.session()) != ueA || e.srv.pool.Active() != 2 {
		t.Fatal("same identity must share one engine")
	}
	// The same person with a different group set is a different identity (different visibility).
	if (Identity{Sub: "s", Groups: []string{"a", "b"}}).Key() != (Identity{Sub: "s", Groups: []string{"b", "a"}}).Key() ||
		(Identity{Sub: "s", Groups: []string{"a"}}).Key() == (Identity{Sub: "s", Groups: []string{"a", "b"}}).Key() {
		t.Fatal("identity key must depend on the set of groups, not their order")
	}
}

func TestStreamEndpointsNeedSessionAndEngine(t *testing.T) {
	e := newEnv(t, nil)
	b := e.browser()
	do := func(path string, h map[string]string) int {
		req, _ := http.NewRequest("GET", e.ts.URL+path, nil)
		for k, v := range h {
			req.Header.Set(k, v)
		}
		resp, err := b.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	same := map[string]string{"Sec-Fetch-Site": "same-origin"}
	if c := do("/stream/download?streamId=x", same); c != 401 {
		t.Fatalf("download without a session: %d", c)
	}
	b.login("alice-sub")
	// Safari/iOS send no fetch metadata on <a download>: allowed (the streamId is the credential).
	if c := do("/stream/download?streamId=x", nil); c != 409 {
		t.Fatalf("download without fetch metadata: %d", c)
	}
	if c := do("/stream/download?streamId=x", map[string]string{"Sec-Fetch-Site": "cross-site"}); c != 403 {
		t.Fatalf("cross-site download: %d", c)
	}
	if c := do("/stream/download?streamId=x", same); c != 409 {
		t.Fatalf("download without an open data socket: %d", c)
	}
}

func TestVisibleClusters(t *testing.T) {
	cfg := testConfig("https://idp.example", "https://v8s.example", "")
	cfg.AdminGroups = []string{"admins"}
	ids := func(g ...string) string {
		var out []string
		for _, c := range visibleClusters(cfg, Identity{Groups: g}) {
			out = append(out, c.ID)
		}
		return strings.Join(out, ",")
	}
	for in, want := range map[string]string{"devs": "cluster-1,cluster-3", "ops": "cluster-2,cluster-3", "admins": "cluster-1,cluster-2,cluster-3", "": "cluster-3"} {
		if got := ids(in); got != want {
			t.Errorf("groups %q: got %s want %s", in, got, want)
		}
	}
	cfg.Clusters = append(cfg.Clusters, config.Cluster{ID: "nobody", Groups: nil})
	if strings.Contains(ids("devs", "ops"), "nobody") {
		t.Error("a cluster with no groups must be visible to admins only")
	}
}

func TestEngineStopsThirtySecondsAfterLastClient(t *testing.T) {
	e := newEnv(t, nil)
	sess := e.srv.store.Create(Identity{Sub: "alice-sub", Username: "alice@example.com", Groups: []string{"devs"}})
	pool := e.srv.pool

	_, release, err := pool.Acquire(sess)
	if err != nil {
		t.Fatal(err)
	}
	if pool.Active() != 1 {
		t.Fatal("engine did not start on the first client")
	}
	_, release2, _ := pool.Acquire(sess) // a second tab
	release()
	e.clock.Advance(time.Hour) // still one client: nothing stops
	if pool.Active() != 1 {
		t.Fatal("engine stopped while a client was open")
	}
	release2()
	release2() // idempotent
	e.clock.Advance(29 * time.Second)
	if pool.Active() != 1 {
		t.Fatal("engine stopped before the 30 s grace")
	}
	e.clock.Advance(2 * time.Second)
	if pool.Active() != 0 || pool.Retained() != 0 {
		t.Fatalf("engine still running 31 s after the last client (active=%d retained=%d)", pool.Active(), pool.Retained())
	}

	// A reload within the grace keeps the watches.
	_, release, _ = pool.Acquire(sess)
	release()
	e.clock.Advance(20 * time.Second)
	_, release, _ = pool.Acquire(sess)
	e.clock.Advance(20 * time.Second)
	if pool.Active() != 1 {
		t.Fatal("reconnect inside the grace did not keep the engine")
	}
	release()
	e.clock.Advance(31 * time.Second)
	if pool.Active() != 0 {
		t.Fatal("engine did not stop after the reconnected client left")
	}
}

func TestRunningJobKeepsEngineRecordUntilDone(t *testing.T) {
	e := newEnv(t, nil)
	sess := e.srv.store.Create(Identity{Sub: "alice-sub", Username: "alice@example.com", Groups: []string{"devs"}})
	pool := e.srv.pool
	ue, release, _ := pool.Acquire(sess)
	done := make(chan struct{})
	id := ue.jobs.Start("req-1", "drain", "cluster-1", "node-1", func(ctx context.Context, jc *jobs.Ctx) error {
		select {
		case <-done:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	})
	release()
	e.clock.Advance(31 * time.Second)
	if pool.Active() != 0 {
		t.Fatal("watches must stop 30 s after the last client even with a job running")
	}
	if pool.Retained() != 1 {
		t.Fatal("the job's engine must be retained")
	}
	e.clock.Advance(jobPoll)
	if pool.Retained() != 1 {
		t.Fatal("engine dropped while its job still runs")
	}
	// The user comes back: same engine, jobs intact.
	ue2, release, _ := pool.Acquire(sess)
	if ue2 != ue || pool.Active() != 1 {
		t.Fatal("reconnect did not resume the retained engine")
	}
	release()
	e.clock.Advance(31 * time.Second)
	close(done)
	ue.jobs.Wait(id)
	e.clock.Advance(jobPoll)
	if pool.Retained() != 0 {
		t.Fatal("engine not retired after its job finished")
	}
}
