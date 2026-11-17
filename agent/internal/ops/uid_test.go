package ops

import (
	"context"
	"errors"
	"testing"

	"github.com/zyno-io/v8s/agent/pkg/protocol"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	ktesting "k8s.io/client-go/testing"
)

func pinned(uid string) protocol.ObjectRef {
	r := depRef()
	r.UID = uid
	return r
}

func TestUIDPinnedAction(t *testing.T) {
	e := newEnv(newConn(true, deployment("ns", "web")), nil)
	// same uid: the patch goes through
	if _, err := e.s.Action(context.Background(), protocol.ObjectActionParams{RequestID: "a1", Ref: pinned("dep-uid"), Action: "scale", Params: map[string]any{"replicas": 3}}); err != nil {
		t.Fatal(err)
	}
	// the object was deleted and recreated: conflict, and nothing is patched
	patched := false
	e.read.dyn.PrependReactor("patch", "deployments", func(ktesting.Action) (bool, runtime.Object, error) { patched = true; return false, nil, nil })
	_, err := e.s.Action(context.Background(), protocol.ObjectActionParams{RequestID: "a2", Ref: pinned("old-uid"), Action: "restart"})
	var pe *Error
	if !errors.As(err, &pe) || pe.Body.Code != "conflict" || pe.Body.Message != ErrReplaced {
		t.Fatalf("want conflict %q, got %v", ErrReplaced, err)
	}
	if patched {
		t.Fatal("patched a replaced object")
	}
	// an unpinned request is unchanged
	if _, err := e.s.Action(context.Background(), protocol.ObjectActionParams{RequestID: "a3", Ref: depRef(), Action: "restart"}); err != nil {
		t.Fatal(err)
	}
}

func TestUIDPinnedDeleteSendsPrecondition(t *testing.T) {
	e := newEnv(newConn(true, deployment("ns", "web")), nil)
	var got *string
	e.read.dyn.PrependReactor("delete", "deployments", func(a ktesting.Action) (bool, runtime.Object, error) {
		if d, ok := a.(ktesting.DeleteActionImpl); ok && d.DeleteOptions.Preconditions != nil && d.DeleteOptions.Preconditions.UID != nil {
			s := string(*d.DeleteOptions.Preconditions.UID)
			got = &s
		}
		return true, nil, apierrors.NewConflict(schema.GroupResource{Group: "apps", Resource: "deployments"}, "web", errors.New("Precondition failed: UID in precondition: x"))
	})
	_, err := e.s.Delete(context.Background(), protocol.ObjectDeleteParams{RequestID: "d1", Ref: pinned("dep-uid")})
	var pe *Error
	if !errors.As(err, &pe) || pe.Body.Code != "conflict" || pe.Body.Message != ErrReplaced {
		t.Fatalf("want replaced conflict, got %v", err)
	}
	if got == nil || *got != "dep-uid" {
		t.Fatalf("precondition not sent: %v", got)
	}
}

func TestUIDPinnedUpdateApply(t *testing.T) {
	e := newEnv(newConn(true, deployment("ns", "web")), nil)
	p := protocol.ObjectApplyParams{RequestID: "u1", Ref: pinned("other"), YAML: webYAML, Mode: "update", ResourceVersion: "10"}
	_, err := e.s.Apply(context.Background(), p)
	var pe *Error
	if !errors.As(err, &pe) || pe.Body.Code != "conflict" || pe.Body.Message != ErrReplaced {
		t.Fatalf("want replaced conflict, got %v", err)
	}
}
