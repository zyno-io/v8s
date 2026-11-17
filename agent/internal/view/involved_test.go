package view

import (
	"errors"
	"testing"

	"github.com/zyno-io/v8s/agent/pkg/protocol"
)

func TestNormalizeRejectsInvolved(t *testing.T) {
	_, err := Normalize(protocol.ViewQuery{GVR: "v1/events", Involved: &protocol.ObjectRef{Cluster: "c", GVR: "v1/pods", Namespace: "ns", Name: "p"}})
	var bq *BadQueryError
	if !errors.As(err, &bq) {
		t.Fatalf("want a bad query error, got %v", err)
	}
}
