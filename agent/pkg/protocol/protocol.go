// Package protocol is the wire contract between the v8s agent and its UI clients.
// It is the source of truth: TypeScript types are generated from it with tygo
// (`yarn protocol:gen` writes packages/client/src/protocol.gen.ts). See docs/protocol.md.
//
// Transport: one binary WebSocket per tab ("data socket"). Every frame is
//
//	[u8 kind][u32 channel BE][u32 seq BE][msgpack payload]
//
// channel is the subscription id (qid) for patches, the request id for requests and
// responses, and 0 otherwise. seq is a per-socket, monotonically increasing number that the
// agent assigns to every frame it sends; the client acknowledges patch frames by seq.
// Maximum frame size is 128 KiB; larger payloads are split across consecutive patches.
package protocol

// Version is checked during the handshake; client and agent must match exactly.
const Version = 1

// Subprotocol is offered in Sec-WebSocket-Protocol together with "token.<hex>" on desktop.
const Subprotocol = "v8s.v1"

// FrameKind is the first byte of every frame.
type FrameKind uint8

const (
	// KindHello (agent → client) is the first frame after the upgrade. Payload: Hello.
	KindHello FrameKind = 1
	// KindRequest (client → agent). channel = request id. Payload: Request.
	KindRequest FrameKind = 2
	// KindResponse (agent → client). channel = request id. Payload: Response.
	KindResponse FrameKind = 3
	// KindPatch (agent → client). channel = qid. Payload: ViewPatch. Must be acknowledged.
	KindPatch FrameKind = 4
	// KindAck (client → agent). seq = the acknowledged patch frame's seq. No payload.
	KindAck FrameKind = 5
	// KindDict (agent → client). Payload: DictAdd. Always sent before the first patch that uses the entries.
	KindDict FrameKind = 6
	// KindClusterState (agent → client). Payload: ClusterStates. Sent on any cluster state change.
	KindClusterState FrameKind = 7
	// KindObject (agent → client). channel = oid (chosen by the client in object.subscribe). Payload: ObjectFrame.
	// Sent once after object.subscribe and again on every change of the object. Not acknowledged.
	KindObject FrameKind = 8
	// KindJob (agent → client). channel = 0. Payload: JobFrame (the job's full current state). Sent on every
	// job change to every connected data socket; jobs.list returns the same states for a (re)connecting client.
	// Not acknowledged.
	KindJob FrameKind = 9
)

// Hello is sent by the agent right after the WebSocket upgrade.
type Hello struct {
	Version  int    `msgpack:"version" json:"version"`
	AgentVer string `msgpack:"agentVersion" json:"agentVersion"`
	Mode     string `msgpack:"mode" json:"mode"` // "desktop" | "hosted" | "fake" ("desktop" also covers `v8s serve`)
}

// Request is a client call. Method names and their params/result types:
//
//	"clusters.list"      → ClustersListResult (no params)
//	"clusters.select"    ClustersSelectParams → ClusterStates
//	"clusters.elevate"   ClustersElevateParams → ClusterStates
//	"view.subscribe"     ViewSubscribeParams → ViewSubscribeResult
//	"view.update"        ViewUpdateParams → (empty)
//	"view.unsubscribe"   ViewUnsubscribeParams → (empty)
//	"kinds.list"         → KindsListResult (no params)
//	"object.get"         ObjectGetParams → ObjectPayload
//	"object.subscribe"   ObjectSubscribeParams → (empty); frames of KindObject follow on channel oid
//	"object.unsubscribe" ObjectUnsubscribeParams → (empty)
//	"object.dryRun"      ObjectDryRunParams → ObjectDryRunResult
//	"object.apply"       ObjectApplyParams → ObjectApplyResult
//	"object.delete"      ObjectDeleteParams → MutationResult
//	"object.action"      ObjectActionParams → MutationResult
//	"access.check"       AccessCheckParams → AccessCheckResult
//	"settings.get"       → Settings (no params)
//	"settings.set"       SettingsSetParams → Settings (the merged document)
//	"search"             SearchParams → SearchResult
//	"stream.open"        StreamOpenParams → StreamOpenResult (see StreamSubprotocolPrefix)
//	"portforward.list"   → PortForwardListResult (no params)
//	"portforward.stop"   PortForwardStopParams → (empty)
//	"jobs.start"         JobsStartParams → JobsStartResult
//	"jobs.list"          → JobsListResult (no params)
//	"jobs.cancel"        JobsCancelParams → (empty)
//	"drain.preflight"    DrainPreflightParams → DrainPreflightResult
//	"flux.inventory"     FluxInventoryParams → FluxInventoryResult
//	"flux.graph"         FluxGraphParams → FluxGraphResult
//	"helm.release"       HelmReleaseParams → HelmRelease
//	"helm.diff"          HelmDiffParams → HelmDiffResult
//	"problems.classes"   → ProblemsClassesResult (no params)
//	"problems.evidence"  ProblemsEvidenceParams → ProblemsEvidenceResult
//	"problems.ack"       ProblemsAckParams → (empty)
//	"problems.unack"     ProblemsUnackParams → (empty)
//	"hubble.status"      HubbleStatusParams → HubbleStatus
//	"hubble.serviceMap"  HubbleServiceMapParams → HubbleServiceMap
//	"hubble.drops"       HubbleDropsParams → HubbleDrops
//	"update.status"      → UpdateStatus (no params)
type Request struct {
	Method string `msgpack:"method" json:"method"`
	Params any    `msgpack:"params,omitempty" json:"params,omitempty" tstype:"unknown"`
}

// Response answers a Request with the same channel id. Exactly one of Result or Error is set.
type Response struct {
	Result any        `msgpack:"result,omitempty" json:"result,omitempty" tstype:"unknown"`
	Error  *ErrorBody `msgpack:"error,omitempty" json:"error,omitempty"`
}

type ErrorBody struct {
	// Code is one of "bad_request" | "not_found" | "forbidden" | "unavailable" | "internal" |
	// "conflict" (object.apply update lost a resourceVersion race; Live carries the current object) |
	// "write_unavailable" (the linked write context was rejected with 401/403; Message is the server's) |
	// "invalid" (the API server rejected the object, e.g. validation or admission) |
	// "write_confirm" (the read context is denied, a write context is linked but autoSwitchWrite is off: ask the user, then retry with useWriteContext) |
	// "link_mismatch" (the linked write context reaches a different cluster than the read context: kube-system uids differ) |
	// "gitops_managed" (a Helm action on a Flux-managed release; Hint names the right Flux action, "force": true overrides).
	Code    string         `msgpack:"code" json:"code"`
	Message string         `msgpack:"message" json:"message"`
	Hint    string         `msgpack:"hint,omitempty" json:"hint,omitempty"`
	Live    *ObjectPayload `msgpack:"live,omitempty" json:"live,omitempty"`
	// Applied lists the documents of a multi-document object.apply that succeeded before the one that failed.
	Applied []ObjectRef `msgpack:"applied,omitempty" json:"applied,omitempty"`
}

// ---- clusters -------------------------------------------------------------------------

type Cluster struct {
	ID     string   `msgpack:"id" json:"id"`         // stable local id (desktop: the kubeconfig context name)
	Name   string   `msgpack:"name" json:"name"`     // display name
	Server string   `msgpack:"server" json:"server"` // API server URL, for display
	Groups []string `msgpack:"groups" json:"groups"`
	// FixedGroups are the groups the source puts the cluster in (hosted mode: the server's displayGroups). They are
	// also in Groups; the user cannot rename, edit or delete them.
	FixedGroups []string `msgpack:"fixedGroups,omitempty" json:"fixedGroups,omitempty"`
	// WriteContext is the id of the linked write context (settings.links), if any.
	WriteContext string `msgpack:"writeContext,omitempty" json:"writeContext,omitempty"`
}

