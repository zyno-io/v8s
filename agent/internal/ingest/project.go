package ingest

import (
	"fmt"
	"strings"
	"unique"

	"github.com/zyno-io/v8s/agent/internal/ipx"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

var intern = unique.Make[string]

// Project turns a decoded pod into its row. managedFields, annotations, labels, spec and the
// rest of the object are dropped here; nothing of p is retained.
func Project(p *corev1.Pod) *Row {
	var b ipx.Builder
	collectPodIPs(p, &b)
	var r *Row
	if t, ok := b.Single(); ok {
		r = newRowIP1(&p.ObjectMeta, t) // the usual single address shares the row's allocation
	} else {
		r = newRow(&p.ObjectMeta, 5)
		r.ips = b.Set()
	}
	reason, restarts, ready, total := PodStatus(p)
	r.putN(int64(ready))
	r.putN(int64(total))
	r.putS(reason)
	r.putN(int64(restarts))
	r.putS(p.Spec.NodeName)
	r.tone = StatusTone(reason, p.Status.Phase, ready, total)
	if f := podFacts(p); f != nil {
		r.facts = f
	}
	return r
}

// ProjectBookmark converts a bookmark event object (only resourceVersion and annotations matter).
func ProjectBookmark(p *corev1.Pod) *Row {
	return bookmarkRow(p.ResourceVersion, p.Annotations)
}

func bookmarkRow(rv string, annotations map[string]string) *Row {
	r := &Row{rv: strings.Clone(rv), ns: emptyStr, ownerKind: emptyStr, ownerName: emptyStr}
	if annotations[metav1.InitialEventsAnnotationKey] == "true" {
		r.flags |= flagInitialEventsEnd
	}
	return r
}

// PodStatus mirrors the status column of `kubectl get pods` (printPod in kubectl's printers),
// including native sidecars (init containers with restartPolicy Always) and SchedulingGated.
// It returns the status reason, total restarts, ready containers and total containers.
func PodStatus(pod *corev1.Pod) (reason string, restarts, ready, total int) {
	reason = string(pod.Status.Phase)
	if pod.Status.Reason != "" {
		reason = pod.Status.Reason
	}
	for i := range pod.Status.Conditions {
		if c := &pod.Status.Conditions[i]; c.Type == corev1.PodScheduled && c.Reason == corev1.PodReasonSchedulingGated {
			reason = corev1.PodReasonSchedulingGated
		}
	}
	total = len(pod.Spec.Containers)
	sidecar := make(map[string]bool)
	for i := range pod.Spec.InitContainers {
		ic := &pod.Spec.InitContainers[i]
		if ic.RestartPolicy != nil && *ic.RestartPolicy == corev1.ContainerRestartPolicyAlways {
			sidecar[ic.Name] = true
			total++
		}
	}

	initializing := false
	sidecarRestarts := 0
	for i := range pod.Status.InitContainerStatuses {
		st := &pod.Status.InitContainerStatuses[i]
		restarts += int(st.RestartCount)
		switch {
		case st.State.Terminated != nil && st.State.Terminated.ExitCode == 0:
			continue
		case sidecar[st.Name] && st.Started != nil && *st.Started:
			sidecarRestarts += int(st.RestartCount)
			if st.Ready {
				ready++
			}
			continue
		case st.State.Terminated != nil:
			if len(st.State.Terminated.Reason) == 0 {
				if st.State.Terminated.Signal != 0 {
					reason = fmt.Sprintf("Init:Signal:%d", st.State.Terminated.Signal)
				} else {
					reason = fmt.Sprintf("Init:ExitCode:%d", st.State.Terminated.ExitCode)
				}
			} else {
				reason = "Init:" + st.State.Terminated.Reason
			}
			initializing = true
		case st.State.Waiting != nil && len(st.State.Waiting.Reason) > 0 && st.State.Waiting.Reason != "PodInitializing":
			reason = "Init:" + st.State.Waiting.Reason
			initializing = true
		default:
			reason = fmt.Sprintf("Init:%d/%d", i, len(pod.Spec.InitContainers))
			initializing = true
		}
		break
	}
	if !initializing || podInitialized(pod) {
		restarts = sidecarRestarts
		hasRunning := false
		for i := len(pod.Status.ContainerStatuses) - 1; i >= 0; i-- {
			st := &pod.Status.ContainerStatuses[i]
			restarts += int(st.RestartCount)
			switch {
			case st.State.Waiting != nil && st.State.Waiting.Reason != "":
				reason = st.State.Waiting.Reason
			case st.State.Terminated != nil && st.State.Terminated.Reason != "":
				reason = st.State.Terminated.Reason
			case st.State.Terminated != nil && st.State.Terminated.Reason == "":
				if st.State.Terminated.Signal != 0 {
					reason = fmt.Sprintf("Signal:%d", st.State.Terminated.Signal)
				} else {
					reason = fmt.Sprintf("ExitCode:%d", st.State.Terminated.ExitCode)
				}
			case st.Ready && st.State.Running != nil:
				hasRunning = true
				ready++
			}
		}
		if reason == "Completed" && hasRunning {
			if podReady(pod) {
				reason = "Running"
			} else {
				reason = "NotReady"
			}
		}
	}
	terminal := pod.Status.Phase == corev1.PodSucceeded || pod.Status.Phase == corev1.PodFailed
	if pod.DeletionTimestamp != nil && pod.Status.Reason == "NodeLost" {
		reason = "Unknown"
	} else if pod.DeletionTimestamp != nil && !terminal {
		reason = "Terminating"
	}
	return
}

func podInitialized(pod *corev1.Pod) bool {
	for i := range pod.Status.Conditions {
		if pod.Status.Conditions[i].Type == corev1.PodInitialized {
			return pod.Status.Conditions[i].Status == corev1.ConditionTrue
		}
	}
	return false
}

func podReady(pod *corev1.Pod) bool {
	for i := range pod.Status.Conditions {
		if pod.Status.Conditions[i].Type == corev1.PodReady {
			return pod.Status.Conditions[i].Status == corev1.ConditionTrue
		}
	}
	return false
}

// StatusTone classifies a status reason: danger for crash/pull/OOM/Error/Failed, warning for
// Pending/ContainerCreating/Terminating/not-ready Running, success for ready Running, info for
// Succeeded/Completed.
func StatusTone(reason string, phase corev1.PodPhase, ready, total int) uint8 {
	r := strings.TrimPrefix(reason, "Init:")
	switch r {
	case "Completed", "Succeeded":
		return ToneInfo
	case "Running":
		if ready >= total {
			return ToneSuccess
		}
		return ToneWarning
	case "Pending", "ContainerCreating", "PodInitializing", "Terminating", "NotReady", "Unknown", "SchedulingGated":
		return ToneWarning
	case "CrashLoopBackOff", "ImagePullBackOff", "ErrImagePull", "OOMKilled", "Error", "Failed", "Evicted",
		"InvalidImageName", "ErrImageNeverPull", "RunContainerError", "StartError", "DeadlineExceeded",
		"ContainerCannotRun", "NodeAffinity", "NodeLost", "Shutdown", "UnexpectedAdmissionError":
		return ToneDanger
	}
	switch {
	case strings.HasPrefix(r, "Signal:"), strings.HasPrefix(r, "ExitCode:"),
		strings.HasPrefix(r, "CreateContainer"), strings.HasSuffix(r, "Error"):
		return ToneDanger
	case len(r) > 2 && r[0] >= '0' && r[0] <= '9' && strings.Contains(r, "/"): // "Init:0/2"
		return ToneWarning
	}
	if phase == corev1.PodFailed {
		return ToneDanger
	}
	if strings.HasPrefix(reason, "Init:") {
		return ToneWarning
	}
	return ToneNeutral
}
