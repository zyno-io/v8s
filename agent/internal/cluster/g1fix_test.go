package cluster

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/zyno-io/v8s/agent/internal/fake"
	"github.com/zyno-io/v8s/agent/internal/ingest"
	"github.com/zyno-io/v8s/agent/internal/kube"
	"github.com/zyno-io/v8s/agent/internal/view"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/watch"
)

type forbiddenAPI struct{}

func (forbiddenAPI) List(context.Context, metav1.ListOptions) (runtime.Object, error) {
	return nil, apierrors.NewForbidden(schema.GroupResource{Resource: "configmaps"}, "", errors.New("cannot list"))
}
func (forbiddenAPI) Watch(context.Context, metav1.ListOptions) (watch.Interface, error) {
	return nil, apierrors.NewForbidden(schema.GroupResource{Resource: "configmaps"}, "", errors.New("cannot watch"))
}

type denyBackend struct{ kube.Backend }

func (b denyBackend) API(gvr string, m *ingest.Module) (ingest.API, error) {
	if gvr == "v1/configmaps" {
		return forbiddenAPI{}, nil
	}
	return b.Backend.API(gvr, m)
}

type denySource struct{ *fake.Source }

func (s denySource) Backend(id string) (Backend, error) {
	b, err := s.Source.Backend(id)
	if err != nil {
		return nil, err
	}
	return denyBackend{b}, nil
}

// Regression: a generic kind the read context cannot list must fail fast, not hang view.subscribe for 20 s.
func TestResolveModuleFailsFastWhenListForbidden(t *testing.T) {
	if ingest.Lookup("v1/configmaps") != nil {
		t.Skip("configmaps already has a module in this process (an earlier test or -count iteration registered it): nothing to resolve")
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	eng := view.NewEngine()
	go eng.Run(ctx)
	m := NewManager(ctx, denySource{fake.NewSource(fake.Config{Clusters: 1, Pods: 100})}, eng, nil)
	defer m.Close()
	if _, err := m.Select([]string{"prod-us"}); err != nil {
		t.Fatal(err)
	}
	readyAndDiscovered(t, m, "prod-us")
	start := time.Now()
	rctx, rcancel := context.WithTimeout(ctx, 15*time.Second)
	defer rcancel()
	_, err := m.ResolveModule(rctx, "v1/configmaps", nil)
	if !apierrors.IsForbidden(err) {
		t.Fatalf("want forbidden, got %v", err)
	}
	if d := time.Since(start); d > 5*time.Second {
		t.Fatalf("took %v", d)
	}
}

func forbiddenAPIErr() error {
	_, err := forbiddenAPI{}.List(context.Background(), metav1.ListOptions{})
	return err
}