type ClustersListResult struct {
	Clusters []Cluster `msgpack:"clusters" json:"clusters"`
	// Selected is the remembered selection (restored as pending; the client calls clusters.select to connect).
	Selected []string `msgpack:"selected" json:"selected"`
}

type ClustersSelectParams struct {
	IDs []string `msgpack:"ids" json:"ids"`
}

// ClustersElevateParams runs a selected cluster as its linked write context (Elevated) or back as its read context.
// Either way that cluster's watches, discovery, problems and usage restart cold; other clusters are untouched.
type ClustersElevateParams struct {
	ID       string `msgpack:"id" json:"id"`
	Elevated bool   `msgpack:"elevated" json:"elevated"`
}

// ClusterPhase is the connection lifecycle state of one selected cluster.
type ClusterPhase string

const (
	PhaseConnecting   ClusterPhase = "connecting"
	PhaseAuthRequired ClusterPhase = "auth-required"
	PhaseSyncing      ClusterPhase = "syncing"
	PhaseReady        ClusterPhase = "ready"
	PhaseDegraded     ClusterPhase = "degraded"
	PhaseReconnecting ClusterPhase = "reconnecting"
	PhaseUnreachable  ClusterPhase = "unreachable"
)

// MetricsAvail is whether usage (metrics.k8s.io) can be read from a cluster: "" unknown, "ok", "absent" (no metrics
// API in discovery), "unavailable" (the API is registered but failing or stale), "forbidden" (the user may not read it).
type MetricsAvail string

const (
	MetricsOK          MetricsAvail = "ok"
	MetricsAbsent      MetricsAvail = "absent"
	MetricsUnavailable MetricsAvail = "unavailable"
	MetricsForbidden   MetricsAvail = "forbidden"
)

// CapacityGVR is the synthetic per-cluster capacity view behind the Overview CPU/Memory card.
const CapacityGVR = "v8s/v1/capacity"

type ClusterState struct {
	ID             string       `msgpack:"id" json:"id"`
	Phase          ClusterPhase `msgpack:"phase" json:"phase"`
	Synced         int          `msgpack:"synced" json:"synced"` // rows received so far while syncing
	Message        string       `msgpack:"message,omitempty" json:"message,omitempty"`
	Metrics        MetricsAvail `msgpack:"metrics,omitempty" json:"metrics,omitempty"`               // usage availability; set only while a view polls usage
	MetricsMessage string       `msgpack:"metricsMessage,omitempty" json:"metricsMessage,omitempty"` // why usage is unavailable
	// VPA: some pod of the cluster has its requests set by a VerticalPodAutoscaler (the vpaUpdates annotation); the
	// UI shows the pods VPA column only then. A VPA that is installed but unused, or only in Off mode, leaves it false.
	VPA bool `msgpack:"vpa,omitempty" json:"vpa,omitempty"`
	// Kinds is the cluster's discovery generation: it increases whenever the set of discovered resources changes
	// (a CRD was installed or removed, an aggregated API came or went). The UI re-reads kinds.list when it changes.
	Kinds uint64 `msgpack:"kinds,omitempty" json:"kinds,omitempty"`
	// Elevated: the cluster runs as its linked write context (Cluster.WriteContext) for reads, watches and writes
	// (clusters.elevate) until reverted, stopped after deselection, or the agent restarts. Not persisted.
	Elevated bool `msgpack:"elevated,omitempty" json:"elevated,omitempty"`
	// Loading lists the kinds open views wait for on this cluster: a watch a view holds whose first list has not
	// finished (rows still arriving), and, until the cluster's first discovery, a kind an open view asks for that it
	// cannot place yet. A refused (Forbidden, NotFound) or parked watch is not loading. Sorted; omitted when empty.
	Loading []string `msgpack:"loading,omitempty" json:"loading,omitempty"`
}

type ClusterStates struct {
	States []ClusterState `msgpack:"states" json:"states"`
}

// ---- kinds ----------------------------------------------------------------------------

// Kind describes a resource type the UI can open. kinds.list returns the union over the selected
// clusters' discovery results.
type Kind struct {
	GVR        string   `msgpack:"gvr" json:"gvr"` // "v1/pods", "apps/v1/deployments"
	Group      string   `msgpack:"group" json:"group"`
	Version    string   `msgpack:"version" json:"version"`
	Kind       string   `msgpack:"kind" json:"kind"`
	Plural     string   `msgpack:"plural" json:"plural"`
	ShortNames []string `msgpack:"shortNames" json:"shortNames"` // from discovery: "po", "deploy", …
	Namespaced bool     `msgpack:"namespaced" json:"namespaced"`
	Available  bool     `msgpack:"available" json:"available"`         // true for every listable kind
	Curated    bool     `msgpack:"curated" json:"curated"`             // hand-written columns (mode A) vs Table columns (mode C)
	Clusters   []string `msgpack:"clusters" json:"clusters"`           // selected clusters that serve it
	IPs        bool     `msgpack:"ips,omitempty" json:"ips,omitempty"` // rows carry IPs (IP search, IP-aware filter)
}

type KindsListResult struct {
	Kinds []Kind `msgpack:"kinds" json:"kinds"`
}

// ---- views ----------------------------------------------------------------------------

// ColumnType says how a cell is encoded in a row.
type ColumnType string

const (
	ColDict ColumnType = "dict" // u32 dictionary id (KindDict); 0 = empty
	ColStr  ColumnType = "str"  // inline string
	ColInt  ColumnType = "int"  // integer; cells of unit columns may be nil (no data) or -1 (n/a: no request/limit/node)
	ColTime ColumnType = "time" // unix milliseconds (int); the UI renders relative ages
	ColTone ColumnType = "tone" // small int: 0 neutral, 1 success, 2 warning, 3 danger, 4 info, 5 release
)

type Column struct {
	Key   string     `msgpack:"key" json:"key"`
	Type  ColumnType `msgpack:"type" json:"type"`
	Label string     `msgpack:"label,omitempty" json:"label,omitempty"`
	Width int        `msgpack:"width,omitempty" json:"width,omitempty"` // px hint
	Align string     `msgpack:"align,omitempty" json:"align,omitempty"` // "left" | "right"
	// Unit marks a usage column: "millicores" | "MiB" | "permille". Its int cells may be nil (no data) or -1 (n/a).
	Unit string `msgpack:"unit,omitempty" json:"unit,omitempty"`
	// Short is the label inside the dense usage block ("%R", "req", "alloc").
	Short string `msgpack:"short,omitempty" json:"short,omitempty"`
	// Metrics marks a column whose values come from metrics.k8s.io (needs the subscription's usage flag).
	Metrics bool `msgpack:"metrics,omitempty" json:"metrics,omitempty"`
	// Hidden marks a column hidden until shown from the Columns menu.
	Hidden bool `msgpack:"hidden,omitempty" json:"hidden,omitempty"`
}

type Filter struct {
	Col   string `msgpack:"col" json:"col"`
	Op    string `msgpack:"op" json:"op"` // "eq" | "in" | "contains"
	Value any    `msgpack:"value" json:"value" tstype:"string | string[]"`
}

type Sort struct {
	Col  string `msgpack:"col" json:"col"`
	Desc bool   `msgpack:"desc" json:"desc"`
}

// ViewQuery is normalized by the agent; two equal queries share one materialized view.
type ViewQuery struct {
	Clusters   []string `msgpack:"clusters" json:"clusters"`     // empty = all selected clusters
	GVR        string   `msgpack:"gvr" json:"gvr"`               // "v1/pods"
	Namespaces []string `msgpack:"namespaces" json:"namespaces"` // empty = all
	Text       string   `msgpack:"text" json:"text"`             // substring match on name
	Filters    []Filter `msgpack:"filters" json:"filters"`
	Sort       []Sort   `msgpack:"sort" json:"sort"`
	// Involved, with GVR "v1/events", makes this the view of every event (Normal and Warning) of one object, listed
	// on demand while the view is open (docs/protocol.md "Events of one object"). Clusters and Namespaces are then
	// ignored; Filters, Text and Sort still apply.
	Involved *ObjectRef `msgpack:"involved,omitempty" json:"involved,omitempty"`
	// Background marks a side subscription the user is not looking at (the Pods table's VPA status lookup). It does
	// not keep a watch retrying after a terminal LIST error (Forbidden, NotFound): such a watch is parked as if
	// nobody held it, and a background subscription does not unpark a parked watch.
	Background bool `msgpack:"background,omitempty" json:"background,omitempty"`
}

