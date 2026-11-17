package ops

import (
	"errors"
	"testing"

	authorizationv1 "k8s.io/api/authorization/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	ktesting "k8s.io/client-go/testing"
)

// Regression: Flux actions map errors with mapW, so a 403 from an expired write context is write_unavailable
// (and an admission 403 keeps its own code).
func TestFluxActionsClassifyWriteForbidden(t *testing.T) {
	forbid := func(c *fakeConn) {
		c.dyn.PrependReactor("patch", "kustomizations", func(ktesting.Action) (bool, runtime.Object, error) {
			return true, nil, apierrors.NewForbidden(schema.GroupResource{Resource: "kustomizations"}, "k", errors.New("nope"))
		})
	}
	cases := []struct {
		name  string
		deny  bool // the write context's own access review denies
		wants string
	}{
		{"expired write context", true, "write_unavailable"},
		{"admission rejection", false, "forbidden"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := newConn(true, ks("k"))
			forbid(w)
			if tc.deny {
				w.az.fn = func(*authorizationv1.ResourceAttributes) (bool, string, error) {
					return false, "", errors.New("no session")
				}
			}
			e := fluxEnv(newConn(false, ks("k")), w)
			_, err := fluxAct(e, "1", ksGVR, "k", "flux.suspend")
			if got := code(t, err); got != tc.wants {
				t.Fatalf("got %s (%v), want %s", got, err, tc.wants)
			}
		})
	}
}

func TestFluxPollTimeoutBelowMutationTimeout(t *testing.T) {
	if fluxPollTimeout >= mutationTimeout {
		t.Fatalf("fluxPollTimeout %v must be below mutationTimeout %v", fluxPollTimeout, mutationTimeout)
	}
}
