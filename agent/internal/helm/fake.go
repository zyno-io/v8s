package helm

import (
	"fmt"
	"io"
	"strconv"
	"time"

	"helm.sh/helm/v4/pkg/action"
	chartcommon "helm.sh/helm/v4/pkg/chart/common"
	chart "helm.sh/helm/v4/pkg/chart/v2"
	kubefake "helm.sh/helm/v4/pkg/kube/fake"
	"helm.sh/helm/v4/pkg/release/common"
	releasev1 "helm.sh/helm/v4/pkg/release/v1"
	"helm.sh/helm/v4/pkg/storage"
	"helm.sh/helm/v4/pkg/storage/driver"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
)

// NewFakeActionConfig returns an action configuration whose release records live in cs's Secrets and whose
// Kubernetes client only prints, so rollback, uninstall and retry in fake mode touch release records only.
func NewFakeActionConfig(cs kubernetes.Interface, namespace string) (*action.Configuration, error) {
	cfg := action.NewConfiguration()
	cfg.Releases = storage.Init(driver.NewSecrets(cs.CoreV1().Secrets(namespace)))
	cfg.KubeClient = &kubefake.PrintingKubeClient{Out: io.Discard, LogOutput: io.Discard}
	cfg.Capabilities = chartcommon.DefaultCapabilities
	cfg.HookOutputFunc = func(_, _, _ string) io.Writer { return io.Discard }
	return cfg, nil
}

// ---- demo releases ----------------------------------------------------------------------------

type demoRev struct {
	status   string
	ago      time.Duration
	replicas int
	tag      string
	desc     string
}

type demoRelease struct {
	ns, name, chart, chartVersion string
	flux                          [2]string // HelmRelease name, namespace
	revs                          []demoRev
}

var demoReleases = []demoRelease{
	{ns: "podinfo", name: "podinfo", chart: "podinfo", chartVersion: "6.7.1", revs: []demoRev{
		{"superseded", 72 * time.Hour, 1, "6.6.0", "Install complete"},
		{"superseded", 30 * time.Hour, 2, "6.7.0", "Upgrade complete"},
		{"deployed", 2 * time.Hour, 3, "6.7.1", "Upgrade complete"},
	}},
	{ns: "default", name: "broken-release", chart: "web", chartVersion: "1.4.0", revs: []demoRev{
		{"superseded", 48 * time.Hour, 2, "1.3.0", "Install complete"},
		{"failed", 3 * time.Hour, 2, "1.4.0", "Upgrade \"broken-release\" failed: timed out waiting for the condition"},
	}},
	{ns: "team-0", name: "api", chart: "web", chartVersion: "1.4.0", revs: []demoRev{
		{"superseded", 96 * time.Hour, 2, "2.0.0", "Install complete"},
		{"deployed", 20 * time.Hour, 4, "2.1.0", "Upgrade complete"},
	}},
	{ns: "team-1", name: "worker", chart: "worker", chartVersion: "0.9.2", revs: []demoRev{
		{"deployed", 200 * time.Hour, 2, "0.9.2", "Install complete"},
	}},
	{ns: "team-2", name: "redis", chart: "redis", chartVersion: "19.1.0", revs: []demoRev{
		{"superseded", 400 * time.Hour, 1, "7.2.4", "Install complete"},
		{"superseded", 300 * time.Hour, 1, "7.2.5", "Upgrade complete"},
		{"superseded", 100 * time.Hour, 2, "7.2.5", "Upgrade complete"},
		{"deployed", 10 * time.Hour, 3, "7.4.0", "Upgrade complete"},
	}},
	{ns: "team-3", name: "stuck-upgrade", chart: "web", chartVersion: "1.4.0", revs: []demoRev{
		{"deployed", 60 * time.Hour, 2, "1.3.0", "Install complete"},
		{"pending-upgrade", 40 * time.Minute, 3, "1.4.0", "Preparing upgrade"},
	}},
	{ns: "monitoring", name: "kube-prometheus-stack", chart: "kube-prometheus-stack", chartVersion: "62.3.0",
		flux: [2]string{"kube-prometheus-stack", "flux-system"}, revs: []demoRev{
			{"superseded", 120 * time.Hour, 1, "v0.75.0", "Install complete"},
			{"deployed", 6 * time.Hour, 1, "v0.76.0", "Upgrade complete"},
		}},
	{ns: "team-4", name: "cleanup", chart: "web", chartVersion: "1.2.0", revs: []demoRev{
		{"superseded", 500 * time.Hour, 1, "1.2.0", "Install complete"},
		{"uninstalled", 80 * time.Hour, 1, "1.2.0", "Uninstallation complete"},
	}},
	{ns: "team-5", name: "ingress-nginx", chart: "ingress-nginx", chartVersion: "4.11.2", revs: []demoRev{
		{"superseded", 250 * time.Hour, 2, "1.11.1", "Install complete"},
		{"deployed", 30 * time.Hour, 2, "1.11.2", "Upgrade complete"},
	}},
	{ns: "team-6", name: "cache", chart: "redis", chartVersion: "19.1.0", revs: []demoRev{
		{"deployed", 15 * time.Hour, 1, "7.4.0", "Install complete"},
	}},
}