// MaxObjectEvents is the events of one object an involved view keeps, newest by lastSeen.
const MaxObjectEvents = 200

// Window selects which slice of the sorted view the client holds.
// If AnchorID is set and still in the view, the agent positions the window around it
// (Offset is then relative to the anchor's current index) so inserts above do not shift the viewport.
type Window struct {
	AnchorID uint32 `msgpack:"anchorId,omitempty" json:"anchorId,omitempty"`
	Offset   int    `msgpack:"offset" json:"offset"`
	Limit    int    `msgpack:"limit" json:"limit"`
}

type ViewSubscribeParams struct {
	QID    uint32    `msgpack:"qid" json:"qid"` // chosen by the client, unique per socket
	Gen    uint32    `msgpack:"gen" json:"gen"`
	Query  ViewQuery `msgpack:"query" json:"query"`
	Window Window    `msgpack:"window" json:"window"`
	// Usage asks the agent to poll metrics.k8s.io for this view's namespaces and fill the metrics columns.
	Usage bool `msgpack:"usage,omitempty" json:"usage,omitempty"`
}

type ViewSubscribeResult struct {
	Columns []Column `msgpack:"columns" json:"columns"`
}

// ViewUpdateParams changes the query (bump Gen) and/or the window of an existing subscription.
type ViewUpdateParams struct {
	QID    uint32     `msgpack:"qid" json:"qid"`
	Gen    uint32     `msgpack:"gen" json:"gen"`
	Query  *ViewQuery `msgpack:"query,omitempty" json:"query,omitempty"`
	Window *Window    `msgpack:"window,omitempty" json:"window,omitempty"`
	// Usage changes the subscription's usage flag (nil = unchanged). It never bumps Gen.
	Usage *bool `msgpack:"usage,omitempty" json:"usage,omitempty"`
}

type ViewUnsubscribeParams struct {
	QID uint32 `msgpack:"qid" json:"qid"`
}

// ViewPatch is state-based: the agent diffs the current window against what it last sent
// for this subscription and sends only the difference.
//
//   - Total: rows in the whole view (for the scrollbar).
//   - Offset: index of IDs[0] in the view.
//   - IDs: the full id list of the window, little-endian u32s; present only when membership or
//     order changed since the last patch.
//   - Rows: rows new to the client or changed: [id, version, cell0, cell1, …] in Columns order.
//   - Removed: ids the client may drop from its row cache.
//
// Wire ids are per subscription, monotonic and never reused, so ids from different clusters
// never collide and a replaced object never inherits a selection.
type ViewPatch struct {
	Gen     uint32   `msgpack:"gen" json:"gen"`
	Total   int      `msgpack:"total" json:"total"`
	Offset  int      `msgpack:"offset" json:"offset"`
	IDs     []byte   `msgpack:"ids,omitempty" json:"ids,omitempty" tstype:"Uint8Array"`
	Rows    [][]any  `msgpack:"rows,omitempty" json:"rows,omitempty" tstype:"unknown[][]"`
	Removed []uint32 `msgpack:"removed,omitempty" json:"removed,omitempty"`
}

// DictAdd introduces dictionary entries: Strings[i] has id Base+i. Ids start at 1 per socket.
type DictAdd struct {
	Base    uint32   `msgpack:"base" json:"base"`
	Strings []string `msgpack:"strings" json:"strings"`
}

// ---- pods ------------------------------------------------------------------------------

// PodColumns is the column schema of the "v1/pods" view, in cell order. Other kinds' schemas are
// returned by view.subscribe (curated modules: docs/protocol.md; Table-mode kinds: from the API server).
var PodColumns = []Column{
	{Key: "cluster", Type: ColDict, Label: "Cluster", Width: 110},
	{Key: "name", Type: ColStr, Label: "Name", Width: 320},
	{Key: "namespace", Type: ColDict, Label: "Namespace", Width: 120},
	{Key: "ready", Type: ColInt, Label: "Ready", Width: 64, Align: "right"},           // ready containers
	{Key: "containers", Type: ColInt, Label: "Containers", Width: 84, Align: "right"}, // total containers
	{Key: "status", Type: ColDict, Label: "Status", Width: 120},                       // kubectl-style status reason: Running, CrashLoopBackOff, Pending, …
	{Key: "statusTone", Type: ColTone, Label: "Tone", Width: 40},
	{Key: "restarts", Type: ColInt, Label: "Restarts", Width: 68, Align: "right"},
	{Key: "vpa", Type: ColDict, Label: "VPA", Width: 36}, // name of the VerticalPodAutoscaler that set the pod's requests (vpaUpdates annotation); "" = none
	// Usage columns (one dense block in the UI). Values are ints; nil = no data, -1 = n/a (no request/limit/node).
	{Key: "cpuUse", Type: ColInt, Label: "CPU", Width: 44, Align: "right", Unit: "millicores", Metrics: true},
	{Key: "cpuReq", Type: ColInt, Label: "CPU req", Short: "req", Width: 44, Align: "right", Unit: "millicores", Hidden: true},
	{Key: "cpuLim", Type: ColInt, Label: "CPU lim", Short: "lim", Width: 44, Align: "right", Unit: "millicores", Hidden: true},
	{Key: "cpuPctReq", Type: ColInt, Label: "%CPU/R", Short: "%R", Width: 36, Align: "right", Unit: "permille", Metrics: true},
	{Key: "cpuPctLim", Type: ColInt, Label: "%CPU/L", Short: "%L", Width: 36, Align: "right", Unit: "permille", Metrics: true},
	{Key: "cpuPctNode", Type: ColInt, Label: "%CPU/N", Short: "%N", Width: 36, Align: "right", Unit: "permille", Metrics: true, Hidden: true},
	{Key: "memUse", Type: ColInt, Label: "MEM", Width: 48, Align: "right", Unit: "MiB", Metrics: true},
	{Key: "memReq", Type: ColInt, Label: "MEM req", Short: "req", Width: 48, Align: "right", Unit: "MiB", Hidden: true},
	{Key: "memLim", Type: ColInt, Label: "MEM lim", Short: "lim", Width: 48, Align: "right", Unit: "MiB", Hidden: true},
	{Key: "memPctReq", Type: ColInt, Label: "%MEM/R", Short: "%R", Width: 36, Align: "right", Unit: "permille", Metrics: true},
	{Key: "memPctLim", Type: ColInt, Label: "%MEM/L", Short: "%L", Width: 36, Align: "right", Unit: "permille", Metrics: true},
	{Key: "memPctNode", Type: ColInt, Label: "%MEM/N", Short: "%N", Width: 36, Align: "right", Unit: "permille", Metrics: true, Hidden: true},
	{Key: "node", Type: ColDict, Label: "Node", Width: 120},
	{Key: "ip", Type: ColStr, Label: "IP", Width: 120, Hidden: true}, // pod IPs, comma-separated; sorts numerically
	{Key: "createdAt", Type: ColTime, Label: "Age", Width: 56, Align: "right"},
}

// ---- objects --------------------------------------------------------------------------

