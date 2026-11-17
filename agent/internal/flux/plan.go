package flux

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// Token is the reconcile token: the request time as RFC3339Nano UTC.
func Token(now time.Time) string { return now.UTC().Format(time.RFC3339Nano) }

// Plan is what an action writes: a JSON merge patch (always types.MergePatchType, field manager "v8s").
type Plan struct {
	Patch []byte
	// Token is the reconcile token written to requestedAt ("" for suspend/resume).
	Token string
	// SourceFirst: reconcile the referenced source and wait for it before patching the object.
	SourceFirst bool
}

func annPatch(anns map[string]string) []byte {
	b, _ := json.Marshal(map[string]any{"metadata": map[string]any{"annotations": anns}})
	return b
}

// ReconcilePatch is the merge patch that requests a reconcile with token.
func ReconcilePatch(token string) []byte { return annPatch(map[string]string{AnnRequestedAt: token}) }

// PlanAction plans a flux.* action on an object of kind. The returned error is always a bad request.
func PlanAction(action, kind string, now time.Time, hasSource bool) (Plan, error) {
	switch action {
	case "flux.suspend", "flux.resume":
		return Plan{Patch: []byte(fmt.Sprintf(`{"spec":{"suspend":%t}}`, action == "flux.suspend"))}, nil
	case "flux.reconcile":
		t := Token(now)
		return Plan{Patch: ReconcilePatch(t), Token: t}, nil
	case "flux.force", "flux.reset":
		if kind != "HelmRelease" {
			return Plan{}, fmt.Errorf("action %q is only supported for HelmRelease, not %s", action, kind)
		}
		t := Token(now)
		key := AnnForceAt
		if action == "flux.reset" {
			key = AnnResetAt
		}
		return Plan{Patch: annPatch(map[string]string{key: t, AnnRequestedAt: t}), Token: t}, nil
	case "flux.reconcileWithSource":
		switch kind {
		case "Kustomization", "HelmRelease", "HelmChart":
		default:
			return Plan{}, fmt.Errorf("%s has no source to reconcile", kind)
		}
		if !hasSource {
			return Plan{}, fmt.Errorf("%s has no source reference", kind)
		}
		t := Token(now)
		return Plan{Patch: ReconcilePatch(t), Token: t, SourceFirst: true}, nil
	}
	if strings.HasPrefix(action, "flux.") {
		return Plan{}, fmt.Errorf("unknown action %q", action)
	}
	return Plan{}, fmt.Errorf("not a flux action: %q", action)
}
