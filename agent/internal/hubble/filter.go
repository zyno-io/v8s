package hubble

import (
	"strings"

	"github.com/zyno-io/v8s/agent/internal/hubble/pb"
	"github.com/zyno-io/v8s/agent/internal/ops"
	"github.com/zyno-io/v8s/agent/pkg/protocol"
)

// validateScope refuses an unscoped follow: a flows stream needs a namespace, a workload or a pod (the latter two
// with their namespace), so the relay never has to produce the whole cluster's traffic.
func validateScope(s protocol.FlowScope) error {
	if s.Namespace == "" {
		if s.Workload != "" || s.Pod != "" {
			return ops.NewError("bad_request", "a workload or pod scope needs its namespace")
		}
		return ops.NewError("bad_request", "flows needs a scope: a namespace, workload or pod (an unscoped flow stream is refused)")
	}
	return nil
}

func parseVerdict(s string) (pb.Verdict, error) {
	v, ok := pb.Verdict_value[strings.ToUpper(strings.TrimPrefix(strings.ToUpper(s), "VERDICT_"))]
	if !ok || v == 0 {
		return 0, ops.NewError("bad_request", "unknown verdict %q", s)
	}
	return pb.Verdict(v), nil
}

// requestFilters builds the relay's whitelist for a scope and the user's filter. Whitelist entries are OR-ed
// by the relay and the fields of one entry AND-ed, so each side of the traffic (source, destination) gets an
// entry carrying the whole filter: the relay does all the filtering.
func requestFilters(scope protocol.FlowScope, f protocol.FlowFilter) ([]*pb.FlowFilter, error) {
	if err := validateScope(scope); err != nil {
		return nil, err
	}
	var src, dst pb.FlowFilter
	switch {
	case scope.Pod != "":
		src.SourcePod = []string{scope.Namespace + "/" + scope.Pod}
		dst.DestinationPod = []string{scope.Namespace + "/" + scope.Pod}
	default:
		src.SourcePod = []string{scope.Namespace + "/"}
		dst.DestinationPod = []string{scope.Namespace + "/"}
		if scope.Workload != "" {
			src.SourceWorkload = []*pb.Workload{{Name: scope.Workload}}
			dst.DestinationWorkload = []*pb.Workload{{Name: scope.Workload}}
		}
	}
	out := []*pb.FlowFilter{&src, &dst}
	for _, ff := range out {
		if err := applyFilter(ff, f); err != nil {
			return nil, err
		}
	}
	return out, nil
}

func applyFilter(ff *pb.FlowFilter, f protocol.FlowFilter) error {
	if f.Verdict != "" {
		v, err := parseVerdict(f.Verdict)
		if err != nil {
			return err
		}
		ff.Verdict = []pb.Verdict{v}
	}
	if f.DropReason != "" {
		n := dropReasonNumber(f.DropReason)
		if n == 0 {
			return ops.NewError("bad_request", "unknown drop reason %q", f.DropReason)
		}
		ff.DropReasonDesc = []int32{n}
	}
	if f.Protocol != "" {
		ff.Protocol = []string{strings.ToLower(f.Protocol)}
	}
	if f.HTTPStatus != "" {
		ff.HttpStatusCode = []string{f.HTTPStatus}
	}
	if f.DNSQuery != "" {
		ff.DnsQuery = []string{f.DNSQuery}
	}
	if f.Direction != "" {
		switch strings.ToUpper(f.Direction) {
		case "INGRESS":
			ff.TrafficDirection = []pb.TrafficDirection{pb.TrafficDirection_INGRESS}
		case "EGRESS":
			ff.TrafficDirection = []pb.TrafficDirection{pb.TrafficDirection_EGRESS}
		default:
			return ops.NewError("bad_request", "unknown direction %q", f.Direction)
		}
	}
	return nil
}

// droppedFilter is the narrow filter of the drops view and of the continuous policy-drops subscription.
func droppedFilter() []*pb.FlowFilter {
	return []*pb.FlowFilter{{Verdict: []pb.Verdict{pb.Verdict_DROPPED}}}
}
