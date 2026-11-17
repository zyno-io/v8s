package wire

import "github.com/zyno-io/v8s/agent/pkg/protocol"

// Kinds is the static table of common kinds; only pods have a view in P0.
var Kinds = []protocol.Kind{
	k("v1/pods", "Pod", "pods", true, true, "po"),
	k("apps/v1/deployments", "Deployment", "deployments", true, false, "deploy"),
	k("v1/services", "Service", "services", true, false, "svc"),
	k("v1/nodes", "Node", "nodes", false, false, "no"),
	k("v1/namespaces", "Namespace", "namespaces", false, false, "ns"),
	k("v1/configmaps", "ConfigMap", "configmaps", true, false, "cm"),
	k("v1/secrets", "Secret", "secrets", true, false),
	k("apps/v1/statefulsets", "StatefulSet", "statefulsets", true, false, "sts"),
	k("apps/v1/daemonsets", "DaemonSet", "daemonsets", true, false, "ds"),
	k("batch/v1/jobs", "Job", "jobs", true, false),
	k("batch/v1/cronjobs", "CronJob", "cronjobs", true, false, "cj"),
	k("networking.k8s.io/v1/ingresses", "Ingress", "ingresses", true, false, "ing"),
	k("v1/persistentvolumeclaims", "PersistentVolumeClaim", "persistentvolumeclaims", true, false, "pvc"),
	k("v1/events", "Event", "events", true, false, "ev"),
	k("kustomize.toolkit.fluxcd.io/v1/kustomizations", "Kustomization", "kustomizations", true, false, "ks"),
	k("helm.toolkit.fluxcd.io/v2/helmreleases", "HelmRelease", "helmreleases", true, false, "hr"),
}

func k(gvr, kind, plural string, namespaced, available bool, short ...string) protocol.Kind {
	if short == nil {
		short = []string{}
	}
	return protocol.Kind{GVR: gvr, Kind: kind, Plural: plural, ShortNames: short, Namespaced: namespaced, Available: available}
}
