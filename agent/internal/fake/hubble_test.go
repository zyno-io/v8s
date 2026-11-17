package fake

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/zyno-io/v8s/agent/internal/hubble"
	"github.com/zyno-io/v8s/agent/internal/kube"
	"github.com/zyno-io/v8s/agent/pkg/protocol"
)

func TestFakeHubbleRelay(t *testing.T) {
	src := NewSource(Config{Clusters: 3, Pods: 300})
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	be, err := src.Backend("prod-us")
	if err != nil {
		t.Fatal(err)
	}
	info, err := hubble.Discover(ctx, be.Dynamic())
	if err != nil || info.Service != "hubble-relay" || info.Namespace != "kube-system" || info.Port != 4245 || info.Pod == "" {
		t.Fatalf("discover: %+v %v", info, err)
	}
	staging, err := src.Backend("staging") // the third fake cluster has no Cilium
	if err != nil {
		t.Fatal(err)
	}
	if _, err := hubble.Discover(ctx, staging.Dynamic()); !errors.Is(err, hubble.ErrNoRelay) {
		t.Fatalf("staging should have no relay: %v", err)
	}
	svc := hubble.New(hubble.Config{Conn: func(id string) (kube.Conn, error) { return src.Backend(id) }, Clusters: func() []string { return []string{"prod-us", "staging"} }})
	defer svc.Close()
	st := svc.Status(ctx, "prod-us")
	if !st.Available || st.ConnectedNodes != 3 {
		t.Fatalf("status through the fake port-forward: %+v", st)
	}
	d := svc.Drops(ctx, protocol.HubbleDropsParams{WindowSec: 60})
	if len(d.Groups) == 0 || d.Groups[0].Reason != "POLICY_DENIED" || len(d.Errors) != 1 || d.Errors[0].Cluster != "staging" {
		t.Fatalf("drops: %+v", d)
	}
}
