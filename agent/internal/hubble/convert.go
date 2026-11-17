// Package hubble is the Hubble (Cilium) module: it reaches a cluster's hubble-relay through a port-forward
// carrying the cluster's credentials, streams flows with server-side filters, aggregates them in the agent
// (service map, drops) and feeds the network problem classes (docs/integrations.md "Hubble").
package hubble

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/zyno-io/v8s/agent/internal/hubble/pb"
	"github.com/zyno-io/v8s/agent/pkg/protocol"
)

// Cilium drop reasons (flow.DropReason) the agent names; others print as DROP_REASON_<n>.
var dropNames = map[int32]string{
	130: "INVALID_SOURCE_MAC", 131: "INVALID_DESTINATION_MAC", 132: "INVALID_SOURCE_IP", 133: "POLICY_DENIED",
	134: "INVALID_PACKET_DROPPED", 135: "CT_TRUNCATED_OR_INVALID_HEADER", 136: "CT_MISSING_TCP_ACK_FLAG",
	137: "CT_UNKNOWN_L4_PROTOCOL", 141: "MISSED_TAIL_CALL", 143: "UNKNOWN_L4_PROTOCOL", 151: "UNKNOWN_CONNECTION_TRACKING_STATE",
	152: "LOCAL_HOST_IS_UNREACHABLE", 157: "FIB_LOOKUP_FAILED", 159: "INVALID_IDENTITY", 160: "UNKNOWN_SENDER",
	162: "IS_A_CLUSTERIP", 166: "SOCKET_LOOKUP_FAILED", 169: "NO_CONFIGURATION_AVAILABLE_TO_PERFORM_POLICY_DECISION",
	181: "POLICY_DENY", 184: "TTL_EXCEEDED", 186: "DROP_RATE_LIMITED", 189: "AUTH_REQUIRED",
}

const (
	reasonPolicyDenied = 133
	reasonPolicyDeny   = 181
)

// DropReasonName names a Cilium drop reason number.
func DropReasonName(n int32) string {
	if s, ok := dropNames[n]; ok {
		return s
	}
	return "DROP_REASON_" + strconv.Itoa(int(n))
}

// dropReasonNumber parses a drop reason name or number (0 when unknown).
func dropReasonNumber(s string) int32 {
	if n, err := strconv.Atoi(s); err == nil {
		return int32(n)
	}
	for k, v := range dropNames {
		if strings.EqualFold(v, s) {
			return k
		}
	}
	return 0
}

func isPolicyDrop(n int32) bool { return n == reasonPolicyDenied || n == reasonPolicyDeny }

// workloadIdentity returns the (namespace, workload) identity of an endpoint: the first workload of a pod, the
// pod name with its generated suffixes stripped when the workload is unknown, or a reserved identity
// ("world", "host", "kube-apiserver", ...) with no namespace.
func workloadIdentity(e *pb.Endpoint) (ns, wl string) {
	if e == nil {
		return "", "unknown"
	}
	if e.Namespace != "" {
		if len(e.Workloads) > 0 && e.Workloads[0].Name != "" {
			return e.Namespace, e.Workloads[0].Name
		}
		if e.PodName != "" {
			return e.Namespace, stripPodSuffix(e.PodName)
		}
		return e.Namespace, "unknown"
	}
	for _, l := range e.Labels {
		if r, ok := strings.CutPrefix(l, "reserved:"); ok {
			return "", r
		}
	}
	for _, l := range e.Labels {
		if r, ok := strings.CutPrefix(l, "cidr:"); ok {
			return "", "cidr:" + r
		}
	}
	return "", "world"
}

// stripPodSuffix removes the replicaset hash and pod suffix of a generated pod name ("api-7d9f8-x2k4p" -> "api").
func stripPodSuffix(pod string) string {
	parts := strings.Split(pod, "-")
	if len(parts) >= 3 {
		return strings.Join(parts[:len(parts)-2], "-")
	}
	if len(parts) == 2 {
		return parts[0]
	}
	return pod
}

func identityID(ns, wl string) string {
	if ns == "" {
		return wl
	}
	return ns + "/" + wl
}

func flowEndpoint(e *pb.Endpoint, ip string, port uint32) protocol.FlowEndpoint {
	fe := protocol.FlowEndpoint{IP: ip, Port: int(port)}
	if e != nil {
		fe.Namespace, fe.Pod = e.Namespace, e.PodName
	}
	_, fe.Workload = workloadIdentity(e)
	return fe
}

func protoName(f *pb.Flow) (name string, sport, dport uint32) {
	switch l := f.GetL4().GetProtocol().(type) {
	case *pb.Layer4_TCP:
		return "TCP", l.TCP.GetSourcePort(), l.TCP.GetDestinationPort()
	case *pb.Layer4_UDP:
		return "UDP", l.UDP.GetSourcePort(), l.UDP.GetDestinationPort()
	case *pb.Layer4_ICMPv4, *pb.Layer4_ICMPv6:
		return "ICMP", 0, 0
	}
	return "", 0, 0
}

func dnsRcodeName(c uint32) string {
	switch c {
	case 0:
		return "NOERROR"
	case 1:
		return "FORMERR"
	case 2:
		return "SERVFAIL"
	case 3:
		return "NXDOMAIN"
	case 5:
		return "REFUSED"
	}
	return "RCODE" + strconv.Itoa(int(c))
}

// l7Summary renders the L7 record of a flow ("GET /api 503", "DNS A example.com NXDOMAIN") and its protocol.
func l7Summary(f *pb.Flow) (proto, text string) {
	l7 := f.GetL7()
	if l7 == nil {
		return "", ""
	}
	switch r := l7.GetRecord().(type) {
	case *pb.Layer7_Http:
		h := r.Http
		if l7.Type == pb.L7FlowType_RESPONSE {
			return "HTTP", strings.TrimSpace(fmt.Sprintf("%s %s %d", h.Method, h.Url, h.Code))
		}
		return "HTTP", strings.TrimSpace(h.Method + " " + h.Url)
	case *pb.Layer7_Dns:
		d := r.Dns
		s := "DNS " + strings.Join(d.Qtypes, ",") + " " + strings.TrimSuffix(d.Query, ".")
		if l7.Type == pb.L7FlowType_RESPONSE {
			s += " " + dnsRcodeName(d.Rcode)
		}
		return "DNS", strings.Join(strings.Fields(s), " ")
	}
	return "", ""
}

func verdictName(v pb.Verdict) string { return strings.TrimPrefix(v.String(), "VERDICT_") }

func directionName(d pb.TrafficDirection) string {
	switch d {
	case pb.TrafficDirection_INGRESS:
		return "INGRESS"
	case pb.TrafficDirection_EGRESS:
		return "EGRESS"
	}
	return ""
}

// toFlow converts a relay flow to a UI row.
func toFlow(f *pb.Flow) protocol.Flow {
	p, sp, dp := protoName(f)
	out := protocol.Flow{Verdict: verdictName(f.Verdict), Direction: directionName(f.TrafficDirection), Reply: f.GetIsReply().GetValue(), Node: f.NodeName,
		Src: flowEndpoint(f.Source, f.GetIp().GetSource(), sp), Dst: flowEndpoint(f.Destination, f.GetIp().GetDestination(), dp), Protocol: p}
	if t := f.GetTime(); t != nil {
		out.Time = t.AsTime().UnixMilli()
	}
	if l7p, text := l7Summary(f); l7p != "" {
		out.Protocol, out.L7 = l7p, text
	}
	if f.Verdict == pb.Verdict_DROPPED {
		out.DropReason = DropReasonName(f.DropReasonDesc)
	}
	return out
}
