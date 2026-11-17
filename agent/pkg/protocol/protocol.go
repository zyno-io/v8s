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
	Code    string `msgpack:"code" json:"code"` // "bad_request" | "not_found" | "forbidden" | "unavailable" | "internal"
	Message string `msgpack:"message" json:"message"`
}

// ---- clusters -------------------------------------------------------------------------

type Cluster struct {
	ID     string   `msgpack:"id" json:"id"`         // stable local id (desktop: the kubeconfig context name)
	Name   string   `msgpack:"name" json:"name"`     // display name
	Server string   `msgpack:"server" json:"server"` // API server URL, for display
	Groups []string `msgpack:"groups" json:"groups"`
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

// Kind describes a resource type the UI can open (P0: pods only; the list grows with discovery).
type Kind struct {
	GVR        string   `msgpack:"gvr" json:"gvr"` // "v1/pods", "apps/v1/deployments"
	Kind       string   `msgpack:"kind" json:"kind"`
	Plural     string   `msgpack:"plural" json:"plural"`
	ShortNames []string `msgpack:"shortNames" json:"shortNames"` // from discovery: "po", "deploy", …
	Namespaced bool     `msgpack:"namespaced" json:"namespaced"`
	Available  bool     `msgpack:"available" json:"available"` // false: known to the UI but no view yet
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
	Key  string     `msgpack:"key" json:"key"`
	Type ColumnType `msgpack:"type" json:"type"`
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

// ---- pods (P0's one curated kind) ------------------------------------------------------

// PodColumns is the column schema of the "v1/pods" view, in cell order.
var PodColumns = []Column{
	{Key: "cluster", Type: ColDict},
	{Key: "name", Type: ColStr},
	{Key: "namespace", Type: ColDict},
	{Key: "ready", Type: ColInt},      // ready containers
	{Key: "containers", Type: ColInt}, // total containers
	{Key: "status", Type: ColDict},    // kubectl-style status reason: Running, CrashLoopBackOff, Pending, …
	{Key: "statusTone", Type: ColTone},
	{Key: "restarts", Type: ColInt},
	{Key: "node", Type: ColDict},
	{Key: "createdAt", Type: ColTime},
}

// Flow control (agent side): at most MaxUnacked patch frames or MaxInFlightBytes in flight per socket.
const (
	MaxUnacked       = 3
	MaxInFlightBytes = 512 * 1024
	MaxFrameBytes    = 128 * 1024
)
