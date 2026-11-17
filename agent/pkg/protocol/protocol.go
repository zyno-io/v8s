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
)

// Hello is sent by the agent right after the WebSocket upgrade.
type Hello struct {
	Version  int    `msgpack:"version" json:"version"`
	AgentVer string `msgpack:"agentVersion" json:"agentVersion"`
	Mode     string `msgpack:"mode" json:"mode"` // "desktop" | "serve" | "fake"
}

// Request is a client call. Method names and their params/result types:
//
//	"clusters.list"      → ClustersListResult (no params)
//	"clusters.select"    ClustersSelectParams → ClusterStates
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
	// "invalid" (the API server rejected the object, e.g. validation or admission).
	Code    string         `msgpack:"code" json:"code"`
	Message string         `msgpack:"message" json:"message"`
	Live    *ObjectPayload `msgpack:"live,omitempty" json:"live,omitempty"`
}

// ---- clusters -------------------------------------------------------------------------

type Cluster struct {
	ID     string   `msgpack:"id" json:"id"`         // stable local id (desktop: the kubeconfig context name)
	Name   string   `msgpack:"name" json:"name"`     // display name
	Server string   `msgpack:"server" json:"server"` // API server URL, for display
	Groups []string `msgpack:"groups" json:"groups"`
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

type ClusterState struct {
	ID      string       `msgpack:"id" json:"id"`
	Phase   ClusterPhase `msgpack:"phase" json:"phase"`
	Synced  int          `msgpack:"synced" json:"synced"` // rows received so far while syncing
	Message string       `msgpack:"message,omitempty" json:"message,omitempty"`
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
	Available  bool     `msgpack:"available" json:"available"` // true for every listable kind
	Curated    bool     `msgpack:"curated" json:"curated"`     // hand-written columns (mode A) vs Table columns (mode C)
	Clusters   []string `msgpack:"clusters" json:"clusters"`   // selected clusters that serve it
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
	ColInt  ColumnType = "int"  // integer
	ColTime ColumnType = "time" // unix milliseconds (int); the UI renders relative ages
	ColTone ColumnType = "tone" // small int: 0 neutral, 1 success, 2 warning, 3 danger, 4 info, 5 release
)

type Column struct {
	Key   string     `msgpack:"key" json:"key"`
	Type  ColumnType `msgpack:"type" json:"type"`
	Label string     `msgpack:"label,omitempty" json:"label,omitempty"`
	Width int        `msgpack:"width,omitempty" json:"width,omitempty"` // px hint
	Align string     `msgpack:"align,omitempty" json:"align,omitempty"` // "left" | "right"
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
}

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
	{Key: "namespace", Type: ColDict, Label: "Namespace", Width: 160},
	{Key: "ready", Type: ColInt, Label: "Ready", Width: 64, Align: "right"},           // ready containers
	{Key: "containers", Type: ColInt, Label: "Containers", Width: 84, Align: "right"}, // total containers
	{Key: "status", Type: ColDict, Label: "Status", Width: 140},                       // kubectl-style status reason: Running, CrashLoopBackOff, Pending, …
	{Key: "statusTone", Type: ColTone, Label: "Tone", Width: 40},
	{Key: "restarts", Type: ColInt, Label: "Restarts", Width: 76, Align: "right"},
	{Key: "node", Type: ColDict, Label: "Node", Width: 160},
	{Key: "createdAt", Type: ColTime, Label: "Age", Width: 72, Align: "right"},
}

// ---- objects --------------------------------------------------------------------------

// ObjectRef addresses one object. GVR is "v1/pods" or "apps/v1/deployments".
type ObjectRef struct {
	Cluster   string `msgpack:"cluster" json:"cluster"`
	GVR       string `msgpack:"gvr" json:"gvr"`
	Namespace string `msgpack:"namespace,omitempty" json:"namespace,omitempty"`
	Name      string `msgpack:"name,omitempty" json:"name,omitempty"` // empty only for object.dryRun/apply in create mode
}

type ObjectGetParams struct {
	Cluster           string `msgpack:"cluster" json:"cluster"`
	GVR               string `msgpack:"gvr" json:"gvr"`
	Namespace         string `msgpack:"namespace,omitempty" json:"namespace,omitempty"`
	Name              string `msgpack:"name" json:"name"`
	KeepManagedFields bool   `msgpack:"keepManagedFields,omitempty" json:"keepManagedFields,omitempty"`
}

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
}

type ObjectApplyResult struct {
	ResourceVersion string      `msgpack:"resourceVersion" json:"resourceVersion"`
	UsedContext     string      `msgpack:"usedContext" json:"usedContext"`
	Applied         []ObjectRef `msgpack:"applied,omitempty" json:"applied,omitempty"` // create mode: every object applied
}

type ObjectDeleteParams struct {
	RequestID   string    `msgpack:"requestId" json:"requestId"`
	Ref         ObjectRef `msgpack:"ref" json:"ref"`
	Propagation string    `msgpack:"propagation,omitempty" json:"propagation,omitempty"` // "Background" (default) | "Foreground" | "Orphan"
}

// ObjectActionParams: Action is "scale" {replicas}, "restart", "cordon", "uncordon", "suspend",
// "resume", "trigger", "rollback" {revision}.
type ObjectActionParams struct {
	RequestID string         `msgpack:"requestId" json:"requestId"`
	Ref       ObjectRef      `msgpack:"ref" json:"ref"`
	Action    string         `msgpack:"action" json:"action"`
	Params    map[string]any `msgpack:"params,omitempty" json:"params,omitempty" tstype:"{ [key: string]: unknown }"`
}

// MutationResult answers object.delete and object.action.
type MutationResult struct {
	UsedContext     string `msgpack:"usedContext" json:"usedContext"`
	ResourceVersion string `msgpack:"resourceVersion,omitempty" json:"resourceVersion,omitempty"`
	Name            string `msgpack:"name,omitempty" json:"name,omitempty"` // action "trigger": the created Job
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
	Allowed  bool   `msgpack:"allowed" json:"allowed"`
	ViaWrite bool   `msgpack:"viaWrite" json:"viaWrite"` // allowed only through the linked write context
	Reason   string `msgpack:"reason" json:"reason"`
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
}

// SettingsSetParams carries an RFC 7386 JSON merge patch against the Settings document.
type SettingsSetParams struct {
	Patch map[string]any `msgpack:"patch" json:"patch" tstype:"{ [key: string]: unknown }"`
}

type SearchParams struct {
	Text  string `msgpack:"text" json:"text"`
	Limit int    `msgpack:"limit" json:"limit"`
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
}

// Flow control (agent side): at most MaxUnacked patch frames or MaxInFlightBytes in flight per socket.
const (
	MaxUnacked       = 3
	MaxInFlightBytes = 512 * 1024
	MaxFrameBytes    = 128 * 1024
)
