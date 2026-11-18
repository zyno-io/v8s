package fake

// Network fixtures for IP search: LoadBalancer Services with MetalLB addresses, a Service that only requests one,
// and a dual-stack Service. Pod, node, endpoint and ingress addresses come from the base world (world.go).

func init() { registerGen(genNetwork) }

func genNetwork(w *World, c *Cluster) {
	svcPorts := []any{map[string]any{"port": int64(80), "protocol": "TCP", "targetPort": int64(8080)}}
	w.putLocked("v1/services", map[string]any{
		"metadata": withAnnotations(objMeta("ingress-nginx", "ingress-nginx-controller", map[string]string{"app.kubernetes.io/name": "ingress-nginx"}),
			map[string]any{"metallb.io/loadBalancerIPs": "192.168.100.10"}),
		"spec": map[string]any{"type": "LoadBalancer", "clusterIP": "10.96.250.1", "clusterIPs": []any{"10.96.250.1"},
			"selector": map[string]any{"app.kubernetes.io/name": "ingress-nginx"}, "ports": svcPorts},
		"status": map[string]any{"loadBalancer": map[string]any{"ingress": []any{map[string]any{"ip": "192.168.100.10"}}}}})
	w.putLocked("v1/services", map[string]any{"metadata": objMeta("team-2", "legacy-lb", nil),
		"spec": map[string]any{"type": "LoadBalancer", "clusterIP": "10.96.250.2", "clusterIPs": []any{"10.96.250.2"}, "loadBalancerIP": "192.168.100.250",
			"selector": map[string]any{"app": "svc-2"}, "ports": svcPorts}})
	if clusterIndex(c.name) == 0 {
		w.putLocked("v1/services", map[string]any{"metadata": objMeta("team-0", "dual", nil),
			"spec": map[string]any{"type": "ClusterIP", "clusterIP": "10.96.251.1", "clusterIPs": []any{"10.96.251.1", "fd00:10:96::1"},
				"ipFamilies": []any{"IPv4", "IPv6"}, "ipFamilyPolicy": "RequireDualStack", "selector": map[string]any{"app": "svc-0"}, "ports": svcPorts}})
	}
}

func withAnnotations(md map[string]any, ann map[string]any) map[string]any {
	md["annotations"] = ann
	return md
}