func demoChart(d *demoRelease, r demoRev) *chart.Chart {
	const deploy = `apiVersion: apps/v1
kind: Deployment
metadata:
  name: {{ .Release.Name }}
spec:
  replicas: {{ .Values.replicaCount }}
  selector:
    matchLabels:
      app: {{ .Release.Name }}
  template:
    metadata:
      labels:
        app: {{ .Release.Name }}
    spec:
      containers:
        - name: app
          image: "{{ .Values.image.repository }}:{{ .Values.image.tag }}"
`
	return &chart.Chart{
		Metadata: &chart.Metadata{APIVersion: "v2", Name: d.chart, Version: d.chartVersion, AppVersion: r.tag, Description: "Demo chart " + d.chart},
		Templates: []*chartcommon.File{
			{Name: "templates/deployment.yaml", Data: []byte(deploy)},
			{Name: "templates/NOTES.txt", Data: []byte("Release {{ .Release.Name }} installed.\n")},
		},
		Values: map[string]any{
			"replicaCount": 1,
			"image":        map[string]any{"repository": "ghcr.io/demo/" + d.chart, "tag": "latest"},
			"service":      map[string]any{"type": "ClusterIP", "port": 80},
		},
	}
}

func demoManifest(d *demoRelease, r demoRev) string {
	return fmt.Sprintf(`---
# Source: %[1]s/templates/deployment.yaml
apiVersion: apps/v1
kind: Deployment
metadata:
  name: %[2]s
  namespace: %[3]s
spec:
  replicas: %[4]d
  selector:
    matchLabels:
      app: %[2]s
  template:
    metadata:
      labels:
        app: %[2]s
    spec:
      containers:
        - name: app
          image: "ghcr.io/demo/%[1]s:%[5]s"
`, d.chart, d.name, d.ns, r.replicas, r.tag)
}

// DemoSecrets returns the release Secrets of the demo releases for the fake world: real Helm payloads
// (gzip + base64 JSON of a small chart) with the labels Helm writes. Revision timestamps are relative to now.
func DemoSecrets(cluster string, now time.Time) []*corev1.Secret {
	var out []*corev1.Secret
	for i := range demoReleases {
		d := &demoReleases[i]
		for n, r := range d.revs {
			version := n + 1
			at := now.Add(-r.ago)
			rel := &releasev1.Release{
				Name: d.name, Namespace: d.ns, Version: version,
				Chart:  demoChart(d, r),
				Config: map[string]any{"replicaCount": r.replicas, "image": map[string]any{"tag": r.tag}},
				Info: &releasev1.Info{FirstDeployed: now.Add(-d.revs[0].ago), LastDeployed: at,
					Status: common.Status(r.status), Description: r.desc, Notes: "Release " + d.name + " installed.\n"},
				Manifest: demoManifest(d, r),
				Hooks: []*releasev1.Hook{{
					Name: d.name + "-migrate", Kind: "Job", Path: d.chart + "/templates/migrate.yaml",
					Manifest: "apiVersion: batch/v1\nkind: Job\nmetadata:\n  name: " + d.name + "-migrate\n",
					Events:   []releasev1.HookEvent{releasev1.HookPreUpgrade, releasev1.HookPreInstall}, Weight: -5,
				}},
			}
			enc, err := EncodeRelease(rel)
			if err != nil {
				continue
			}
			lbl := map[string]string{
				LabelName: d.name, LabelOwner: "helm", LabelStatus: r.status, LabelVersion: strconv.Itoa(version),
				LabelCreatedAt: strconv.FormatInt(at.Unix(), 10), LabelModified: strconv.FormatInt(at.Unix(), 10),
			}
			if d.flux[0] != "" {
				lbl[FluxNameLabel], lbl[FluxNSLabel] = d.flux[0], d.flux[1]
			}
			out = append(out, &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name: secretName(d.name, version), Namespace: d.ns, Labels: lbl,
					UID:               types.UID(fmt.Sprintf("demo-%s-%s-%s-v%d", cluster, d.ns, d.name, version)),
					CreationTimestamp: metav1.NewTime(at), ResourceVersion: "1",
				},
				Type: corev1.SecretType(ReleaseType),
				Data: map[string][]byte{ReleaseDataKey: []byte(enc)},
			})
		}
	}
	return out
}
