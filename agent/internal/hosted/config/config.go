// Package config is the YAML configuration of `v8s server`. Secrets are never inline: every secret is a
// reference to an environment variable or a file.
package config

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"regexp"
	"strings"
	"time"

	"sigs.k8s.io/yaml"
)

// Strategy values of a cluster record.
const (
	StrategyPassthrough   = "oidc-passthrough"
	StrategyImpersonation = "impersonation"
	StrategyKiosk         = "kiosk"
)

// Duration is a JSON/YAML string such as "12h".
type Duration time.Duration

func (d *Duration) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return errors.New("duration must be a string such as \"30m\"")
	}
	v, err := time.ParseDuration(s)
	if err != nil {
		return err
	}
	*d = Duration(v)
	return nil
}

func (d Duration) D() time.Duration { return time.Duration(d) }

// SecretRef points at a secret value: an environment variable or a file (re-read on each Resolve).
type SecretRef struct {
	Env  string `json:"env,omitempty"`
	File string `json:"file,omitempty"`
}

func (s SecretRef) IsZero() bool { return s.Env == "" && s.File == "" }

// Resolve returns the secret with surrounding whitespace trimmed.
func (s SecretRef) Resolve() (string, error) {
	switch {
	case s.Env != "" && s.File != "":
		return "", errors.New("set only one of env and file")
	case s.Env != "":
		v := strings.TrimSpace(os.Getenv(s.Env))
		if v == "" {
			return "", fmt.Errorf("environment variable %s is empty", s.Env)
		}
		return v, nil
	case s.File != "":
		b, err := os.ReadFile(s.File)
		if err != nil {
			return "", fmt.Errorf("read secret file: %w", err)
		}
		v := strings.TrimSpace(string(b))
		if v == "" {
			return "", fmt.Errorf("secret file %s is empty", s.File)
		}
		return v, nil
	}
	return "", errors.New("no secret configured")
}

// Config is the whole server configuration.
type Config struct {
	Listen    string `json:"listen"`
	PublicURL string `json:"publicUrl"`
	// DataDir holds the per-user settings files (the only state written to disk).
	DataDir string `json:"dataDir"`

	Session     Session     `json:"session"`
	OIDC        OIDC        `json:"oidc"`
	ClusterApp  *ClusterApp `json:"clusterApp,omitempty"`
	Clusters    []Cluster   `json:"clusters"`
	AdminGroups []string    `json:"adminGroups,omitempty"`
	Limits      Limits      `json:"limits"`
	// CorrelationKey keys the HMAC of the session id sent in the User-Agent (default: random per start).
	CorrelationKey SecretRef `json:"correlationKey,omitempty"`
}

type Session struct {
	Lifetime    Duration `json:"lifetime"`    // absolute, default 12h
	IdleTimeout Duration `json:"idleTimeout"` // default 30m
	MaxPerUser  int      `json:"maxPerUser"`  // default 5
}

// OIDC is the login provider registration.
type OIDC struct {
	Issuer   string `json:"issuer"`
	ClientID string `json:"clientId"`
	// PublicClient: the app has no credential (PKCE only), e.g. an Entra app whose redirect URI is registered
	// under "Mobile and desktop applications". Explicit, so a forgotten secret is still an error.
	PublicClient  bool      `json:"publicClient,omitempty"`
	ClientSecret  SecretRef `json:"clientSecret,omitempty"`
	PrivateKey    SecretRef `json:"privateKey,omitempty"`  // PEM, RSA (private_key_jwt)
	Certificate   SecretRef `json:"certificate,omitempty"` // PEM, for the x5t#S256 header (Entra ID)
	Scopes        []string  `json:"scopes,omitempty"`
	UsernameClaim string    `json:"usernameClaim,omitempty"` // default preferred_username
	GroupsClaim   string    `json:"groupsClaim,omitempty"`   // default groups
}

// ClusterApp is the cluster app registration: its ID token is what OIDC pass-through clusters receive.
type ClusterApp struct {
	Issuer       string    `json:"issuer,omitempty"` // default: the login issuer
	ClientID     string    `json:"clientId"`
	PublicClient bool      `json:"publicClient,omitempty"` // no credential, PKCE only (see OIDC.PublicClient)
	ClientSecret SecretRef `json:"clientSecret,omitempty"`
	PrivateKey   SecretRef `json:"privateKey,omitempty"`
	Certificate  SecretRef `json:"certificate,omitempty"`
	Scopes       []string  `json:"scopes,omitempty"` // default openid, offline_access
}

type Cluster struct {
	ID       string `json:"id"`
	Name     string `json:"name,omitempty"`
	Server   string `json:"server"`
	CAData   string `json:"caData,omitempty"` // base64 PEM (as in kubeconfig) or PEM
	CAFile   string `json:"caFile,omitempty"`
	Strategy string `json:"strategy,omitempty"` // default oidc-passthrough
	// Impersonation and Kiosk carry the v8s-held credential of those strategies.
	Impersonation *Impersonation `json:"impersonation,omitempty"`
	Kiosk         *KioskCred     `json:"kiosk,omitempty"`
	// Groups are the IdP groups that may see this cluster ("*" = every signed-in user).
	Groups []string `json:"groups,omitempty"`
	// DisplayGroups are the selector groups the cluster is listed under.
	DisplayGroups []string `json:"displayGroups,omitempty"`
}

