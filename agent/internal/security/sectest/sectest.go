// Package sectest has helpers for testing the security modules against a fake dynamic client.
package sectest

import (
	"context"

	"github.com/zyno-io/v8s/agent/internal/kube"
	"github.com/zyno-io/v8s/agent/internal/ops"
	"github.com/zyno-io/v8s/agent/internal/security"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	dynfake "k8s.io/client-go/dynamic/fake"
	authv1 "k8s.io/client-go/kubernetes/typed/authorization/v1"
	clienttesting "k8s.io/client-go/testing"
)

// Conn is a kube.Conn over a dynamic client.
type Conn struct{ D dynamic.Interface }

func (c Conn) Dynamic() dynamic.Interface                     { return c.D }
func (c Conn) Authz() authv1.SelfSubjectAccessReviewInterface { return nil }

var _ kube.Conn = Conn{}

// Env is a security.Config over one fake cluster named "c".
type Env struct {
	Dyn  *dynfake.FakeDynamicClient
	Gets int
	Cfg  security.Config
}

// New builds an Env serving objs; kinds maps each gvr to its list kind (fake dynamic clients need it).
func New(kinds map[schema.GroupVersionResource]string, objs ...runtime.Object) *Env {
	e := &Env{Dyn: dynfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), kinds, objs...)}
	e.Dyn.PrependReactor("get", "*", func(clienttesting.Action) (bool, runtime.Object, error) { e.Gets++; return false, nil, nil })
	conn := Conn{e.Dyn}
	e.Cfg = security.Config{
		Conn: func(string) (kube.Conn, error) { return conn, nil },
		Route: func(ctx context.Context, cluster, verb, gvr, sub, ns, name string) (*ops.Routed, error) {
			return &ops.Routed{ContextID: cluster, Conn: conn}, nil
		},
		Clusters: func() []string { return []string{"c"} },
	}
	return e
}
