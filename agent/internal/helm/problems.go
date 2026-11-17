package helm

import (
	"fmt"
	"strings"
	"time"

	"github.com/zyno-io/v8s/agent/internal/ingest"
	"github.com/zyno-io/v8s/agent/internal/problems"
	"github.com/zyno-io/v8s/agent/pkg/protocol"
)

// release row cells: revision, status, updated, managedBy.
const (
	cellRevision = iota
	cellStatus
	cellUpdated
	cellManagedBy
)

// ReleaseGR is the problems GR of the release view.
var ReleaseGR = problems.GR(protocol.HelmGVR)

func releaseSubject(r *ingest.Row) problems.Subject {
	return problems.Subject{Group: "helm.sh", Kind: "Release", NS: r.GetNamespace(), Name: r.Name(), GVR: protocol.HelmGVR}
}

func init() {
	problems.Register(&problems.Detector{Class: "helm.failed", Family: problems.FamHelm, Default: true, Severity: problems.SevError, Title: "Failed Helm releases",
		Inputs: []string{ReleaseGR},
		Evaluate: func(c *problems.Ctx, k problems.Key, r *ingest.Row) []problems.Draft {
			if r.StrCell(cellStatus) != "failed" {
				return nil
			}
			sub := releaseSubject(r)
			msg := "latest revision " + fmt.Sprint(r.Cell(cellRevision).N) + " failed"
			if m := r.StrCell(cellManagedBy); m != "" {
				msg += " (managed by " + strings.TrimPrefix(m, "flux:") + ")"
			}
			return []problems.Draft{{Subject: sub, Title: "Helm release " + sub.Name + " failed", Message: msg, Since: r.Cell(cellUpdated).N}}
		}})
	problems.Register(&problems.Detector{Class: "helm.stuck-pending", Family: problems.FamHelm, Default: true, Severity: problems.SevWarning, Title: "Helm releases stuck pending",
		Inputs: []string{ReleaseGR},
		Evaluate: func(c *problems.Ctx, k problems.Key, r *ingest.Row) []problems.Draft {
			st := r.StrCell(cellStatus)
			if !strings.HasPrefix(st, "pending-") {
				return nil
			}
			sub := releaseSubject(r)
			return []problems.Draft{{Subject: sub, Title: "Helm release " + sub.Name + " is stuck in " + st, Message: "revision " + fmt.Sprint(r.Cell(cellRevision).N) + " has not finished",
				Since: r.Cell(cellUpdated).N, Grace: 15 * time.Minute}}
		}})
}