// ObjectRef addresses one object. GVR is "v1/pods" or "apps/v1/deployments".
type ObjectRef struct {
	Cluster   string `msgpack:"cluster" json:"cluster"`
	GVR       string `msgpack:"gvr" json:"gvr"`
	Namespace string `msgpack:"namespace,omitempty" json:"namespace,omitempty"`
	Name      string `msgpack:"name,omitempty" json:"name,omitempty"` // empty only for object.dryRun/apply in create mode
	// UID, when set, pins the request to one incarnation of the object: object.delete sends it as a precondition,
	// object.action and update-mode object.apply verify the live uid first and fail with code "conflict" and the
	// message "object was replaced" when the object was deleted and recreated; streams and jobs check it too.
	UID string `msgpack:"uid,omitempty" json:"uid,omitempty"`
}

type ObjectGetParams struct {
	Cluster           string `msgpack:"cluster" json:"cluster"`
	GVR               string `msgpack:"gvr" json:"gvr"`
	Namespace         string `msgpack:"namespace,omitempty" json:"namespace,omitempty"`
	Name              string `msgpack:"name" json:"name"`
	KeepManagedFields bool   `msgpack:"keepManagedFields,omitempty" json:"keepManagedFields,omitempty"`
	// Reveal returns Secret values and the last-applied annotation; false (default) masks values with
	// MaskedValue (keys are kept) and strips kubectl.kubernetes.io/last-applied-configuration.
	Reveal bool `msgpack:"reveal,omitempty" json:"reveal,omitempty"`
}

// MaskedValue replaces Secret data values in a payload fetched without Reveal. An update that still contains it
// keeps the live value of that key.
const MaskedValue = "__v8s_masked__"

// ManagedBy summarizes one managedFields entry: which manager owns which field paths.
type ManagedBy struct {
	Manager   string   `msgpack:"manager" json:"manager"`
	Operation string   `msgpack:"operation" json:"operation"` // "Apply" | "Update"
	Fields    []string `msgpack:"fields" json:"fields"`       // dotted paths, e.g. "spec.replicas", "metadata.labels.app"
}

type ObjectOwner struct {
	Kind string `msgpack:"kind" json:"kind"`
	Name string `msgpack:"name" json:"name"`
}

// GitOps names the controller that manages an object, from Flux and Helm labels.
type GitOps struct {
	Kind      string `msgpack:"kind" json:"kind"` // "Kustomization" | "HelmRelease" | "Helm"
	Name      string `msgpack:"name" json:"name"`
	Namespace string `msgpack:"namespace,omitempty" json:"namespace,omitempty"`
}

// ObjectPayload is a full object as YAML plus the facts the editor needs. managedFields is
// stripped from YAML unless keepManagedFields is set.
type ObjectPayload struct {
	YAML            string        `msgpack:"yaml" json:"yaml"`
	UID             string        `msgpack:"uid" json:"uid"`
	ResourceVersion string        `msgpack:"resourceVersion" json:"resourceVersion"`
	ManagedBy       []ManagedBy   `msgpack:"managedBy" json:"managedBy"`
	Owners          []ObjectOwner `msgpack:"owners" json:"owners"`
	GitOps          *GitOps       `msgpack:"gitops,omitempty" json:"gitops,omitempty"`
}

type ObjectSubscribeParams struct {
	OID               uint32    `msgpack:"oid" json:"oid"` // chosen by the client, unique per socket; the KindObject channel
	Ref               ObjectRef `msgpack:"ref" json:"ref"`
	KeepManagedFields bool      `msgpack:"keepManagedFields,omitempty" json:"keepManagedFields,omitempty"`
	Reveal            bool      `msgpack:"reveal,omitempty" json:"reveal,omitempty"` // as ObjectGetParams.Reveal
}

type ObjectUnsubscribeParams struct {
	OID uint32 `msgpack:"oid" json:"oid"`
}

// ObjectFrame is the KindObject payload: the object's current state, or Deleted (payload fields hold
// the last known state) when it was removed, or Error when the watch failed.
type ObjectFrame struct {
	ObjectPayload
	Deleted bool       `msgpack:"deleted,omitempty" json:"deleted,omitempty"`
	Error   *ErrorBody `msgpack:"error,omitempty" json:"error,omitempty"`
}

type ObjectDryRunParams struct {
	Ref  ObjectRef `msgpack:"ref" json:"ref"`
	YAML string    `msgpack:"yaml" json:"yaml"`
	Mode string    `msgpack:"mode" json:"mode"` // "update" | "create"
	// UseWriteContext runs the dry run through the linked write context (honoured only when a link exists).
	UseWriteContext bool `msgpack:"useWriteContext,omitempty" json:"useWriteContext,omitempty"`
}

// FieldConflict: a changed field path that another field manager owns.
type FieldConflict struct {
	Path    string `msgpack:"path" json:"path"`
	Manager string `msgpack:"manager" json:"manager"`
}

type ObjectDryRunResult struct {
	OK        bool            `msgpack:"ok" json:"ok"`
	Warnings  []string        `msgpack:"warnings" json:"warnings"`
	Error     *ErrorBody      `msgpack:"error,omitempty" json:"error,omitempty"`
	Conflicts []FieldConflict `msgpack:"conflicts" json:"conflicts"`
}

type ObjectApplyParams struct {
	RequestID       string    `msgpack:"requestId" json:"requestId"`
	Ref             ObjectRef `msgpack:"ref" json:"ref"`
	YAML            string    `msgpack:"yaml" json:"yaml"`
	Mode            string    `msgpack:"mode" json:"mode"`                                           // "update" | "create"
	ResourceVersion string    `msgpack:"resourceVersion,omitempty" json:"resourceVersion,omitempty"` // update: the version the user opened
	// UseWriteContext (here and on object.delete / object.action / jobs.start / stream.open) runs the mutation
	// through the linked write context; honoured only when a link exists. It is how the UI retries after a
	// "write_confirm" error (autoSwitchWrite off).
	UseWriteContext bool `msgpack:"useWriteContext,omitempty" json:"useWriteContext,omitempty"`
}

type ObjectApplyResult struct {
	ResourceVersion string      `msgpack:"resourceVersion" json:"resourceVersion"`
	UsedContext     string      `msgpack:"usedContext" json:"usedContext"`
	Applied         []ObjectRef `msgpack:"applied,omitempty" json:"applied,omitempty"` // create mode: every object applied
}

type ObjectDeleteParams struct {
	RequestID       string    `msgpack:"requestId" json:"requestId"`
	Ref             ObjectRef `msgpack:"ref" json:"ref"`
	Propagation     string    `msgpack:"propagation,omitempty" json:"propagation,omitempty"` // "Background" (default) | "Foreground" | "Orphan"
	UseWriteContext bool      `msgpack:"useWriteContext,omitempty" json:"useWriteContext,omitempty"`
}

// ObjectActionParams: Action is "scale" {replicas}, "restart", "cordon", "uncordon", "suspend",
// "resume", "trigger", "rollback" {revision}, and the Flux actions "flux.suspend", "flux.resume",
// "flux.reconcile", "flux.reconcileWithSource", "flux.force" and "flux.reset" (docs/integrations.md).
type ObjectActionParams struct {
	RequestID       string         `msgpack:"requestId" json:"requestId"`
	Ref             ObjectRef      `msgpack:"ref" json:"ref"`
	Action          string         `msgpack:"action" json:"action"`
	Params          map[string]any `msgpack:"params,omitempty" json:"params,omitempty" tstype:"{ [key: string]: unknown }"`
	UseWriteContext bool           `msgpack:"useWriteContext,omitempty" json:"useWriteContext,omitempty"`
}

// MutationResult answers object.delete and object.action.
type MutationResult struct {
	UsedContext     string `msgpack:"usedContext" json:"usedContext"`
	ResourceVersion string `msgpack:"resourceVersion,omitempty" json:"resourceVersion,omitempty"`
	Name            string `msgpack:"name,omitempty" json:"name,omitempty"` // action "trigger": the created Job
	// Token is the reconcile token a Flux action wrote to reconcile.fluxcd.io/requestedAt (and forceAt/resetAt).
	// The object is handled once status.lastHandledReconcileAt equals it.
	Token string `msgpack:"token,omitempty" json:"token,omitempty"`
}

