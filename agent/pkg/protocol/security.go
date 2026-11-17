package protocol

// ---- security: Kyverno, Trivy Operator, Kubescape (docs/integrations.md) ------------------
//
// Request methods (served through the wire server's extension handlers):
//
//	"security.images"             SecurityImagesParams → SecurityImagesResult
//	"security.reportDetail"       ReportDetailParams → ReportDetail
//	"kyverno.setMode"             KyvernoSetModeParams → KyvernoSetModeResult
//	"kyverno.exceptionTemplate"   KyvernoExceptionTemplateParams → KyvernoExceptionTemplateResult
//	"trivy.rescan"                TrivyRescanParams → SecurityActionResult
//	"kubescape.scanNow"           KubescapeScanNowParams → KubescapeScanNowResult

// Scanner sources of normalized security data (also the problem Source).
const (
	SourceTrivy     = "trivy"
	SourceKubescape = "kubescape"
	SourceKyverno   = "kyverno"
)

type SecurityImagesParams struct {
	Clusters []string `msgpack:"clusters,omitempty" json:"clusters,omitempty"` // empty = every selected cluster
}

// ImageVulnSummary is one image (repository:tag) of the Vulnerabilities overview, deduplicated across workloads
// and clusters: the same image in 40 pods is one row with Workloads 40. When both scanners run the image has one
// row per Source (their verdicts are not merged).
type ImageVulnSummary struct {
	Image      string   `msgpack:"image" json:"image"`
	Repository string   `msgpack:"repository" json:"repository"`
	Tag        string   `msgpack:"tag" json:"tag"`
	Source     string   `msgpack:"source" json:"source"`
	Critical   int      `msgpack:"critical" json:"critical"`
	High       int      `msgpack:"high" json:"high"`
	Medium     int      `msgpack:"medium" json:"medium"`
	Low        int      `msgpack:"low" json:"low"`
	Unknown    int      `msgpack:"unknown" json:"unknown"`
	Workloads  int      `msgpack:"workloads" json:"workloads"`
	Reports    int      `msgpack:"reports" json:"reports"`
	Clusters   []string `msgpack:"clusters" json:"clusters"`
}

type SecurityImagesResult struct {
	// Images are ranked by critical then high counts.
	Images []ImageVulnSummary `msgpack:"images" json:"images"`
}

// ReportDetailParams selects one report (ref.gvr names its kind) and a chunk of its rows. A report with more rows than
// Limit (default 300) is read in chunks: repeat the request with Offset = offset + rows returned until
// offset + rows >= total. The decoded report is cached by resourceVersion, so chunks cost no second fetch.
type ReportDetailParams struct {
	Ref    ObjectRef `msgpack:"ref" json:"ref"`
	Offset int       `msgpack:"offset,omitempty" json:"offset,omitempty"`
	Limit  int       `msgpack:"limit,omitempty" json:"limit,omitempty"`
}

// VulnFinding is one CVE of a Trivy VulnerabilityReport or a Kubescape VulnerabilityManifest.
type VulnFinding struct {
	ID        string `msgpack:"id" json:"id"`
	Severity  string `msgpack:"severity" json:"severity"`
	Package   string `msgpack:"package" json:"package"`
	Installed string `msgpack:"installed" json:"installed"`
	Fixed     string `msgpack:"fixed,omitempty" json:"fixed,omitempty"`
	Title     string `msgpack:"title,omitempty" json:"title,omitempty"`
	Link      string `msgpack:"link,omitempty" json:"link,omitempty"`
}

// ConfigCheckResult is one failing configuration check (Trivy ConfigAuditReport and RBAC/infra assessments,
// Kubescape configuration scans) in the scanner-agnostic shape.
type ConfigCheckResult struct {
	Source      string   `msgpack:"source" json:"source"`
	ID          string   `msgpack:"id" json:"id"`
	Title       string   `msgpack:"title" json:"title"`
	Severity    string   `msgpack:"severity" json:"severity"`
	Category    string   `msgpack:"category,omitempty" json:"category,omitempty"`
	Description string   `msgpack:"description,omitempty" json:"description,omitempty"`
	Messages    []string `msgpack:"messages,omitempty" json:"messages,omitempty"`
	Frameworks  []string `msgpack:"frameworks,omitempty" json:"frameworks,omitempty"` // NSA, MITRE, CIS ...
}

// SecretFinding is one entry of a Trivy ExposedSecretReport.
type SecretFinding struct {
	RuleID   string `msgpack:"ruleId" json:"ruleId"`
	Severity string `msgpack:"severity" json:"severity"`
	Title    string `msgpack:"title" json:"title"`
	Category string `msgpack:"category,omitempty" json:"category,omitempty"`
	Target   string `msgpack:"target,omitempty" json:"target,omitempty"`
	Match    string `msgpack:"match,omitempty" json:"match,omitempty"`
}

