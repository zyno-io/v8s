package fake

import (
	"time"
)

// Fake data for the platform problem classes: an unavailable APIService, fail-closed webhooks whose Service has
// no endpoints, cert-manager Certificates (healthy, expiring, not ready), Gateway API routes (one not
// accepted) and Flux controller Deployments (one down).

func init() {
	cluster := func(gvr, apiVersion, kind, resource string, namespaced bool, short ...string) *kindDef {
		return &kindDef{gvr: gvr, apiVersion: apiVersion, kind: kind, resource: resource, namespaced: namespaced, short: short}
	}
	registerKinds(
		cluster("apiregistration.k8s.io/v1/apiservices", "apiregistration.k8s.io/v1", "APIService", "apiservices", false),
		cluster("admissionregistration.k8s.io/v1/validatingwebhookconfigurations", "admissionregistration.k8s.io/v1", "ValidatingWebhookConfiguration", "validatingwebhookconfigurations", false),
		cluster("admissionregistration.k8s.io/v1/mutatingwebhookconfigurations", "admissionregistration.k8s.io/v1", "MutatingWebhookConfiguration", "mutatingwebhookconfigurations", false),
		cluster("cert-manager.io/v1/certificates", "cert-manager.io/v1", "Certificate", "certificates", true, "cert", "certs"),
		cluster("gateway.networking.k8s.io/v1/httproutes", "gateway.networking.k8s.io/v1", "HTTPRoute", "httproutes", true),
		cluster("gateway.networking.k8s.io/v1/grpcroutes", "gateway.networking.k8s.io/v1", "GRPCRoute", "grpcroutes", true),
		cluster("gateway.networking.k8s.io/v1/gateways", "gateway.networking.k8s.io/v1", "Gateway", "gateways", true),
	)
	registerGen(genPlatform)
}

func cond(typ, status, reason, msg string, ago time.Duration) map[string]any {
	return map[string]any{"type": typ, "status": status, "reason": reason, "message": msg, "lastTransitionTime": ts(time.Now().Add(-ago))}
}

func genPlatform(w *World, c *Cluster) {
	idx := clusterIndex(c.name)
	now := time.Now()

	// APIServices: the metrics API is down on every other cluster
	w.putLocked("apiregistration.k8s.io/v1/apiservices", map[string]any{"metadata": objMeta("", "v1.apps", nil),
		"spec": map[string]any{}, "status": map[string]any{"conditions": []any{cond("Available", "True", "Local", "Local APIServices are always available", 48*time.Hour)}}})
	metricsStatus := cond("Available", "True", "Passed", "all checks passed", 48*time.Hour)
	if idx%2 == 0 {
		metricsStatus = cond("Available", "False", "MissingEndpoints", "endpoints for service/metrics-server in \"kube-system\" have no addresses with port name \"https\"", 35*time.Minute)
	}
	w.putLocked("apiregistration.k8s.io/v1/apiservices", map[string]any{"metadata": objMeta("", "v1beta1.metrics.k8s.io", nil),
		"spec":   map[string]any{"service": map[string]any{"namespace": "kube-system", "name": "metrics-server"}},
		"status": map[string]any{"conditions": []any{metricsStatus}}})

	// webhooks: one fail-closed validating webhook with no endpoints, one healthy mutating webhook, one Ignore
	hook := func(name, policy, ns, svc string) map[string]any {
		return map[string]any{"name": name, "failurePolicy": policy, "clientConfig": map[string]any{"service": map[string]any{"namespace": ns, "name": svc}}}
	}
	w.putLocked("admissionregistration.k8s.io/v1/validatingwebhookconfigurations", map[string]any{"metadata": objMeta("", "ingress-nginx-admission", nil),
		"webhooks": []any{hook("validate.nginx.ingress.kubernetes.io", "Fail", "ingress-nginx", "ingress-nginx-controller-admission")}})
	w.putLocked("admissionregistration.k8s.io/v1/validatingwebhookconfigurations", map[string]any{"metadata": objMeta("", "cert-manager-webhook", nil),
		"webhooks": []any{hook("webhook.cert-manager.io", "Ignore", "cert-manager", "cert-manager-webhook")}})
	w.putLocked("admissionregistration.k8s.io/v1/mutatingwebhookconfigurations", map[string]any{"metadata": objMeta("", "sidecar-injector", nil),
		"webhooks": []any{hook("inject.example.com", "Fail", teamNS(1), "svc-1")}})

	// cert-manager Certificates
	cert := func(ns, name, ready, reason string, notAfter time.Duration) {
		st := map[string]any{"conditions": []any{cond("Ready", ready, reason, reason, 6*time.Hour)}}
		if notAfter != 0 {
			st["notAfter"] = ts(now.Add(notAfter))
			st["renewalTime"] = ts(now.Add(notAfter - 30*24*time.Hour))
		}
		w.putLocked("cert-manager.io/v1/certificates", map[string]any{"metadata": objMeta(ns, name, nil),
			"spec":   map[string]any{"issuerRef": map[string]any{"kind": "ClusterIssuer", "name": "letsencrypt"}, "secretName": name},
			"status": st})
	}
	cert(teamNS(3), "web-tls", "True", "Ready", 60*24*time.Hour)
	cert(teamNS(4), "api-tls", "True", "Ready", 5*24*time.Hour)
	cert(teamNS(5), "legacy-tls", "False", "Failed", 0)

	// Gateway API
	w.putLocked("gateway.networking.k8s.io/v1/gateways", map[string]any{"metadata": objMeta("infra", "main", nil),
		"status": map[string]any{"conditions": []any{cond("Accepted", "True", "Accepted", "", time.Hour), cond("Programmed", "True", "Programmed", "", time.Hour)}}})
	parent := func(conds ...any) map[string]any {
		return map[string]any{"parents": []any{map[string]any{"parentRef": map[string]any{"name": "main", "namespace": "infra"}, "conditions": conds}}}
	}
	w.putLocked("gateway.networking.k8s.io/v1/httproutes", map[string]any{"metadata": objMeta(teamNS(4), "storefront", nil),
		"status": parent(cond("Accepted", "False", "NoMatchingListenerHostname", "no listener matches hostname shop.example.com", 50*time.Minute), cond("ResolvedRefs", "True", "ResolvedRefs", "", 50*time.Minute))})
	w.putLocked("gateway.networking.k8s.io/v1/httproutes", map[string]any{"metadata": objMeta(teamNS(4), "api", nil),
		"status": parent(cond("Accepted", "True", "Accepted", "", time.Hour), cond("ResolvedRefs", "True", "ResolvedRefs", "", time.Hour))})

	// Flux controllers: source-controller is down on every other cluster
	for _, name := range []string{"source-controller", "kustomize-controller", "helm-controller", "notification-controller"} {
		avail := int64(1)
		conds := []any{cond("Available", "True", "MinimumReplicasAvailable", "", 24*time.Hour)}
		if name == "source-controller" && idx%2 == 1 {
			avail = 0
			conds = []any{cond("Available", "False", "MinimumReplicasUnavailable", "Deployment does not have minimum availability.", 20*time.Minute)}
		}
		w.putLocked("apps/v1/deployments", map[string]any{"metadata": objMeta("flux-system", name, map[string]string{"app": name}),
			"spec":   map[string]any{"replicas": int64(1), "selector": map[string]any{"matchLabels": map[string]any{"app": name}}, "template": podTemplate(name, "ghcr.io/fluxcd/"+name+":v1.4.0")},
			"status": map[string]any{"replicas": int64(1), "readyReplicas": avail, "availableReplicas": avail, "updatedReplicas": int64(1), "conditions": conds}})
	}
}