type Impersonation struct {
	Token  SecretRef `json:"token"`            // the impersonator's bearer token (file: re-read, so projected tokens rotate)
	Prefix string    `json:"prefix,omitempty"` // mandatory username prefix, default "v8s:"
	// GroupPrefix is prepended to forwarded IdP groups (default: Prefix).
	GroupPrefix string `json:"groupPrefix,omitempty"`
	// Groups, when set, restricts forwarded groups to this allowlist (match on the raw IdP group name).
	Groups []string `json:"groups,omitempty"`
}

type KioskCred struct {
	Token SecretRef `json:"token"`
}

type Limits struct {
	ReadQPS      float64 `json:"readQps"`      // per (user, cluster), default 20
	ReadBurst    int     `json:"readBurst"`    // default 40
	ClusterQPS   float64 `json:"clusterQps"`   // ceiling across users, default 100
	ClusterBurst int     `json:"clusterBurst"` // default 200
}

var idRe = regexp.MustCompile(`^[a-z0-9]([a-z0-9._-]{0,62}[a-z0-9])?$`)

// Load reads and validates a configuration file.
func Load(path string) (*Config, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return Parse(b)
}

// Parse parses and validates YAML (unknown fields are errors).
func Parse(b []byte) (*Config, error) {
	var c Config
	if err := yaml.UnmarshalStrict(b, &c); err != nil {
		return nil, fmt.Errorf("config: %w", err)
	}
	c.defaults()
	if err := c.Validate(); err != nil {
		return nil, err
	}
	return &c, nil
}

// normalizeOrigin lowercases scheme and host, drops a trailing slash and the scheme's default port, so the
// configured URL compares equal to the browser's Origin header and redirect URIs are built correctly.
func normalizeOrigin(s string) string {
	u, err := url.Parse(s)
	if err != nil || u.Host == "" {
		return s
	}
	host := strings.ToLower(u.Host)
	switch {
	case u.Scheme == "https" && strings.HasSuffix(host, ":443"):
		host = strings.TrimSuffix(host, ":443")
	case u.Scheme == "http" && strings.HasSuffix(host, ":80"):
		host = strings.TrimSuffix(host, ":80")
	}
	out := strings.ToLower(u.Scheme) + "://" + host
	if u.Path != "" && u.Path != "/" || u.RawQuery != "" {
		return s // Validate rejects it
	}
	return out
}

func (c *Config) defaults() {
	c.PublicURL = normalizeOrigin(c.PublicURL)
	if c.Listen == "" {
		c.Listen = ":7931"
	}
	if c.DataDir == "" {
		c.DataDir = "/var/lib/v8s"
	}
	if c.Session.Lifetime == 0 {
		c.Session.Lifetime = Duration(12 * time.Hour)
	}
	if c.Session.IdleTimeout == 0 {
		c.Session.IdleTimeout = Duration(30 * time.Minute)
	}
	if c.Session.MaxPerUser == 0 {
		c.Session.MaxPerUser = 5
	}
	if c.OIDC.UsernameClaim == "" {
		c.OIDC.UsernameClaim = "preferred_username"
	}
	if c.OIDC.GroupsClaim == "" {
		c.OIDC.GroupsClaim = "groups"
	}
	if len(c.OIDC.Scopes) == 0 {
		c.OIDC.Scopes = []string{"openid", "profile", "email"}
	}
	if ca := c.ClusterApp; ca != nil {
		if len(ca.Scopes) == 0 {
			ca.Scopes = []string{"openid", "offline_access"}
		}
	}
	if c.Limits.ReadQPS == 0 {
		c.Limits.ReadQPS = 20
	}
	if c.Limits.ReadBurst == 0 {
		c.Limits.ReadBurst = 40
	}
	if c.Limits.ClusterQPS == 0 {
		c.Limits.ClusterQPS = 100
	}
	if c.Limits.ClusterBurst == 0 {
		c.Limits.ClusterBurst = 200
	}
	for i := range c.Clusters {
		cl := &c.Clusters[i]
		if cl.Strategy == "" {
			cl.Strategy = StrategyPassthrough
		}
		if cl.Name == "" {
			cl.Name = cl.ID
		}
		if cl.Impersonation != nil {
			if cl.Impersonation.Prefix == "" {
				cl.Impersonation.Prefix = "v8s:"
			}
			if cl.Impersonation.GroupPrefix == "" {
				cl.Impersonation.GroupPrefix = cl.Impersonation.Prefix
			}
		}
	}
}

func secure(u string) (*url.URL, error) {
	p, err := url.Parse(u)
	if err != nil || p.Host == "" {
		return nil, fmt.Errorf("invalid URL %q", u)
	}
	loopback := p.Hostname() == "localhost" || p.Hostname() == "127.0.0.1" || p.Hostname() == "::1"
	if p.Scheme != "https" && !(p.Scheme == "http" && loopback) {
		return nil, fmt.Errorf("URL %q must be https (http only for localhost)", u)
	}
	return p, nil
}

