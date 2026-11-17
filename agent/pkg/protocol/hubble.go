package protocol

// ---- hubble (docs/integrations.md) -------------------------------------------------------

// FlowScope narrows a flows stream server-side. A namespace, or a workload (with its namespace), or a pod
// (with its namespace) is required: an unscoped follow is refused with bad_request.
type FlowScope struct {
	Namespace string `msgpack:"namespace,omitempty" json:"namespace,omitempty"`
	Workload  string `msgpack:"workload,omitempty" json:"workload,omitempty"`
	Pod       string `msgpack:"pod,omitempty" json:"pod,omitempty"`
}

// FlowFilter maps to Hubble FlowFilter fields, applied by the relay (AND-ed with the scope). Verdict is
// FORWARDED, DROPPED, ERROR, AUDIT, REDIRECTED, TRACED or TRANSLATED; DropReason a Cilium drop reason name
// (POLICY_DENIED) or number; Protocol tcp, udp, icmp, http or dns; HTTPStatus a code or prefix ("5+");
// Direction INGRESS or EGRESS.
type FlowFilter struct {
	Verdict    string `msgpack:"verdict,omitempty" json:"verdict,omitempty"`
	DropReason string `msgpack:"dropReason,omitempty" json:"dropReason,omitempty"`
	Protocol   string `msgpack:"protocol,omitempty" json:"protocol,omitempty"`
	HTTPStatus string `msgpack:"httpStatus,omitempty" json:"httpStatus,omitempty"`
	DNSQuery   string `msgpack:"dnsQuery,omitempty" json:"dnsQuery,omitempty"`
	Direction  string `msgpack:"direction,omitempty" json:"direction,omitempty"`
}

// FlowsStreamParams are the stream.open params of type "flows" (docs/protocol.md "Streams").
type FlowsStreamParams struct {
	Cluster string     `msgpack:"cluster" json:"cluster"`
	Scope   FlowScope  `msgpack:"scope" json:"scope"`
	Filter  FlowFilter `msgpack:"filter" json:"filter"`
}

// FlowEndpoint is one end of a flow, identity-aggregated: Workload is the owning workload (or a reserved
// identity such as world or host for non-pod endpoints).
type FlowEndpoint struct {
	Namespace string `msgpack:"namespace,omitempty" json:"namespace,omitempty"`
	Pod       string `msgpack:"pod,omitempty" json:"pod,omitempty"`
	Workload  string `msgpack:"workload,omitempty" json:"workload,omitempty"`
	IP        string `msgpack:"ip,omitempty" json:"ip,omitempty"`
	Port      int    `msgpack:"port,omitempty" json:"port,omitempty"`
}

// Flow is one row of the live flow table.
type Flow struct {
	Time       int64        `msgpack:"time" json:"time"` // unix ms
	Verdict    string       `msgpack:"verdict" json:"verdict"`
	Direction  string       `msgpack:"direction,omitempty" json:"direction,omitempty"`
	Reply      bool         `msgpack:"reply,omitempty" json:"reply,omitempty"`
	Src        FlowEndpoint `msgpack:"src" json:"src"`
	Dst        FlowEndpoint `msgpack:"dst" json:"dst"`
	Protocol   string       `msgpack:"protocol,omitempty" json:"protocol,omitempty"` // TCP | UDP | ICMP | HTTP | DNS
	L7         string       `msgpack:"l7,omitempty" json:"l7,omitempty"`             // "GET /api 503", "DNS A example.com"
	DropReason string       `msgpack:"dropReason,omitempty" json:"dropReason,omitempty"`
	Node       string       `msgpack:"node,omitempty" json:"node,omitempty"`
}

// FlowRate reports the sampling of a flows stream: Total is the observed rate (flows/s), Shown what reached the
// client. The toolbar says "showing {shown}/s of ~{total}/s, narrow the filter" when Sampled.
type FlowRate struct {
	Shown   int  `msgpack:"shown" json:"shown"`
	Total   int  `msgpack:"total" json:"total"`
	Sampled bool `msgpack:"sampled" json:"sampled"`
}

// FlowsFrame is an agent → client text frame (JSON) of a flows stream: Ready first, then batches every 100 ms
// (only when there is something new: flows or a changed rate), End last.
type FlowsFrame struct {
	Ready *StreamReady `msgpack:"ready,omitempty" json:"ready,omitempty"`
	Flows []Flow       `msgpack:"flows,omitempty" json:"flows,omitempty"`
	Rate  *FlowRate    `msgpack:"rate,omitempty" json:"rate,omitempty"`
	End   bool         `msgpack:"end,omitempty" json:"end,omitempty"`
	Error string       `msgpack:"error,omitempty" json:"error,omitempty"`
}

// FlowsClientMsg is a client → agent text frame of a flows stream: Pause when no view shows the flows (the
// stream is cancelled after 10 s paused), Resume to continue.
type FlowsClientMsg struct {
	Pause  bool `msgpack:"pause,omitempty" json:"pause,omitempty"`
	Resume bool `msgpack:"resume,omitempty" json:"resume,omitempty"`
}

// HubbleRelayInfo says where the cluster's hubble-relay was found.
type HubbleRelayInfo struct {
	Namespace string `msgpack:"namespace" json:"namespace"`
	Service   string `msgpack:"service" json:"service"`
	Pod       string `msgpack:"pod,omitempty" json:"pod,omitempty"`
	Port      int    `msgpack:"port,omitempty" json:"port,omitempty"`
}

type HubbleStatusParams struct {
	Cluster string `msgpack:"cluster" json:"cluster"`
}