// ---- access ---------------------------------------------------------------------------

type AccessCheck struct {
	Cluster   string `msgpack:"cluster" json:"cluster"`
	Verb      string `msgpack:"verb" json:"verb"`
	GVR       string `msgpack:"gvr" json:"gvr"`
	Namespace string `msgpack:"namespace,omitempty" json:"namespace,omitempty"`
	Name      string `msgpack:"name,omitempty" json:"name,omitempty"`
}

type AccessCheckParams struct {
	Checks []AccessCheck `msgpack:"checks" json:"checks"`
}

type AccessResult struct {
	Allowed  bool `msgpack:"allowed" json:"allowed"`
	ViaWrite bool `msgpack:"viaWrite" json:"viaWrite"` // allowed only through the linked write context, which is used automatically (autoSwitchWrite on)
	// NeedsConfirm: allowed only through the linked write context but autoSwitchWrite is off; the UI asks, then
	// sends the mutation with useWriteContext.
	NeedsConfirm bool   `msgpack:"needsConfirm,omitempty" json:"needsConfirm,omitempty"`
	Reason       string `msgpack:"reason" json:"reason"`
}

type AccessCheckResult struct {
	Results []AccessResult `msgpack:"results" json:"results"`
}

// ---- settings and search --------------------------------------------------------------

type Bookmark struct {
	Name string `msgpack:"name" json:"name"`
	URL  string `msgpack:"url" json:"url"`
}

// Settings is the per-user document persisted by the agent (desktop: <user config dir>/v8s/settings.json).
type Settings struct {
	Groups          map[string][]string `msgpack:"groups" json:"groups"`
	Links           map[string]string   `msgpack:"links" json:"links"` // clusterId → write context id
	AutoSwitchWrite bool                `msgpack:"autoSwitchWrite" json:"autoSwitchWrite"`
	Stars           []string            `msgpack:"stars" json:"stars"` // gvrs
	Bookmarks       []Bookmark          `msgpack:"bookmarks" json:"bookmarks"`
	Selection       []string            `msgpack:"selection" json:"selection"`
	ColumnLayouts   map[string]any      `msgpack:"columnLayouts" json:"columnLayouts" tstype:"{ [key: string]: unknown }"`
	ProblemPrefs    map[string]any      `msgpack:"problemPrefs" json:"problemPrefs" tstype:"{ [key: string]: unknown }"`
	Acks            map[string]any      `msgpack:"acks" json:"acks" tstype:"{ [key: string]: unknown }"`
	// UIPrefs holds UI layout preferences (sidebar pinned state and width); the UI validates the shape.
	UIPrefs map[string]any `msgpack:"uiPrefs" json:"uiPrefs" tstype:"{ [key: string]: unknown }"`
	// NodeShell configures node shell pods per cluster id; "*" is the default for every cluster.
	NodeShell map[string]NodeShellSettings `msgpack:"nodeShell" json:"nodeShell"`
	// PortForwards are the saved forwards (restorable with one click: stream.open type "portforward").
	PortForwards []SavedForward `msgpack:"portForwards" json:"portForwards"`
	// ProtectedClusters are cluster ids whose destructive actions ask for a typed confirmation in the UI.
	ProtectedClusters []string `msgpack:"protectedClusters" json:"protectedClusters"`
}

// NodeShellSettings: Image must be pinned (a digest); empty fields use the defaults.
type NodeShellSettings struct {
	Image         string `msgpack:"image,omitempty" json:"image,omitempty"`
	Namespace     string `msgpack:"namespace,omitempty" json:"namespace,omitempty"`         // default "v8s-system"
	PriorityClass string `msgpack:"priorityClass,omitempty" json:"priorityClass,omitempty"` // default none
}

// SavedForward is a remembered port-forward.
type SavedForward struct {
	Name string    `msgpack:"name,omitempty" json:"name,omitempty"`
	Ref  ObjectRef `msgpack:"ref" json:"ref"` // v1/pods or v1/services
	Port int       `msgpack:"port" json:"port"`
}

// SettingsSetParams carries an RFC 7386 JSON merge patch against the Settings document.
type SettingsSetParams struct {
	Patch map[string]any `msgpack:"patch" json:"patch" tstype:"{ [key: string]: unknown }"`
}

type SearchParams struct {
	Text  string `msgpack:"text" json:"text"`
	Limit int    `msgpack:"limit" json:"limit"`
	// IPPerKind caps the hits per kind of an IP search (default 5, max 50).
	IPPerKind int `msgpack:"ipPerKind,omitempty" json:"ipPerKind,omitempty"`
}

// ---- update check ---------------------------------------------------------------------

// UpdateStatus answers update.status: the newest stable v8s release when it is newer than this agent. The agent
// checks GitHub itself; the request only waits on the network for this process's first check (≤10 s). Empty when
// this build is current, the check is off, this is a development/branch build, or no check has succeeded yet.
type UpdateStatus struct {
	Latest string `msgpack:"latest,omitempty" json:"latest,omitempty"` // "0.5.0" (no "v")
	URL    string `msgpack:"url,omitempty" json:"url,omitempty"`       // Latest's GitHub release page
}

type SearchHit struct {
	Cluster   string `msgpack:"cluster" json:"cluster"`
	GVR       string `msgpack:"gvr" json:"gvr"`
	Kind      string `msgpack:"kind" json:"kind"`
	Namespace string `msgpack:"namespace,omitempty" json:"namespace,omitempty"`
	Name      string `msgpack:"name" json:"name"`
}

type SearchResult struct {
	Results []SearchHit `msgpack:"results" json:"results"`
	// IP is set when Text reads as an IP address, a partial address, a CIDR or an a-b range.
	IP *IPSearch `msgpack:"ip,omitempty" json:"ip,omitempty"`
}

type IPQueryMode string

const (
	IPQueryAddr   IPQueryMode = "addr"
	IPQueryCIDR   IPQueryMode = "cidr"
	IPQueryRange  IPQueryMode = "range"
	IPQueryPrefix IPQueryMode = "prefix"
)

type IPMatch string

const (
	IPMatchExact    IPMatch = "exact"
	IPMatchContains IPMatch = "contains"
	IPMatchWithin   IPMatch = "within"
	IPMatchOverlap  IPMatch = "overlap"
	IPMatchPrefix   IPMatch = "prefix"
)

// IPSearch is the IP half of a search result: the objects holding the queried address, grouped by kind.
type IPSearch struct {
	Query   string      `msgpack:"query" json:"query"` // normalized
	Mode    IPQueryMode `msgpack:"mode" json:"mode"`
	Groups  []IPGroup   `msgpack:"groups" json:"groups"`
	Pending []IPSource  `msgpack:"pending,omitempty" json:"pending,omitempty"` // still loading: ask again shortly
	Skipped []IPSource  `msgpack:"skipped,omitempty" json:"skipped,omitempty"` // not searched
}

type IPGroup struct {
	GVR   string  `msgpack:"gvr" json:"gvr"` // of the first hit; "Show all" opens it
	Kind  string  `msgpack:"kind" json:"kind"`
	Total int     `msgpack:"total" json:"total"` // matching objects across clusters
	Hits  []IPHit `msgpack:"hits" json:"hits"`
}

type IPHit struct {
	Cluster   string  `msgpack:"cluster" json:"cluster"`
	GVR       string  `msgpack:"gvr" json:"gvr"`
	Namespace string  `msgpack:"namespace,omitempty" json:"namespace,omitempty"`
	Name      string  `msgpack:"name" json:"name"`
	Field     string  `msgpack:"field" json:"field"` // "status.podIPs", "status.loadBalancer.ingress"
	Label     string  `msgpack:"label" json:"label"` // "Pod IP", "LB ingress IP"
	Value     string  `msgpack:"value" json:"value"` // "10.0.0.5" | "10.0.0.0/24" | "10.0.0.10-10.0.0.50"
	Match     IPMatch `msgpack:"match" json:"match"`
	Detail    string  `msgpack:"detail,omitempty" json:"detail,omitempty"`
}

