package streams

import (
	"context"
	"errors"
	"testing"

	"github.com/zyno-io/v8s/agent/internal/ops"
	"github.com/zyno-io/v8s/agent/pkg/protocol"
)

// A stream pinned to a pod uid must not attach to a pod recreated under the same name.
func TestStreamOpenChecksRefUID(t *testing.T) {
	p := pod("ns", "p1", "app")
	p.UID = "uid-1"
	m, _, _ := newMgr(t, p)
	ref := func(uid string) *protocol.ObjectRef {
		return &protocol.ObjectRef{Cluster: "c1", Namespace: "ns", Name: "p1", UID: uid}
	}
	_, err := m.Open(context.Background(), protocol.StreamOpenParams{Type: "exec", Ref: ref("old-uid")})
	var oe *ops.Error
	if !errors.As(err, &oe) || oe.Body.Code != "conflict" || oe.Body.Message != ops.ErrReplaced {
		t.Fatalf("want replaced conflict, got %v", err)
	}
	if _, err := m.Open(context.Background(), protocol.StreamOpenParams{Type: "exec", Ref: ref("uid-1")}); err != nil {
		t.Fatalf("matching uid: %v", err)
	}
}
