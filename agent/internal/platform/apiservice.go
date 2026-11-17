package platform

import (
	"fmt"
	"time"

	"github.com/zyno-io/v8s/agent/internal/ingest"
	"github.com/zyno-io/v8s/agent/internal/problems"
	"k8s.io/apimachinery/pkg/runtime"
)

// APIServiceFacts: Available condition and the backing Service ("" for local APIServices).
type APIServiceFacts struct {
	Available ingest.CondFact
	Service   string // namespace/name
}

// APIService row cells: service, available.
const (
	asCellService = iota
	asCellAvailable
)

// GRAPIServices is the version-less key of apiregistration APIServices.
const GRAPIServices = "apiregistration.k8s.io/apiservices"

func decodeAPIService(raw []byte) (runtime.Object, error) {
	var w struct {
		meta
		Spec struct {
			Service *struct {
				Namespace string `json:"namespace"`
				Name      string `json:"name"`
			} `json:"service"`
		} `json:"spec"`
		Status struct {
			Conditions []condition `json:"conditions"`
		} `json:"status"`
	}
	if err := decodeInto(raw, &w); err != nil {
		return nil, err
	}
	f := &APIServiceFacts{Available: findCond(w.Status.Conditions, "Available")}
	svc := "Local"
	if w.Spec.Service != nil {
		f.Service = w.Spec.Service.Namespace + "/" + w.Spec.Service.Name
		svc = f.Service
	}
	r := w.newRow(2)
	r.PutS(svc)
	switch f.Available.Status {
	case "True":
		r.PutS("True")
		r.SetTone(ingest.ToneSuccess)
	case "False":
		r.PutS("False (" + f.Available.Reason + ")")
		r.SetTone(ingest.ToneDanger)
	default:
		r.PutS("Unknown")
		r.SetTone(ingest.ToneWarning)
	}
	r.SetFacts(f)
	return w.obj(r), nil
}

func init() {
	register(kind{group: "apiregistration.k8s.io", resource: "apiservices", kind: "APIService", versions: []string{"v1"},
		cols:   []ingest.Col{ingest.ColStr("service", "Service", 260), ingest.ColStr("available", "Available", 200), ingest.ColTone(), ingest.ColAge()},
		decode: decodeAPIService})
	problems.Register(&problems.Detector{Class: "apiservice.unavailable", Family: problems.FamPlatform, Default: true, Severity: problems.SevError,
		Title: "Unavailable APIServices", Inputs: []string{GRAPIServices},
		Evaluate: func(c *problems.Ctx, k problems.Key, r *ingest.Row) []problems.Draft {
			f, _ := r.Facts().(*APIServiceFacts)
			if f == nil || f.Available.Status != "False" {
				return nil
			}
			sub := c.SubjectOf(k.GR, r)
			return []problems.Draft{{Subject: sub, Title: fmt.Sprintf("APIService %s is unavailable: %s", sub.Name, f.Available.Reason), Message: f.Available.Message,
				Since: firstNZ(f.Available.Since, r.CreatedMs()), Grace: time.Minute,
				Evidence: serviceRef(f.Service)}}
		}})
}

func serviceRef(nsName string) []problems.Ref {
	for i := 0; i < len(nsName); i++ {
		if nsName[i] == '/' {
			return []problems.Ref{{Kind: "Service", NS: nsName[:i], Name: nsName[i+1:]}}
		}
	}
	return nil
}
