package flux

import "testing"

// Regression: cli-utils encodes ":" in object names as "__" in inventory ids.
func TestParseInventoryIDDecodesColons(t *testing.T) {
	cases := []struct{ id, ns, name, group, kind string }{
		{"apps_web_apps_Deployment", "apps", "web", "apps", "Deployment"},
		{"_system__controller__deployment-controller_rbac.authorization.k8s.io_ClusterRole", "", "system:controller:deployment-controller", "rbac.authorization.k8s.io", "ClusterRole"},
		{"ns_my_name_apps_StatefulSet", "ns", "my_name", "apps", "StatefulSet"},
		{"_cert-manager__leaderelection_rbac.authorization.k8s.io_ClusterRole", "", "cert-manager:leaderelection", "rbac.authorization.k8s.io", "ClusterRole"},
		{"ns_cm__x__y__z__0___ConfigMap", "ns", "cm:x:y:z:0_", "", "ConfigMap"},
	}
	for _, c := range cases {
		ns, name, group, kind := parseInventoryID(c.id)
		if ns != c.ns || name != c.name || group != c.group || kind != c.kind {
			t.Errorf("%s -> %q %q %q %q", c.id, ns, name, group, kind)
		}
	}
}
