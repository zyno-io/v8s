package security

import (
	"strings"

	"github.com/zyno-io/v8s/agent/internal/ops"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

var getOpts = metav1.GetOptions{}

// ParseGVR parses "v1/pods" or "group/version/resource".
func ParseGVR(s string) (schema.GroupVersionResource, error) {
	parts := strings.Split(s, "/")
	switch len(parts) {
	case 2:
		return schema.GroupVersionResource{Version: parts[0], Resource: parts[1]}, nil
	case 3:
		return schema.GroupVersionResource{Group: parts[0], Version: parts[1], Resource: parts[2]}, nil
	}
	return schema.GroupVersionResource{}, ops.NewError("bad_request", "invalid gvr %q", s)
}

// GroupOf returns the API group of a gvr string ("" for core).
func GroupOf(gvr string) string {
	if parts := strings.Split(gvr, "/"); len(parts) == 3 {
		return parts[0]
	}
	return ""
}

// ResourceOf returns the plural resource of a gvr string.
func ResourceOf(gvr string) string {
	parts := strings.Split(gvr, "/")
	return parts[len(parts)-1]
}

// KindGVR maps the common workload kinds a report scope names to their gvr and group, so a problem about "Deployment
// api" can fetch the live object. Unknown kinds return empty strings.
func KindGVR(kind string) (group, gvr string) {
	switch kind {
	case "Pod":
		return "", "v1/pods"
	case "Service":
		return "", "v1/services"
	case "ConfigMap":
		return "", "v1/configmaps"
	case "Namespace":
		return "", "v1/namespaces"
	case "Node":
		return "", "v1/nodes"
	case "ServiceAccount":
		return "", "v1/serviceaccounts"
	case "Deployment":
		return "apps", "apps/v1/deployments"
	case "StatefulSet":
		return "apps", "apps/v1/statefulsets"
	case "DaemonSet":
		return "apps", "apps/v1/daemonsets"
	case "ReplicaSet":
		return "apps", "apps/v1/replicasets"
	case "Job":
		return "batch", "batch/v1/jobs"
	case "CronJob":
		return "batch", "batch/v1/cronjobs"
	case "Ingress":
		return "networking.k8s.io", "networking.k8s.io/v1/ingresses"
	}
	return "", ""
}
