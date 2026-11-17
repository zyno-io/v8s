//go:build e2e

package e2e

import (
	"context"
	"strconv"
	"strings"
	"testing"

	"github.com/zyno-io/v8s/agent/internal/cluster"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

func listDeployments(ctx context.Context, src *cluster.KubeSource, id string) ([]string, error) {
	be, err := src.Backend(id)
	if err != nil {
		return nil, err
	}
	l, err := be.Dynamic().Resource(schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "deployments"}).Namespace("payments").List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, err
	}
	var out []string
	for _, it := range l.Items {
		out = append(out, it.GetName())
	}
	return out, nil
}

func replicasOf(t *testing.T, yaml string) int {
	t.Helper()
	for _, line := range strings.Split(yaml, "\n") {
		if v, ok := strings.CutPrefix(strings.TrimSpace(line), "replicas: "); ok && strings.HasPrefix(line, "  replicas") {
			n, err := strconv.Atoi(v)
			if err != nil {
				t.Fatal(err)
			}
			return n
		}
	}
	t.Fatal("no spec.replicas in object")
	return 0
}
