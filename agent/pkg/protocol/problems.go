package protocol

// ---- problems (docs/problems.md) ---------------------------------------------------------

// ProblemsGVR is the synthetic resource the problems engine serves through the ordinary view engine
// (view.subscribe with gvr "v8s/v1/problems"). Columns after cluster, name (the subject's name) and namespace:
// severity (critical|error|warning|info), severityRank (0 critical ... 3 info), class, family, kind, title,
// message, since, lastChanged, count, source, acked ("true"|"false"), statusTone. Filters: family, class,
// severity, cluster, namespace, text (subject name), acked. Default sort: severityRank, lastChanged desc, name.
const ProblemsGVR = "v8s/v1/problems"

// ProblemCount is the live number of problems of a class: Total includes the acknowledged ones.
type ProblemCount struct {
	Total int `msgpack:"total" json:"total"`
	Acked int `msgpack:"acked" json:"acked"`
}

// ProblemClassInfo describes one problem class of the registry.
type ProblemClassInfo struct {
	Class    string `msgpack:"class" json:"class"`
	Family   string `msgpack:"family" json:"family"`
	Title    string `msgpack:"title" json:"title"`
	Severity string `msgpack:"severity" json:"severity"` // default severity
	Default  bool   `msgpack:"default" json:"default"`   // enabled by default
	// Enabled is the effective global setting (settings.problemPrefs over the default).
	Enabled bool `msgpack:"enabled" json:"enabled"`
	// Counts are the live problems per cluster id (only clusters with at least one).
	Counts map[string]ProblemCount `msgpack:"counts" json:"counts"`
	// EnabledIn lists, per cluster id, the effective setting when a per-cluster override differs from Enabled.
	EnabledIn map[string]bool `msgpack:"enabledIn,omitempty" json:"enabledIn,omitempty"`
}

// ProblemUnavailable says a check cannot run in a cluster (a baseline kind that cannot be listed).
type ProblemUnavailable struct {
	Cluster string `msgpack:"cluster" json:"cluster"`
	Kind    string `msgpack:"kind" json:"kind"`
	Reason  string `msgpack:"reason" json:"reason"`
}

type ProblemsClassesResult struct {
	Classes     []ProblemClassInfo   `msgpack:"classes" json:"classes"`
	Unavailable []ProblemUnavailable `msgpack:"unavailable" json:"unavailable"`
}

type ProblemsEvidenceParams struct {
	ID string `msgpack:"id" json:"id"`
}

type ProblemCondition struct {
	Type    string `msgpack:"type" json:"type"`
	Status  string `msgpack:"status" json:"status"`
	Reason  string `msgpack:"reason,omitempty" json:"reason,omitempty"`
	Message string `msgpack:"message,omitempty" json:"message,omitempty"`
	Since   int64  `msgpack:"since,omitempty" json:"since,omitempty"` // unix ms
}

type ProblemContainer struct {
	Name       string `msgpack:"name" json:"name"`
	State      string `msgpack:"state" json:"state"` // "waiting" | "running" | "terminated"
	Reason     string `msgpack:"reason,omitempty" json:"reason,omitempty"`
	Message    string `msgpack:"message,omitempty" json:"message,omitempty"`
	ExitCode   int    `msgpack:"exitCode,omitempty" json:"exitCode,omitempty"`
	Restarts   int    `msgpack:"restarts" json:"restarts"`
	Since      int64  `msgpack:"since,omitempty" json:"since,omitempty"`
	LastReason string `msgpack:"lastReason,omitempty" json:"lastReason,omitempty"` // lastState.terminated.reason
	Pod        string `msgpack:"pod,omitempty" json:"pod,omitempty"`
}

type ProblemEvent struct {
	Reason   string `msgpack:"reason" json:"reason"`
	Message  string `msgpack:"message" json:"message"`
	Count    int    `msgpack:"count" json:"count"`
	LastSeen int64  `msgpack:"lastSeen" json:"lastSeen"`
	Object   string `msgpack:"object,omitempty" json:"object,omitempty"` // "Kind/name" when the event is about a related object
}

// ProblemRef is a related object (a rolled-up pod, an owner, a Service).
type ProblemRef struct {
	GVR       string `msgpack:"gvr,omitempty" json:"gvr,omitempty"`
	Kind      string `msgpack:"kind" json:"kind"`
	Namespace string `msgpack:"namespace,omitempty" json:"namespace,omitempty"`
	Name      string `msgpack:"name" json:"name"`
	Note      string `msgpack:"note,omitempty" json:"note,omitempty"`
}

// ProblemsEvidenceResult: the triggering conditions and container states (from the live object), the last 5
// related Warning events (from the agent's event cache) and related objects.
type ProblemsEvidenceResult struct {
	ID         string             `msgpack:"id" json:"id"`
	Cluster    string             `msgpack:"cluster" json:"cluster"`
	Class      string             `msgpack:"class" json:"class"`
	Subject    ProblemRef         `msgpack:"subject" json:"subject"`
	Title      string             `msgpack:"title" json:"title"`
	Message    string             `msgpack:"message,omitempty" json:"message,omitempty"`
	Since      int64              `msgpack:"since" json:"since"`
	Conditions []ProblemCondition `msgpack:"conditions" json:"conditions"`
	Containers []ProblemContainer `msgpack:"containers" json:"containers"`
	Events     []ProblemEvent     `msgpack:"events" json:"events"`
	Related    []ProblemRef       `msgpack:"related" json:"related"`
	// Error is set when the live object could not be read (the cached parts are still returned).
	Error string `msgpack:"error,omitempty" json:"error,omitempty"`
}

// ProblemsAckParams acknowledges a problem: it is hidden (acked=true) until its severity rises, its count grows
// past a threshold (+50%) or it resolves and recurs. Until (unix ms, optional) makes it a snooze.
type ProblemsAckParams struct {
	ID    string `msgpack:"id" json:"id"`
	Until int64  `msgpack:"until,omitempty" json:"until,omitempty"`
}

type ProblemsUnackParams struct {
	ID string `msgpack:"id" json:"id"`
}

// ProblemPrefs is the shape of settings.problemPrefs: {classes: {class: bool}, clusters: {clusterId: {class: bool}}}.
// A per-cluster entry wins over the global one, which wins over the class default.
