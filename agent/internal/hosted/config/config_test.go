package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const good = `
publicUrl: https://v8s.example.com
oidc:
  issuer: https://login.example.com/tenant/v2.0
  clientId: v8s
  clientSecret: {env: V8S_OIDC_SECRET}
clusterApp:
  clientId: k8s
  privateKey: {file: /etc/v8s/cluster-app.key}
  certificate: {file: /etc/v8s/cluster-app.crt}
adminGroups: [v8s-admins]
clusters:
  - id: prod-eu
    name: Production EU
    server: https://k8s.example.com
    caData: LS0tLS1CRUdJTg==
    groups: [platform]
    displayGroups: [Production]
  - id: kiosk
    server: https://k8s2.example.com
    strategy: kiosk
    kiosk: {token: {file: /var/run/secrets/kiosk/token}}
    groups: ["*"]
  - id: legacy
    server: https://k8s3.example.com
    strategy: impersonation
    impersonation: {token: {file: /var/run/secrets/imp/token}}
    groups: [ops]
`

func TestParseDefaults(t *testing.T) {
	c, err := Parse([]byte(good))
	if err != nil {
		t.Fatal(err)
	}
	if c.Listen != ":7931" || c.Session.Lifetime.D() != 12*time.Hour || c.Session.IdleTimeout.D() != 30*time.Minute || c.Session.MaxPerUser != 5 {
		t.Fatalf("defaults: %+v %+v", c.Listen, c.Session)
	}
	if c.Limits.ReadQPS != 20 || c.Limits.ReadBurst != 40 || c.Limits.ClusterQPS != 100 {
		t.Fatalf("limits: %+v", c.Limits)
	}
	if c.Clusters[0].Strategy != StrategyPassthrough || c.Clusters[2].Impersonation.Prefix != "v8s:" || c.Clusters[2].Impersonation.GroupPrefix != "v8s:" {
		t.Fatalf("cluster defaults: %+v", c.Clusters)
	}
	if c.Clusters[1].Name != "kiosk" || c.OIDC.UsernameClaim != "preferred_username" || c.OIDC.GroupsClaim != "groups" {
		t.Fatalf("defaults: %+v", c.Clusters[1])
	}
}

func TestParseRejects(t *testing.T) {
	sub := func(old, new string) string { return strings.Replace(good, old, new, 1) }
	cases := map[string]string{
		"unknown field":          sub("adminGroups:", "bogus: 1\nadminGroups:"),
		"http public url":        sub("https://v8s.example.com", "http://v8s.example.com"),
		"public url with path":   sub("https://v8s.example.com", "https://v8s.example.com/app"),
		"both secret and key":    sub("clientSecret: {env: V8S_OIDC_SECRET}", "clientSecret: {env: A}\n  privateKey: {env: B}"),
		"no credential":          sub("clientSecret: {env: V8S_OIDC_SECRET}", ""),
		"missing cluster app":    sub("clusterApp:\n  clientId: k8s\n  privateKey: {file: /etc/v8s/cluster-app.key}\n  certificate: {file: /etc/v8s/cluster-app.crt}\n", ""),
		"duplicate cluster":      sub("id: kiosk", "id: prod-eu"),
		"bad cluster id":         sub("id: kiosk", "id: Kiosk_1"),
		"system prefix":          sub("impersonation: {token:", "impersonation: {prefix: \"system:\", token:"),
		"unknown strategy":       sub("strategy: kiosk", "strategy: magic"),
		"kiosk without token":    sub("kiosk: {token: {file: /var/run/secrets/kiosk/token}}", ""),
		"impersonation no token": sub("impersonation: {token: {file: /var/run/secrets/imp/token}}", ""),
		"short session":          sub("adminGroups:", "session: {lifetime: 5s}\nadminGroups:"),
		"unreachable cluster":    sub("groups: [platform]", "groups: []") + "\n",
	}
	for name, y := range cases {
		if name == "unreachable cluster" {
			y = strings.Replace(y, "adminGroups: [v8s-admins]\n", "", 1)
		}
		if _, err := Parse([]byte(y)); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestSecretRef(t *testing.T) {
	t.Setenv("V8S_TEST_SECRET", " from-env \n")
	if v, err := (SecretRef{Env: "V8S_TEST_SECRET"}).Resolve(); err != nil || v != "from-env" {
		t.Fatalf("env: %q %v", v, err)
	}
	f := filepath.Join(t.TempDir(), "s")
	_ = os.WriteFile(f, []byte("from-file\n"), 0o600)
	if v, err := (SecretRef{File: f}).Resolve(); err != nil || v != "from-file" {
		t.Fatalf("file: %q %v", v, err)
	}
	for _, bad := range []SecretRef{{}, {Env: "V8S_TEST_NOPE"}, {File: f + "x"}, {Env: "V8S_TEST_SECRET", File: f}} {
		if _, err := bad.Resolve(); err == nil {
			t.Errorf("%+v resolved", bad)
		}
	}
}

func TestPublicURLNormalized(t *testing.T) {
	for in, want := range map[string]string{
		"https://v8s.example.com/":     "https://v8s.example.com",
		"https://V8S.example.com:443":  "https://v8s.example.com",
		"https://v8s.example.com:8443": "https://v8s.example.com:8443",
		"http://localhost:80/":         "http://localhost",
	} {
		c, err := Parse([]byte(strings.Replace(good, "https://v8s.example.com", in, 1)))
		if err != nil {
			t.Fatalf("%s: %v", in, err)
		}
		if c.PublicURL != want {
			t.Errorf("%s -> %s, want %s", in, c.PublicURL, want)
		}
	}
}