// IPSource names a source an IP search could not read yet (Pending) or at all (Skipped).
type IPSource struct {
	Cluster string `msgpack:"cluster" json:"cluster"`
	Kind    string `msgpack:"kind,omitempty" json:"kind,omitempty"`     // "" = the cluster (discovery or sync)
	Reason  string `msgpack:"reason,omitempty" json:"reason,omitempty"` // skipped: forbidden | not found | budget
}

// Flow control (agent side): at most MaxUnacked patch frames or MaxInFlightBytes in flight per socket.
const (
	MaxUnacked       = 3
	MaxInFlightBytes = 512 * 1024
	MaxFrameBytes    = 128 * 1024
)

// ---- streams ---------------------------------------------------------------------------

// A stream (logs, exec, debug, node shell) has its own WebSocket, `GET /stream`, so a chatty log never
// delays view patches or keystrokes. It is authenticated like /ws (Sec-WebSocket-Protocol `v8s.v1` and
// `token.<hex>`, Origin allowlist) and is opened with a single-use id obtained from stream.open: the
// client additionally offers `stream.<id>` as a subprotocol, so ids never appear in URLs. An id is valid
// for StreamIDTTLSeconds and for one connection.
const (
	StreamSubprotocolPrefix = "stream."
	StreamIDTTLSeconds      = 30
	// DefaultLogTailLines is used when StreamOpenParams.TailLines is unset.
	DefaultLogTailLines = 1000
	// InitialLogCredit is the credit (bytes) a log stream starts with; the client adds more with {"credit": n}.
	InitialLogCredit = 256 * 1024
	// LogBatchBytes and LogBatchMillis bound one logs frame: at most 64 KiB, flushed after at most 100 ms.
	LogBatchBytes  = 64 * 1024
	LogBatchMillis = 100
)

// Binary frame channel bytes of an exec / node shell / debug stream. Client → agent: [StreamChanStdin][bytes].
// Agent → client: [StreamChanStdout][bytes] or [StreamChanStderr][bytes] (a TTY merges stderr into stdout).
const (
	StreamChanStdin  uint8 = 0x00
	StreamChanStdout uint8 = 0x01
	StreamChanStderr uint8 = 0x02
)

// StreamClientMsg is a client → agent text frame: {"credit": n} (logs: grants n more bytes) or
// {"resize": {"cols": c, "rows": r}} (exec, node shell, debug).
type StreamClientMsg struct {
	Credit int64         `msgpack:"credit,omitempty" json:"credit,omitempty"`
	Resize *StreamResize `msgpack:"resize,omitempty" json:"resize,omitempty"`
}

type StreamResize struct {
	Cols int `msgpack:"cols" json:"cols"`
	Rows int `msgpack:"rows" json:"rows"`
}

// StreamServerMsg is an agent → client text frame. Ready is the first frame of an interactive stream;
// Exit ends exec / node shell / debug ({"exit": code, "error"?}); End ends a logs stream ({"end": true, "error"?}).
type StreamServerMsg struct {
	Ready *StreamReady `msgpack:"ready,omitempty" json:"ready,omitempty"`
	Exit  *int         `msgpack:"exit,omitempty" json:"exit,omitempty"`
	End   bool         `msgpack:"end,omitempty" json:"end,omitempty"`
	Error string       `msgpack:"error,omitempty" json:"error,omitempty"`
}

type StreamReady struct {
	UsedContext string `msgpack:"usedContext" json:"usedContext"`
	Shell       string `msgpack:"shell,omitempty" json:"shell,omitempty"`         // exec: the shell that started
	Container   string `msgpack:"container,omitempty" json:"container,omitempty"` // exec/debug: the container entered
	Pod         string `msgpack:"pod,omitempty" json:"pod,omitempty"`             // nodeshell: the pod
	Attached    bool   `msgpack:"attached,omitempty" json:"attached,omitempty"`   // nodeshell: first session (attach) vs nsenter exec
}

// LogSelector selects the pods of a multi-pod log stream.
type LogSelector struct {
	Cluster       string `msgpack:"cluster" json:"cluster"`
	Namespace     string `msgpack:"namespace" json:"namespace"`
	LabelSelector string `msgpack:"labelSelector" json:"labelSelector"`
}

// StreamOpenParams is the stream.open request. Type selects which fields apply:
//
//   - "logs": Ref (a pod; with Container) or Selector, or Ref of a workload (its pods are selected from its
//     spec.selector); Follow, TailLines (default 1000), SinceSeconds, Previous, Timestamps; Download opens a
//     one-shot HTTP download (result.DownloadURL) instead of a socket. Multi-pod lines are prefixed
//     "<pod>/<container>\t"; the agent merges them by timestamp.
//   - "exec": Ref (pod), Container, Command (default: shell discovery /bin/bash → /bin/sh → /busybox/sh), TTY (default true).
//   - "debug": Ref (pod), Target (container), Image.
//   - "nodeshell": Node (and Cluster).
//   - "portforward": Ref (a pod, or a service resolved to a ready backing pod), Port. The result carries
//     LocalPort and ForwardID instead of a stream id.
type StreamOpenParams struct {
	Type         string       `msgpack:"type" json:"type"`
	Cluster      string       `msgpack:"cluster,omitempty" json:"cluster,omitempty"` // nodeshell
	Ref          *ObjectRef   `msgpack:"ref,omitempty" json:"ref,omitempty"`
	Selector     *LogSelector `msgpack:"selector,omitempty" json:"selector,omitempty"`
	Container    string       `msgpack:"container,omitempty" json:"container,omitempty"`
	Follow       bool         `msgpack:"follow,omitempty" json:"follow,omitempty"`
	TailLines    *int64       `msgpack:"tailLines,omitempty" json:"tailLines,omitempty"`
	SinceSeconds *int64       `msgpack:"sinceSeconds,omitempty" json:"sinceSeconds,omitempty"`
	Previous     bool         `msgpack:"previous,omitempty" json:"previous,omitempty"`
	Timestamps   bool         `msgpack:"timestamps,omitempty" json:"timestamps,omitempty"`
	Download     bool         `msgpack:"download,omitempty" json:"download,omitempty"`
	Command      []string     `msgpack:"command,omitempty" json:"command,omitempty"`
	TTY          *bool        `msgpack:"tty,omitempty" json:"tty,omitempty"`
	Target       string       `msgpack:"target,omitempty" json:"target,omitempty"`
	Image        string       `msgpack:"image,omitempty" json:"image,omitempty"`
	Node         string       `msgpack:"node,omitempty" json:"node,omitempty"`
	Port         int          `msgpack:"port,omitempty" json:"port,omitempty"`
	// Flows: type "flows" (Hubble), see hubble.go.
	Flows *FlowsStreamParams `msgpack:"flows,omitempty" json:"flows,omitempty"`
	// UseWriteContext as on ObjectApplyParams.
	UseWriteContext bool `msgpack:"useWriteContext,omitempty" json:"useWriteContext,omitempty"`
}

type StreamOpenResult struct {
	// StreamID is the single-use id to offer as subprotocol "stream.<id>" on GET /stream.
	StreamID string `msgpack:"streamId,omitempty" json:"streamId,omitempty"`
	// DownloadURL (logs with Download): GET it once with the agent token as `Authorization: Bearer <token>`
	// or `?token=`; the response is the whole log as one text/plain stream.
	DownloadURL string `msgpack:"downloadUrl,omitempty" json:"downloadUrl,omitempty"`
	// UsedContext is the context the stream runs through (the linked write context when the read context was denied).
	UsedContext string `msgpack:"usedContext,omitempty" json:"usedContext,omitempty"`
	// LocalPort and ForwardID answer type "portforward".
	LocalPort int    `msgpack:"localPort,omitempty" json:"localPort,omitempty"`
	ForwardID string `msgpack:"forwardId,omitempty" json:"forwardId,omitempty"`
}