func hasSystem(s string) bool { return strings.HasPrefix(strings.ToLower(s), "system:") }

// Validate checks the configuration (no secrets are read).
func (c *Config) Validate() error {
	pu, err := secure(c.PublicURL)
	if err != nil {
		return fmt.Errorf("publicUrl: %w", err)
	}
	if pu.Path != "" && pu.Path != "/" || pu.RawQuery != "" {
		return errors.New("publicUrl must be an origin without a path")
	}
	if _, err := secure(c.OIDC.Issuer); err != nil {
		return fmt.Errorf("oidc.issuer: %w", err)
	}
	if c.OIDC.ClientID == "" {
		return errors.New("oidc.clientId is required")
	}
	if err := checkAuth("oidc", c.OIDC.PublicClient, c.OIDC.ClientSecret, c.OIDC.PrivateKey, c.OIDC.Certificate); err != nil {
		return err
	}
	passthrough := false
	seen := map[string]bool{}
	for _, cl := range c.Clusters {
		if !idRe.MatchString(cl.ID) {
			return fmt.Errorf("cluster id %q must match %s", cl.ID, idRe)
		}
		if seen[cl.ID] {
			return fmt.Errorf("duplicate cluster id %q", cl.ID)
		}
		seen[cl.ID] = true
		if _, err := secure(cl.Server); err != nil {
			return fmt.Errorf("cluster %s: server: %w", cl.ID, err)
		}
		if cl.CAData != "" && cl.CAFile != "" {
			return fmt.Errorf("cluster %s: set only one of caData and caFile", cl.ID)
		}
		switch cl.Strategy {
		case StrategyPassthrough:
			passthrough = true
			if cl.Impersonation != nil || cl.Kiosk != nil {
				return fmt.Errorf("cluster %s: credentials are not used by strategy %s", cl.ID, cl.Strategy)
			}
		case StrategyImpersonation:
			im := cl.Impersonation
			if im == nil || im.Token.IsZero() {
				return fmt.Errorf("cluster %s: impersonation.token is required", cl.ID)
			}
			if hasSystem(im.Prefix) || hasSystem(im.GroupPrefix) {
				return fmt.Errorf("cluster %s: impersonation prefixes must not start with system:", cl.ID)
			}
			for _, g := range im.Groups {
				if hasSystem(g) {
					return fmt.Errorf("cluster %s: impersonation.groups must not contain system: groups", cl.ID)
				}
			}
		case StrategyKiosk:
			if cl.Kiosk == nil || cl.Kiosk.Token.IsZero() {
				return fmt.Errorf("cluster %s: kiosk.token is required", cl.ID)
			}
		default:
			return fmt.Errorf("cluster %s: unknown strategy %q", cl.ID, cl.Strategy)
		}
		if len(cl.Groups) == 0 && len(c.AdminGroups) == 0 {
			return fmt.Errorf("cluster %s: groups is empty and no adminGroups are set, so nobody could see it", cl.ID)
		}
	}
	if ca := c.ClusterApp; ca != nil {
		if ca.ClientID == "" {
			return errors.New("clusterApp.clientId is required")
		}
		if ca.Issuer != "" {
			if _, err := secure(ca.Issuer); err != nil {
				return fmt.Errorf("clusterApp.issuer: %w", err)
			}
		}
		// The same app as login reuses the login tokens, so it needs no credential of its own.
		sameApp := ca.ClientID == c.OIDC.ClientID && (ca.Issuer == "" || ca.Issuer == c.OIDC.Issuer)
		if !sameApp {
			if err := checkAuth("clusterApp", ca.PublicClient, ca.ClientSecret, ca.PrivateKey, ca.Certificate); err != nil {
				return err
			}
		}
	} else if passthrough {
		return errors.New("clusterApp is required when a cluster uses oidc-passthrough")
	}
	if c.Session.Lifetime.D() < time.Minute || c.Session.IdleTimeout.D() < time.Minute {
		return errors.New("session lifetime and idleTimeout must be at least 1m")
	}
	return nil
}

func checkAuth(what string, public bool, secret, key, cert SecretRef) error {
	if public {
		if !secret.IsZero() || !key.IsZero() || !cert.IsZero() {
			return fmt.Errorf("%s: publicClient takes no clientSecret, privateKey or certificate", what)
		}
		return nil
	}
	if secret.IsZero() == key.IsZero() {
		return fmt.Errorf("%s: set exactly one of clientSecret and privateKey", what)
	}
	return nil
}

// CAPEM returns the cluster's CA bundle (nil: system roots).
func (c Cluster) CAPEM() ([]byte, error) {
	switch {
	case c.CAFile != "":
		return os.ReadFile(c.CAFile)
	case c.CAData == "":
		return nil, nil
	case strings.HasPrefix(strings.TrimSpace(c.CAData), "-----BEGIN"):
		return []byte(c.CAData), nil
	}
	return base64.StdEncoding.DecodeString(c.CAData)
}

func (d Duration) MarshalJSON() ([]byte, error) { return json.Marshal(time.Duration(d).String()) }