// PolicyResult is one result of a Kyverno policy report (wgpolicyk8s.io PolicyReport and openreports.io Report
// are normalized into this shape).
type PolicyResult struct {
	Policy    string `msgpack:"policy" json:"policy"`
	Rule      string `msgpack:"rule,omitempty" json:"rule,omitempty"`
	Result    string `msgpack:"result" json:"result"` // pass | fail | warn | error | skip
	Severity  string `msgpack:"severity,omitempty" json:"severity,omitempty"`
	Message   string `msgpack:"message,omitempty" json:"message,omitempty"`
	Category  string `msgpack:"category,omitempty" json:"category,omitempty"`
	Timestamp int64  `msgpack:"timestamp,omitempty" json:"timestamp,omitempty"` // unix ms
}

// ReportDetail is the lazily fetched body of one report. Kind says which row array is filled:
// "vulnerabilities" (Vulns), "checks" (Checks), "secrets" (Secrets), "policyResults" (Results) or "document"
// (Doc: a YAML rendering of a Kubescape ApplicationProfile or NetworkNeighborhood).
type ReportDetail struct {
	Kind            string              `msgpack:"kind" json:"kind"`
	Source          string              `msgpack:"source" json:"source"`
	Title           string              `msgpack:"title,omitempty" json:"title,omitempty"`
	ResourceVersion string              `msgpack:"resourceVersion,omitempty" json:"resourceVersion,omitempty"`
	Summary         map[string]int      `msgpack:"summary,omitempty" json:"summary,omitempty" tstype:"{ [key: string]: number }"`
	Total           int                 `msgpack:"total" json:"total"`
	Offset          int                 `msgpack:"offset" json:"offset"`
	Vulns           []VulnFinding       `msgpack:"vulns,omitempty" json:"vulns,omitempty"`
	Checks          []ConfigCheckResult `msgpack:"checks,omitempty" json:"checks,omitempty"`
	Secrets         []SecretFinding     `msgpack:"secrets,omitempty" json:"secrets,omitempty"`
	Results         []PolicyResult      `msgpack:"results,omitempty" json:"results,omitempty"`
	Doc             string              `msgpack:"doc,omitempty" json:"doc,omitempty"`
}

// KyvernoSetModeParams switches a policy between Audit and Enforce. Mode is "Audit" or "Enforce". For CEL policies
// (policies.kyverno.io) the whole policy's validationActions change (Rule must be empty); legacy kyverno.io/v1
// policies are read-only and refused.
type KyvernoSetModeParams struct {
	Ref             ObjectRef `msgpack:"ref" json:"ref"`
	Mode            string    `msgpack:"mode" json:"mode"`
	Rule            string    `msgpack:"rule,omitempty" json:"rule,omitempty"`
	UseWriteContext bool      `msgpack:"useWriteContext,omitempty" json:"useWriteContext,omitempty"`
}

type KyvernoSetModeResult struct {
	UsedContext     string `msgpack:"usedContext" json:"usedContext"`
	ResourceVersion string `msgpack:"resourceVersion,omitempty" json:"resourceVersion,omitempty"`
	Mode            string `msgpack:"mode" json:"mode"`
}

// KyvernoResourceRef names the resource a PolicyException is for.
type KyvernoResourceRef struct {
	Kind      string `msgpack:"kind" json:"kind"`
	Namespace string `msgpack:"namespace,omitempty" json:"namespace,omitempty"`
	Name      string `msgpack:"name" json:"name"`
}

// KyvernoExceptionTemplateParams: Policy is the violated policy (ref.gvr tells CEL from legacy), Rule the violated
// rule (legacy policies).
type KyvernoExceptionTemplateParams struct {
	Policy   ObjectRef          `msgpack:"policy" json:"policy"`
	Rule     string             `msgpack:"rule,omitempty" json:"rule,omitempty"`
	Resource KyvernoResourceRef `msgpack:"resource" json:"resource"`
}

// KyvernoExceptionTemplateResult is YAML for the UI's create flow (opened in the editor for review: exceptions
// are security decisions).
type KyvernoExceptionTemplateResult struct {
	YAML       string `msgpack:"yaml" json:"yaml"`
	APIVersion string `msgpack:"apiVersion" json:"apiVersion"`
	Namespace  string `msgpack:"namespace,omitempty" json:"namespace,omitempty"`
}

// TrivyRescanParams deletes a Trivy report so the operator recreates it on its next reconcile.
type TrivyRescanParams struct {
	Ref             ObjectRef `msgpack:"ref" json:"ref"`
	UseWriteContext bool      `msgpack:"useWriteContext,omitempty" json:"useWriteContext,omitempty"`
}

type SecurityActionResult struct {
	UsedContext string `msgpack:"usedContext" json:"usedContext"`
}

type KubescapeScanNowParams struct {
	Cluster         string `msgpack:"cluster" json:"cluster"`
	UseWriteContext bool   `msgpack:"useWriteContext,omitempty" json:"useWriteContext,omitempty"`
}

// KubescapeScanNowResult lists the Jobs created from the operator's scan CronJobs (namespace/name).
type KubescapeScanNowResult struct {
	UsedContext string   `msgpack:"usedContext" json:"usedContext"`
	Jobs        []string `msgpack:"jobs" json:"jobs"`
}