type PortForwardInfo struct {
	ID          string    `msgpack:"id" json:"id"`
	Ref         ObjectRef `msgpack:"ref" json:"ref"`
	Port        int       `msgpack:"port" json:"port"`
	LocalPort   int       `msgpack:"localPort" json:"localPort"`
	Pod         string    `msgpack:"pod" json:"pod"`
	State       string    `msgpack:"state" json:"state"` // "running" | "reconnecting" | "stopped"
	UsedContext string    `msgpack:"usedContext" json:"usedContext"`
	Error       string    `msgpack:"error,omitempty" json:"error,omitempty"`
}

type PortForwardListResult struct {
	Forwards []PortForwardInfo `msgpack:"forwards" json:"forwards"`
}

type PortForwardStopParams struct {
	ID string `msgpack:"id" json:"id"`
}

// ---- jobs ------------------------------------------------------------------------------

type JobState string

const (
	JobRunning   JobState = "running"
	JobDone      JobState = "done"
	JobFailed    JobState = "failed"
	JobCancelled JobState = "cancelled"
)

// Job types for jobs.start.
const (
	JobDrain         = "drain"          // params: DrainParams
	JobHelmRollback  = "helm.rollback"  // params: HelmRollbackParams
	JobHelmUninstall = "helm.uninstall" // params: HelmUninstallParams
	JobHelmRetry     = "helm.retry"     // params: HelmRetryParams
)

type JobProgress struct {
	At      int64  `msgpack:"at" json:"at"`                           // unix ms
	Message string `msgpack:"message" json:"message"`                 // human text
	Item    string `msgpack:"item,omitempty" json:"item,omitempty"`   // what it is about, e.g. "team-1/svc-3-abc"
	State   string `msgpack:"state,omitempty" json:"state,omitempty"` // item state: "evicting" | "waiting-pdb" | "evicted" | "error" | ...
}

// JobFrame is the KindJob payload and the jobs.list entry: the job's whole state. Progress holds the last
// 200 entries (ProgressDropped counts older ones).
type JobFrame struct {
	JobID           string        `msgpack:"jobId" json:"jobId"`
	Type            string        `msgpack:"type" json:"type"`
	State           JobState      `msgpack:"state" json:"state"`
	Cluster         string        `msgpack:"cluster,omitempty" json:"cluster,omitempty"`
	Subject         string        `msgpack:"subject,omitempty" json:"subject,omitempty"` // node or release the job acts on
	StartedAt       int64         `msgpack:"startedAt" json:"startedAt"`
	EndedAt         int64         `msgpack:"endedAt,omitempty" json:"endedAt,omitempty"`
	Progress        []JobProgress `msgpack:"progress" json:"progress"`
	ProgressDropped int           `msgpack:"progressDropped,omitempty" json:"progressDropped,omitempty"`
	Error           *ErrorBody    `msgpack:"error,omitempty" json:"error,omitempty"`
	UsedContext     string        `msgpack:"usedContext,omitempty" json:"usedContext,omitempty"`
}

type JobsStartParams struct {
	RequestID       string         `msgpack:"requestId" json:"requestId"`
	Type            string         `msgpack:"type" json:"type"`
	Params          map[string]any `msgpack:"params" json:"params" tstype:"{ [key: string]: unknown }"`
	UseWriteContext bool           `msgpack:"useWriteContext,omitempty" json:"useWriteContext,omitempty"`
}

type JobsStartResult struct {
	JobID string `msgpack:"jobId" json:"jobId"`
}

type JobsListResult struct {
	Jobs []JobFrame `msgpack:"jobs" json:"jobs"`
}

type JobsCancelParams struct {
	JobID string `msgpack:"jobId" json:"jobId"`
}

// ---- drain -----------------------------------------------------------------------------

// DrainOptions mirror kubectl drain. IgnoreDaemonSets defaults to true. TimeoutSeconds 0 means 300.
// GracePeriodSeconds nil uses each pod's own. DisableEviction deletes pods instead of evicting them
// (PDBs are then not honoured): it is the explicit second confirmation.
type DrainOptions struct {
	IgnoreDaemonSets   *bool  `msgpack:"ignoreDaemonSets,omitempty" json:"ignoreDaemonSets,omitempty"`
	DeleteEmptyDirData bool   `msgpack:"deleteEmptyDirData,omitempty" json:"deleteEmptyDirData,omitempty"`
	Force              bool   `msgpack:"force,omitempty" json:"force,omitempty"` // also drain pods no controller manages
	GracePeriodSeconds *int   `msgpack:"gracePeriodSeconds,omitempty" json:"gracePeriodSeconds,omitempty"`
	TimeoutSeconds     int    `msgpack:"timeoutSeconds,omitempty" json:"timeoutSeconds,omitempty"`
	PodSelector        string `msgpack:"podSelector,omitempty" json:"podSelector,omitempty"`
	DisableEviction    bool   `msgpack:"disableEviction,omitempty" json:"disableEviction,omitempty"`
}

// DrainParams are the params of jobs.start type "drain".
type DrainParams struct {
	Cluster string       `msgpack:"cluster" json:"cluster"`
	Node    string       `msgpack:"node" json:"node"`
	Options DrainOptions `msgpack:"options" json:"options"`
}

type DrainPreflightParams = DrainParams

type DrainPDB struct {
	Name               string `msgpack:"name" json:"name"`
	DisruptionsAllowed int    `msgpack:"disruptionsAllowed" json:"disruptionsAllowed"`
}

type DrainPod struct {
	Namespace string    `msgpack:"namespace" json:"namespace"`
	Name      string    `msgpack:"name" json:"name"`
	Owner     string    `msgpack:"owner,omitempty" json:"owner,omitempty"` // "Kind/name" of the controller
	PDB       *DrainPDB `msgpack:"pdb,omitempty" json:"pdb,omitempty"`
	EmptyDir  bool      `msgpack:"emptyDir,omitempty" json:"emptyDir,omitempty"`
}

// DrainPreflightResult classifies the pods on the node. Evict pods will be evicted; Blocked pods are covered
// by a PDB with disruptionsAllowed 0 (the drain waits on them); Unmanaged pods have no controller and are
// lost for good (need force); DaemonSets are skipped; Mirror pods are skipped; Finished pods are deleted.
// EmptyDir lists, in addition, the pods whose emptyDir data is lost (needs deleteEmptyDirData).
type DrainPreflightResult struct {
	Node        string     `msgpack:"node" json:"node"`
	Cordoned    bool       `msgpack:"cordoned" json:"cordoned"`
	UsedContext string     `msgpack:"usedContext" json:"usedContext"`
	Evict       []DrainPod `msgpack:"evict" json:"evict"`
	Blocked     []DrainPod `msgpack:"blocked" json:"blocked"`
	Unmanaged   []DrainPod `msgpack:"unmanaged" json:"unmanaged"`
	EmptyDir    []DrainPod `msgpack:"emptyDir" json:"emptyDir"`
	DaemonSets  []DrainPod `msgpack:"daemonSets" json:"daemonSets"`
	Mirror      []DrainPod `msgpack:"mirror" json:"mirror"`
	Finished    []DrainPod `msgpack:"finished" json:"finished"`
}

// ---- flux ------------------------------------------------------------------------------

// FluxPayloadKey is where object payloads expose status.lastHandledReconcileAt: ObjectPayload.YAML contains
// it under status, so the UI shows "Reconciling…" until it equals the token a flux action returned.

type FluxInventoryParams struct {
	Ref ObjectRef `msgpack:"ref" json:"ref"` // a Kustomization
}

