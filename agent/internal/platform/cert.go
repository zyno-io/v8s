package platform

import (
	"fmt"
	"time"

	"github.com/zyno-io/v8s/agent/internal/ingest"
	"github.com/zyno-io/v8s/agent/internal/problems"
	"k8s.io/apimachinery/pkg/runtime"
)

// GRCertificates is the version-less key of cert-manager Certificates (the gate of cert.expiring).
const GRCertificates = "cert-manager.io/certificates"

// CertFacts: the Ready condition and the validity window, unix ms.
type CertFacts struct {
	Ready     ingest.CondFact
	NotAfter  int64
	Renewal   int64
	IssuerRef string
}

// ExpiryWindow is how far ahead cert.expiring warns.
const ExpiryWindow = 14 * 24 * time.Hour

// UrgentWindow is how close to expiry cert.expiring rises from warning to error.
const UrgentWindow = 3 * 24 * time.Hour

func parseT(s string) int64 {
	if s == "" {
		return 0
	}
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t.UnixMilli()
	}
	return 0
}

// cert row cells: ready, notAfter, issuer.
func decodeCert(raw []byte) (runtime.Object, error) {
	var w struct {
		meta
		Spec struct {
			IssuerRef struct {
				Kind string `json:"kind"`
				Name string `json:"name"`
			} `json:"issuerRef"`
		} `json:"spec"`
		Status struct {
			Conditions  []condition `json:"conditions"`
			NotAfter    string      `json:"notAfter"`
			RenewalTime string      `json:"renewalTime"`
		} `json:"status"`
	}
	if err := decodeInto(raw, &w); err != nil {
		return nil, err
	}
	f := &CertFacts{Ready: findCond(w.Status.Conditions, "Ready"), NotAfter: parseT(w.Status.NotAfter), Renewal: parseT(w.Status.RenewalTime)}
	if w.Spec.IssuerRef.Name != "" {
		f.IssuerRef = w.Spec.IssuerRef.Kind + "/" + w.Spec.IssuerRef.Name
	}
	r := w.newRow(3)
	ready := "Unknown"
	tone := ingest.ToneWarning
	switch f.Ready.Status {
	case "True":
		ready, tone = "True", ingest.ToneSuccess
	case "False":
		ready, tone = "False", ingest.ToneDanger
	}
	r.PutS(ready)
	r.PutN(f.NotAfter)
	r.PutS(f.IssuerRef)
	r.SetTone(tone)
	r.SetFacts(f)
	return w.obj(r), nil
}

func evalCert(c *problems.Ctx, k problems.Key, r *ingest.Row) []problems.Draft {
	f, _ := r.Facts().(*CertFacts)
	if f == nil {
		return nil
	}
	sub := c.SubjectOf(k.GR, r)
	now := c.Now()
	var out []problems.Draft
	if f.Ready.Status == "False" {
		out = append(out, problems.Draft{Subject: sub, Disc: "not-ready", Title: fmt.Sprintf("Certificate %s is not ready: %s", sub.Name, f.Ready.Reason),
			Message: f.Ready.Message, Since: firstNZ(f.Ready.Since, r.CreatedMs()), Grace: 5 * time.Minute})
	}
	if f.NotAfter != 0 {
		left := time.UnixMilli(f.NotAfter).Sub(now)
		title := fmt.Sprintf("Certificate %s expires in %s", sub.Name, days(left))
		sev := problems.SevWarning
		if left <= UrgentWindow {
			sev = problems.SevError
		}
		if left <= 0 {
			title = fmt.Sprintf("Certificate %s expired %s ago", sub.Name, days(-left))
		}
		// begins when the window opens (a deadline wakes the key then); it is evaluated again where the severity
		// or the wording changes: urgent, the last day, expiry
		na := time.UnixMilli(f.NotAfter)
		out = append(out, problems.Draft{Subject: sub, Disc: "expiring", Severity: sev, Title: title, Message: "notAfter " + na.UTC().Format(time.RFC3339),
			Since: f.NotAfter - ExpiryWindow.Milliseconds(), Grace: time.Nanosecond,
			Wake: []time.Time{na.Add(-UrgentWindow), na.Add(-24 * time.Hour), na}})
	}
	return out
}

func days(d time.Duration) string {
	if d >= 48*time.Hour {
		return fmt.Sprintf("%d days", int(d.Hours()/24))
	}
	return fmt.Sprintf("%d hours", int(d.Hours()))
}

func init() {
	register(kind{group: "cert-manager.io", resource: "certificates", kind: "Certificate", versions: []string{"v1"}, namespaced: true, short: []string{"cert", "certs"},
		cols:   []ingest.Col{ingest.ColDict("ready", "Ready", 80), ingest.ColTimeCell("notAfter", "Expires", 100), ingest.ColStr("issuer", "Issuer", 200), ingest.ColTone(), ingest.ColAge()},
		decode: decodeCert})
	problems.Register(&problems.Detector{Class: "cert.expiring", Family: problems.FamNetwork, Default: true, Severity: problems.SevWarning,
		Title: "cert-manager Certificates not ready or expiring within 14 days", Inputs: []string{GRCertificates}, Gate: GRCertificates, Evaluate: evalCert})
}
