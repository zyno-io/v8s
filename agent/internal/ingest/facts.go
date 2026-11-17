package ingest

// Facts are the small, kind-specific records the problems engine reads next to a row's display
// cells (docs/problems.md: detectors read the agent's projected store). They are computed at
// projection time, before the cache, and are nil for objects that are healthy and uninteresting, so
// a 50k-pod cluster pays for them only on the few objects that matter.

import (
	"reflect"
	"strings"

	appsv1 "k8s.io/api/apps/v1"
	autoscalingv2 "k8s.io/api/autoscaling/v2"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// Facts returns the kind-specific facts of the row (nil when none).
func (r *Row) Facts() any { return r.facts }

// SetFacts attaches facts to the row. Facts take part in the row's equality, so a change of facts alone
// still reaches the change feed.
func (r *Row) SetFacts(f any) { r.facts = f }

func factsEqual(a, b any) bool {
	if a == nil && b == nil {
		return true
	}
	if a == nil || b == nil {
		return false
	}
	return reflect.DeepEqual(a, b)
}

// CondFact is a status condition reduced to what detectors use.
type CondFact struct {
	Status  string // "True" | "False" | "Unknown" ("" when the condition is absent)
	Reason  string
	Message string
	Since   int64 // lastTransitionTime, unix ms (0 unknown)
}

// Present reports whether the condition exists.
func (c CondFact) Present() bool { return c.Status != "" }

const maxFactMsg = 200

func clipMsg(s string) string {
	if len(s) <= maxFactMsg {
		return s
	}
	return strings.ToValidUTF8(s[:maxFactMsg], "") + "..."
}

func ms(t metav1.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.UnixMilli()
}

// CtrFact is one container of a pod (init containers included).
type CtrFact struct {
	Name           string
	Init           bool
	Ready          bool
	Restarts       int32
	Waiting        string // waiting reason
	WaitMsg        string
	TermReason     string // current terminated reason
	TermExit       int32
	TermAt         int64 // finishedAt of the current terminated state
	Started        int64 // startedAt of the running state
	LastTermReason string
	LastTermExit   int32
	LastTermAt     int64
	MemLimit       bool // the container has a memory limit (exit 137 then likely is an OOM kill)
}

// PodFacts is what the pod detectors read. Nil for a pod that is Running, Ready, never restarted and not deleting.
type PodFacts struct {
	Phase      string
	Reason     string // status.reason (Evicted, NodeLost, ...)
	Message    string
	DeletingAt int64 // deletionTimestamp, unix ms
	Scheduled  CondFact
	Ready      CondFact
	Containers []CtrFact
}

func podCond(p *corev1.Pod, t corev1.PodConditionType) CondFact {
	for i := range p.Status.Conditions {
		if c := &p.Status.Conditions[i]; c.Type == t {
			return CondFact{Status: string(c.Status), Reason: c.Reason, Message: clipMsg(c.Message), Since: ms(c.LastTransitionTime)}
		}
	}
	return CondFact{}
}

func ctrFact(st *corev1.ContainerStatus, init bool, memLimit map[string]bool) CtrFact {
	f := CtrFact{Name: st.Name, Init: init, Ready: st.Ready, Restarts: st.RestartCount, MemLimit: memLimit[st.Name]}
	if w := st.State.Waiting; w != nil {
		f.Waiting, f.WaitMsg = w.Reason, clipMsg(w.Message)
	}
	if t := st.State.Terminated; t != nil {
		f.TermReason, f.TermExit, f.TermAt = t.Reason, t.ExitCode, ms(t.FinishedAt)
	}
	if r := st.State.Running; r != nil {
		f.Started = ms(r.StartedAt)
	}
	if t := st.LastTerminationState.Terminated; t != nil {
		f.LastTermReason, f.LastTermExit, f.LastTermAt = t.Reason, t.ExitCode, ms(t.FinishedAt)
	}
	return f
}

// podFacts computes the facts of a pod; nil when it is boring.
func podFacts(p *corev1.Pod) *PodFacts {
	boring := p.Status.Phase == corev1.PodRunning && p.DeletionTimestamp == nil
	if boring {
		for i := range p.Status.Conditions {
			if c := &p.Status.Conditions[i]; c.Type == corev1.PodReady && c.Status != corev1.ConditionTrue {
				boring = false
			}
		}
	}
	if boring {
		for i := range p.Status.ContainerStatuses {
			if st := &p.Status.ContainerStatuses[i]; !st.Ready || st.RestartCount > 0 {
				boring = false
			}
		}
		for i := range p.Status.InitContainerStatuses {
			if st := &p.Status.InitContainerStatuses[i]; st.RestartCount > 0 || (st.State.Terminated != nil && st.State.Terminated.ExitCode != 0) {
				boring = false
			}
		}
	}
	if boring {
		return nil
	}
	memLimit := map[string]bool{}
	for _, cs := range [][]corev1.Container{p.Spec.InitContainers, p.Spec.Containers} {
		for i := range cs {
			if _, ok := cs[i].Resources.Limits[corev1.ResourceMemory]; ok {
				memLimit[cs[i].Name] = true
			}
		}
	}
	f := &PodFacts{Phase: string(p.Status.Phase), Reason: p.Status.Reason, Message: clipMsg(p.Status.Message),
		Scheduled: podCond(p, corev1.PodScheduled), Ready: podCond(p, corev1.PodReady)}
	if p.DeletionTimestamp != nil {
		f.DeletingAt = ms(*p.DeletionTimestamp)
	}
	for i := range p.Status.InitContainerStatuses {
		st := &p.Status.InitContainerStatuses[i]
		if st.State.Terminated != nil && st.State.Terminated.ExitCode == 0 && st.RestartCount == 0 {
			continue
		}
		f.Containers = append(f.Containers, ctrFact(st, true, memLimit))
	}
	for i := range p.Status.ContainerStatuses {
		f.Containers = append(f.Containers, ctrFact(&p.Status.ContainerStatuses[i], false, memLimit))
	}
	return f
}

// NodePressure is one pressure condition that is True.
type NodePressure struct {
	Type    string
	Reason  string
	Message string
	Since   int64
}

// NodeFacts is nil for a Ready, schedulable node without pressure.
type NodeFacts struct {
	Ready    CondFact
	Pressure []NodePressure
	Cordoned bool
}

func nodeFacts(n *corev1.Node) *NodeFacts {
	f := &NodeFacts{Cordoned: n.Spec.Unschedulable}
	for i := range n.Status.Conditions {
		c := &n.Status.Conditions[i]
		switch c.Type {
		case corev1.NodeReady:
			f.Ready = CondFact{Status: string(c.Status), Reason: c.Reason, Message: clipMsg(c.Message), Since: ms(c.LastTransitionTime)}
		case corev1.NodeMemoryPressure, corev1.NodeDiskPressure, corev1.NodePIDPressure:
			if c.Status == corev1.ConditionTrue {
				f.Pressure = append(f.Pressure, NodePressure{Type: string(c.Type), Reason: c.Reason, Message: clipMsg(c.Message), Since: ms(c.LastTransitionTime)})
			}
		}
	}
	if f.Ready.Status == "True" && len(f.Pressure) == 0 && !f.Cordoned {
		return nil
	}
	return f
}

// WorkloadFacts covers Deployments, StatefulSets and DaemonSets. Nil when every desired replica is
// available and the rollout is not failing.
type WorkloadFacts struct {
	Desired, Ready, Available, Updated int32
	ProgressDeadline                   int32 // seconds (0: not set)
	Progressing                        CondFact
	AvailableCond                      CondFact
	Paused                             bool
}

func depCond(conds []appsv1.DeploymentCondition, t appsv1.DeploymentConditionType) CondFact {
	for i := range conds {
		if c := &conds[i]; c.Type == t {
			return CondFact{Status: string(c.Status), Reason: c.Reason, Message: clipMsg(c.Message), Since: ms(c.LastTransitionTime)}
		}
	}
	return CondFact{}
}

func deploymentFacts(d *appsv1.Deployment) *WorkloadFacts {
	want := i32(d.Spec.Replicas, 1)
	f := &WorkloadFacts{Desired: want, Ready: d.Status.ReadyReplicas, Available: d.Status.AvailableReplicas, Updated: d.Status.UpdatedReplicas,
		ProgressDeadline: i32(d.Spec.ProgressDeadlineSeconds, 600), Paused: d.Spec.Paused,
		Progressing:   depCond(d.Status.Conditions, appsv1.DeploymentProgressing),
		AvailableCond: depCond(d.Status.Conditions, appsv1.DeploymentAvailable)}
	if f.Available >= want && f.Progressing.Status != "False" {
		return nil
	}
	return f
}

func statefulSetFacts(s *appsv1.StatefulSet) *WorkloadFacts {
	want := i32(s.Spec.Replicas, 1)
	if s.Status.ReadyReplicas >= want {
		return nil
	}
	return &WorkloadFacts{Desired: want, Ready: s.Status.ReadyReplicas, Available: s.Status.AvailableReplicas, Updated: s.Status.UpdatedReplicas}
}

func daemonSetFacts(d *appsv1.DaemonSet) *WorkloadFacts {
	s := &d.Status
	if s.NumberAvailable >= s.DesiredNumberScheduled {
		return nil
	}
	return &WorkloadFacts{Desired: s.DesiredNumberScheduled, Ready: s.NumberReady, Available: s.NumberAvailable, Updated: s.UpdatedNumberScheduled}
}

// JobFacts is set for failed Jobs and for every Job owned by a CronJob.
type JobFacts struct {
	Failed      CondFact
	Complete    CondFact
	CompletedAt int64
	Active      int32
	Failures    int32
}

func jobFacts(j *batchv1.Job) *JobFacts {
	f := &JobFacts{Active: j.Status.Active, Failures: j.Status.Failed}
	for i := range j.Status.Conditions {
		c := &j.Status.Conditions[i]
		cf := CondFact{Status: string(c.Status), Reason: c.Reason, Message: clipMsg(c.Message), Since: ms(c.LastTransitionTime)}
		switch c.Type {
		case batchv1.JobFailed:
			f.Failed = cf
		case batchv1.JobComplete:
			f.Complete = cf
		}
	}
	if j.Status.CompletionTime != nil {
		f.CompletedAt = ms(*j.Status.CompletionTime)
	}
	cron := false
	for _, o := range j.OwnerReferences {
		cron = cron || o.Kind == "CronJob"
	}
	if f.Failed.Status != "True" && !cron {
		return nil
	}
	return f
}

// CronFacts always accompanies a CronJob row.
type CronFacts struct {
	Schedule     string
	TimeZone     string
	Suspend      bool
	LastSchedule int64
	LastSuccess  int64
	Active       int32
}

func cronFacts(c *batchv1.CronJob) *CronFacts {
	f := &CronFacts{Schedule: c.Spec.Schedule, Suspend: c.Spec.Suspend != nil && *c.Spec.Suspend, Active: int32(len(c.Status.Active))}
	if c.Spec.TimeZone != nil {
		f.TimeZone = *c.Spec.TimeZone
	}
	if c.Status.LastScheduleTime != nil {
		f.LastSchedule = ms(*c.Status.LastScheduleTime)
	}
	if c.Status.LastSuccessfulTime != nil {
		f.LastSuccess = ms(*c.Status.LastSuccessfulTime)
	}
	return f
}

// HPAFacts is nil unless a condition or the replica count is interesting.
type HPAFacts struct {
	Current, Max  int32
	ScalingActive CondFact
	Limited       CondFact
}

func hpaFacts(h *autoscalingv2.HorizontalPodAutoscaler) *HPAFacts {
	f := &HPAFacts{Current: h.Status.CurrentReplicas, Max: h.Spec.MaxReplicas}
	for i := range h.Status.Conditions {
		c := &h.Status.Conditions[i]
		cf := CondFact{Status: string(c.Status), Reason: c.Reason, Message: clipMsg(c.Message), Since: ms(c.LastTransitionTime)}
		switch c.Type {
		case autoscalingv2.ScalingActive:
			f.ScalingActive = cf
		case autoscalingv2.ScalingLimited:
			f.Limited = cf
		}
	}
	if f.ScalingActive.Status != "False" && !(f.Max > 0 && f.Current >= f.Max) {
		return nil
	}
	return f
}

// IngressFacts lists the Services an Ingress routes to.
type IngressFacts struct {
	Services []string
}

func ingressFacts(in *networkingv1.Ingress) *IngressFacts {
	var svcs []string
	add := func(b *networkingv1.IngressBackend) {
		if b == nil || b.Service == nil || b.Service.Name == "" {
			return
		}
		for _, s := range svcs {
			if s == b.Service.Name {
				return
			}
		}
		svcs = append(svcs, b.Service.Name)
	}
	add(in.Spec.DefaultBackend)
	for i := range in.Spec.Rules {
		if h := in.Spec.Rules[i].HTTP; h != nil {
			for j := range h.Paths {
				add(&h.Paths[j].Backend)
			}
		}
	}
	if len(svcs) == 0 {
		return nil
	}
	return &IngressFacts{Services: svcs}
}