// FluxInventoryEntry is one status.inventory.entries item joined to the live object when its kind is watched.
type FluxInventoryEntry struct {
	ID        string `msgpack:"id" json:"id"`
	Group     string `msgpack:"group" json:"group"`
	Version   string `msgpack:"version" json:"version"`
	Kind      string `msgpack:"kind" json:"kind"`
	Namespace string `msgpack:"namespace,omitempty" json:"namespace,omitempty"`
	Name      string `msgpack:"name" json:"name"`
	GVR       string `msgpack:"gvr,omitempty" json:"gvr,omitempty"`
	// Live is true when the object exists in a watched kind; Tone (ColTone values) and Status are then its health.
	Live   bool   `msgpack:"live" json:"live"`
	Tone   int    `msgpack:"tone" json:"tone"`
	Status string `msgpack:"status,omitempty" json:"status,omitempty"`
}

type FluxInventoryResult struct {
	Entries  []FluxInventoryEntry `msgpack:"entries" json:"entries"`
	Revision string               `msgpack:"revision,omitempty" json:"revision,omitempty"`
}

type FluxGraphParams struct {
	Cluster string `msgpack:"cluster" json:"cluster"`
}

type FluxGraphNode struct {
	ID        string `msgpack:"id" json:"id"` // "<Kind>/<namespace>/<name>"
	Kind      string `msgpack:"kind" json:"kind"`
	GVR       string `msgpack:"gvr" json:"gvr"`
	Namespace string `msgpack:"namespace" json:"namespace"`
	Name      string `msgpack:"name" json:"name"`
	Tone      int    `msgpack:"tone" json:"tone"`
	Suspended bool   `msgpack:"suspended" json:"suspended"`
}

type FluxGraphEdge struct {
	From string `msgpack:"from" json:"from"` // node id
	To   string `msgpack:"to" json:"to"`     // node id
	Type string `msgpack:"type" json:"type"` // "dependsOn" (From waits for To) | "sourceRef" (From reads To)
}

type FluxGraphResult struct {
	Nodes []FluxGraphNode `msgpack:"nodes" json:"nodes"`
	Edges []FluxGraphEdge `msgpack:"edges" json:"edges"`
}

// ---- helm ------------------------------------------------------------------------------

// HelmGVR is the virtual resource of the Helm release view (one row per release: its latest revision).
const HelmGVR = "helm.sh/v1/releases"

type HelmReleaseParams struct {
	Cluster   string `msgpack:"cluster" json:"cluster"`
	Namespace string `msgpack:"namespace" json:"namespace"`
	Name      string `msgpack:"name" json:"name"`
	Revision  int    `msgpack:"revision,omitempty" json:"revision,omitempty"` // 0 = latest
}

type HelmRevision struct {
	Revision    int    `msgpack:"revision" json:"revision"`
	Status      string `msgpack:"status" json:"status"`
	Updated     int64  `msgpack:"updated" json:"updated"` // unix ms
	Chart       string `msgpack:"chart,omitempty" json:"chart,omitempty"`
	AppVersion  string `msgpack:"appVersion,omitempty" json:"appVersion,omitempty"`
	Description string `msgpack:"description,omitempty" json:"description,omitempty"`
}

type HelmHook struct {
	Name   string   `msgpack:"name" json:"name"`
	Kind   string   `msgpack:"kind" json:"kind"`
	Path   string   `msgpack:"path,omitempty" json:"path,omitempty"`
	Events []string `msgpack:"events" json:"events"`
	Weight int      `msgpack:"weight" json:"weight"`
}

// HelmRelease is one decoded release revision.
type HelmRelease struct {
	Name           string         `msgpack:"name" json:"name"`
	Namespace      string         `msgpack:"namespace" json:"namespace"`
	Revision       int            `msgpack:"revision" json:"revision"`
	Status         string         `msgpack:"status" json:"status"`
	Chart          string         `msgpack:"chart" json:"chart"`
	ChartVersion   string         `msgpack:"chartVersion" json:"chartVersion"`
	AppVersion     string         `msgpack:"appVersion,omitempty" json:"appVersion,omitempty"`
	Description    string         `msgpack:"description,omitempty" json:"description,omitempty"`
	Updated        int64          `msgpack:"updated" json:"updated"`
	Values         string         `msgpack:"values" json:"values"`                 // user-supplied values, YAML
	ComputedValues string         `msgpack:"computedValues" json:"computedValues"` // chart defaults merged with Values, YAML
	Manifest       string         `msgpack:"manifest" json:"manifest"`
	Notes          string         `msgpack:"notes,omitempty" json:"notes,omitempty"`
	Hooks          []HelmHook     `msgpack:"hooks" json:"hooks"`
	History        []HelmRevision `msgpack:"history" json:"history"`
	ManagedBy      *GitOps        `msgpack:"managedBy,omitempty" json:"managedBy,omitempty"` // Flux HelmRelease from the release labels
}

type HelmDiffParams struct {
	Ref          ObjectRef `msgpack:"ref" json:"ref"` // cluster, namespace, name (gvr is HelmGVR)
	FromRevision int       `msgpack:"fromRevision" json:"fromRevision"`
	ToRevision   int       `msgpack:"toRevision" json:"toRevision"`
}

// HelmDiffResult holds unified diffs ("" when identical).
type HelmDiffResult struct {
	Values   string `msgpack:"values" json:"values"`
	Manifest string `msgpack:"manifest" json:"manifest"`
}

// Helm action params (jobs.start). Force overrides the Flux guard ("gitops_managed"); ForceReplace is Helm's
// own --force. TimeoutSeconds 0 means 300.
type HelmRollbackParams struct {
	Cluster        string `msgpack:"cluster" json:"cluster"`
	Namespace      string `msgpack:"namespace" json:"namespace"`
	Name           string `msgpack:"name" json:"name"`
	Revision       int    `msgpack:"revision" json:"revision"`
	Wait           bool   `msgpack:"wait,omitempty" json:"wait,omitempty"`
	TimeoutSeconds int    `msgpack:"timeout,omitempty" json:"timeout,omitempty"`
	CleanupOnFail  bool   `msgpack:"cleanupOnFail,omitempty" json:"cleanupOnFail,omitempty"`
	ForceReplace   bool   `msgpack:"forceReplace,omitempty" json:"forceReplace,omitempty"`
	Force          bool   `msgpack:"force,omitempty" json:"force,omitempty"`
}

type HelmUninstallParams struct {
	Cluster        string `msgpack:"cluster" json:"cluster"`
	Namespace      string `msgpack:"namespace" json:"namespace"`
	Name           string `msgpack:"name" json:"name"`
	KeepHistory    bool   `msgpack:"keepHistory,omitempty" json:"keepHistory,omitempty"`
	Wait           bool   `msgpack:"wait,omitempty" json:"wait,omitempty"`
	TimeoutSeconds int    `msgpack:"timeout,omitempty" json:"timeout,omitempty"`
	Cascade        string `msgpack:"cascade,omitempty" json:"cascade,omitempty"` // "background" (default) | "foreground" | "orphan"
	Force          bool   `msgpack:"force,omitempty" json:"force,omitempty"`
}

// HelmRetryParams re-runs the last upgrade with the stored chart and values. A release stuck in pending-*
// needs MarkFailed first (it sets that revision to failed so Helm accepts a new operation).
type HelmRetryParams struct {
	Cluster        string `msgpack:"cluster" json:"cluster"`
	Namespace      string `msgpack:"namespace" json:"namespace"`
	Name           string `msgpack:"name" json:"name"`
	MarkFailed     bool   `msgpack:"markFailed,omitempty" json:"markFailed,omitempty"`
	Wait           bool   `msgpack:"wait,omitempty" json:"wait,omitempty"`
	TimeoutSeconds int    `msgpack:"timeout,omitempty" json:"timeout,omitempty"`
	Force          bool   `msgpack:"force,omitempty" json:"force,omitempty"`
}
