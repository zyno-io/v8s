package drainer

import (
	"context"
	"errors"
	"testing"

	"github.com/zyno-io/v8s/agent/internal/ops"
	"github.com/zyno-io/v8s/agent/pkg/protocol"
	corev1 "k8s.io/api/core/v1"
	policyv1 "k8s.io/api/policy/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// Regression: an empty (non-nil) PDB selector matches every pod of the namespace; a nil selector none.
func TestClassifyEmptyPDBSelector(t *testing.T) {
	pods := []corev1.Pod{owned("a", "web-1", "ReplicaSet", map[string]string{"app": "web"}), owned("b", "other", "ReplicaSet", nil)}
	cases := []struct {
		name    string
		sel     *metav1.LabelSelector
		blocked string
	}{
		{"empty selector selects all in namespace", &metav1.LabelSelector{}, "a/web-1"},
		{"nil selector selects none", nil, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			pdbs := []policyv1.PodDisruptionBudget{{ObjectMeta: metav1.ObjectMeta{Namespace: "a", Name: "all"}, Spec: policyv1.PodDisruptionBudgetSpec{Selector: tc.sel}}}
			if got := names(Classify(pods, pdbs, protocol.DrainOptions{}).Blocked); got != tc.blocked {
				t.Fatalf("blocked=%q want %q", got, tc.blocked)
			}
		})
	}
}

type mapRouter struct {
	fakeRouter
	got *ops.Routed
}

func (m *mapRouter) MapRouted(_ context.Context, err error, r *ops.Routed, verb, gvr, _, _, name string) error {
	m.got = r
	return ops.NewError("write_unavailable", "%s %s %s: %s", verb, gvr, name, err)
}

// Regression: drain errors through the write context are classified by the router (mapW), not mapErr.
func TestDrainMapsErrorsThroughRouter(t *testing.T) {
	r := &mapRouter{}
	s := New(Config{Router: r})
	routed := &ops.Routed{ContextID: "w", ViaWrite: true}
	err := s.mapRouted(context.Background(), errors.New("x"), routed, "patch", "node-1")
	var oe *ops.Error
	if !errors.As(err, &oe) || oe.Body.Code != "write_unavailable" || r.got != routed {
		t.Fatalf("%v", err)
	}
}