type HubbleNode struct {
	Name    string `msgpack:"name" json:"name"`
	Version string `msgpack:"version,omitempty" json:"version,omitempty"`
	State   string `msgpack:"state" json:"state"` // "connected" | "unavailable" | "unknown"
}

// HubbleStatus answers hubble.status. Available is false when there is no relay or it cannot be reached
// (Error says why); the rest is then empty.
type HubbleStatus struct {
	Cluster          string           `msgpack:"cluster" json:"cluster"`
	Available        bool             `msgpack:"available" json:"available"`
	Error            string           `msgpack:"error,omitempty" json:"error,omitempty"`
	Relay            *HubbleRelayInfo `msgpack:"relay,omitempty" json:"relay,omitempty"`
	Version          string           `msgpack:"version,omitempty" json:"version,omitempty"`
	NumFlows         int              `msgpack:"numFlows" json:"numFlows"` // flows currently buffered, over all nodes
	MaxFlows         int              `msgpack:"maxFlows" json:"maxFlows"` // flow buffer capacity, over all nodes
	SeenFlows        int              `msgpack:"seenFlows" json:"seenFlows"`
	FlowsPerSec      float64          `msgpack:"flowsPerSec" json:"flowsPerSec"`
	UptimeSec        int              `msgpack:"uptimeSec" json:"uptimeSec"`
	ConnectedNodes   int              `msgpack:"connectedNodes" json:"connectedNodes"`
	UnavailableNodes int              `msgpack:"unavailableNodes" json:"unavailableNodes"`
	Nodes            []HubbleNode     `msgpack:"nodes" json:"nodes"`
}

type HubbleServiceMapParams struct {
	Cluster   string `msgpack:"cluster" json:"cluster"`
	Namespace string `msgpack:"namespace" json:"namespace"`
	WindowSec int    `msgpack:"windowSec,omitempty" json:"windowSec,omitempty"` // default 300, max 3600
}

// ServiceMapNode is a workload (identity-aggregated, not pods) or a reserved identity (world, host).
type ServiceMapNode struct {
	ID        string `msgpack:"id" json:"id"` // "<namespace>/<workload>", or the reserved identity
	Namespace string `msgpack:"namespace,omitempty" json:"namespace,omitempty"`
	Workload  string `msgpack:"workload" json:"workload"`
	External  bool   `msgpack:"external" json:"external"` // outside the requested namespace
}

// ServiceMapEdge aggregates the flows between two workloads over the window: Requests are non-reply flows
// that were not dropped (L7 requests when the pair has L7 visibility), Errors are HTTP 5xx or DNS
// SERVFAIL/NXDOMAIN responses and ERROR verdicts, Drops are DROPPED flows.
type ServiceMapEdge struct {
	From     string `msgpack:"from" json:"from"`
	To       string `msgpack:"to" json:"to"`
	Requests int    `msgpack:"requests" json:"requests"`
	Errors   int    `msgpack:"errors" json:"errors"`
	Drops    int    `msgpack:"drops" json:"drops"`
}

type HubbleServiceMap struct {
	Cluster   string           `msgpack:"cluster" json:"cluster"`
	Namespace string           `msgpack:"namespace" json:"namespace"`
	WindowSec int              `msgpack:"windowSec" json:"windowSec"`
	Nodes     []ServiceMapNode `msgpack:"nodes" json:"nodes"`
	Edges     []ServiceMapEdge `msgpack:"edges" json:"edges"`
	// Truncated is true when more flows matched than the agent read (the counters are lower bounds).
	Truncated bool `msgpack:"truncated,omitempty" json:"truncated,omitempty"`
}

type HubbleDropsParams struct {
	Clusters  []string `msgpack:"clusters" json:"clusters"` // empty: every selected cluster
	WindowSec int      `msgpack:"windowSec,omitempty" json:"windowSec,omitempty"`
}

// DropWorkload is one side of a drop group.
type DropWorkload struct {
	Namespace string `msgpack:"namespace,omitempty" json:"namespace,omitempty"`
	Workload  string `msgpack:"workload" json:"workload"`
}

// DropPolicy is a policy likely responsible for a drop.
type DropPolicy struct {
	Kind      string `msgpack:"kind" json:"kind"` // NetworkPolicy | CiliumNetworkPolicy | CiliumClusterwideNetworkPolicy
	Namespace string `msgpack:"namespace,omitempty" json:"namespace,omitempty"`
	Name      string `msgpack:"name" json:"name"`
}

// DropGroup is dropped traffic grouped by (source workload, destination workload, drop reason).
type DropGroup struct {
	Cluster  string       `msgpack:"cluster" json:"cluster"`
	Src      DropWorkload `msgpack:"src" json:"src"`
	Dst      DropWorkload `msgpack:"dst" json:"dst"`
	Reason   string       `msgpack:"reason" json:"reason"`
	Count    int          `msgpack:"count" json:"count"`
	LastSeen int64        `msgpack:"lastSeen" json:"lastSeen"` // unix ms
	Policies []DropPolicy `msgpack:"policies" json:"policies"`
}

type DropsError struct {
	Cluster string `msgpack:"cluster" json:"cluster"`
	Error   string `msgpack:"error" json:"error"`
}

type HubbleDrops struct {
	WindowSec int          `msgpack:"windowSec" json:"windowSec"`
	Groups    []DropGroup  `msgpack:"groups" json:"groups"`
	Errors    []DropsError `msgpack:"errors" json:"errors"` // clusters without a reachable relay
}
