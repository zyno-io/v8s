package hosted

import (
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/zyno-io/v8s/agent/internal/updatecheck"
	"github.com/zyno-io/v8s/agent/pkg/protocol"
)

// Every user's engine answers update.status from the one server-wide checker (one GitHub request in total).
func TestUpdateStatusSharedAcrossUsers(t *testing.T) {
	var hits atomic.Int32
	gh := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		_, _ = w.Write([]byte(`{"tag_name":"v9.9.9"}`))
	}))
	defer gh.Close()
	e := newEnv(t, nil)
	chk := updatecheck.New(updatecheck.Options{Current: "1.0.0", API: gh.URL})
	e.srv.updates = chk // before any login: no engine exists yet
	go chk.Run(e.srv.baseCtx)

	for _, sub := range []string{"alice-sub", "bob-sub"} {
		b := e.browser()
		b.login(sub)
		c, _, err := b.dialWS(goodHeaders(e))
		if err != nil {
			t.Fatal(err)
		}
		c.hello()
		var st protocol.UpdateStatus
		decode(t, c.call("update.status", nil), &st)
		if st.Latest != "9.9.9" || st.URL != "https://github.com/zyno-io/v8s/releases/tag/v9.9.9" {
			t.Fatalf("%s: %+v", sub, st)
		}
	}
	if n := hits.Load(); n != 1 {
		t.Fatalf("%d GitHub requests", n)
	}
}

// Without a checker (the default in tests and when opted out) the method still exists and answers empty.
func TestUpdateStatusWithoutChecker(t *testing.T) {
	e := newEnv(t, nil)
	b := e.browser()
	b.login("alice-sub")
	c, _, err := b.dialWS(goodHeaders(e))
	if err != nil {
		t.Fatal(err)
	}
	c.hello()
	resp := c.call("update.status", nil)
	if resp.Error != nil {
		t.Fatalf("%+v", resp.Error)
	}
	// An all-omitempty struct may be omitted on the wire entirely; either form means "no update".
	var st protocol.UpdateStatus
	if raw := resp.Result.([]byte); len(raw) > 0 {
		decode(t, resp, &st)
	}
	if st != (protocol.UpdateStatus{}) {
		t.Fatalf("%+v", st)
	}
}
