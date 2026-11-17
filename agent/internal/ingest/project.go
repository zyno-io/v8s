package ingest

import (
	"fmt"
	"strings"
	"unique"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

var intern = unique.Make[string]

// Project turns a decoded pod into its row. managedFields, annotations, labels, spec and the
// rest of the object are dropped here; nothing of p is retained.
func Project(p *corev1.Pod) *PodRow {
	// One allocation backs uid, name and resourceVersion.
	all := string(p.UID) + p.Name + p.ResourceVersion
	r := &PodRow{
		uid:  all[:len(p.UID)],
		name: all[len(p.UID) : len(p.UID)+len(p.Name)],
		rv:   all[len(p.UID)+len(p.Name):],
		ns:   intern(p.Namespace),
		node: intern(p.Spec.NodeName),
	}
	if !p.CreationTimestamp.IsZero() {
		r.created = p.CreationTimestamp.UnixMilli()
	}
	if p.DeletionTimestamp != nil {
		r.flags |= flagDeleting
	}
	if len(p.OwnerReferences) > 0 {
		o := &p.OwnerReferences[0]
		for i := range p.OwnerReferences {
			if p.OwnerReferences[i].Controller != nil && *p.OwnerReferences[i].Controller {
				o = &p.OwnerReferences[i]
				break
			}
		}
		r.ownerKind, r.ownerName = intern(o.Kind), intern(o.Name)
	}
	reason, restarts, ready, total := PodStatus(p)
	r.status = intern(reason)
	r.restarts = int32(restarts)
	r.ready, r.total = uint16(ready), uint16(total)
	r.tone = StatusTone(reason, p.Status.Phase, ready, total)
	return r
}

// ProjectBookmark converts a bookmark event object (only resourceVersion and annotations matter).
func ProjectBookmark(p *corev1.Pod) *PodRow {
	r := &PodRow{rv: strings.Clone(p.ResourceVersion)}
	if p.Annotations[metav1.InitialEventsAnnotationKey] == "true" {
		r.flags |= flagInitialEventsEnd
	}
	return r
}

// PodStatus mirrors the status column of `kubectl get pods` (printPod in kubectl's printers).
// It returns the status reason, total restarts, ready containers and total containers.
func PodStatus(pod *corev1.Pod) (reason string, restarts, ready, total int) {
	reason = string(pod.Status.Phase)
	if pod.Status.Reason != "" {
		reason = pod.Status.Reason
	}
	total = len(pod.Spec.Containers)

	initializing := false
	for i := range pod.Status.InitContainerStatuses {
		st := &pod.Status.InitContainerStatuses[i]
		restarts += int(st.RestartCount)
		switch {
		case st.State.Terminated != nil && st.State.Terminated.ExitCode == 0:
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
	if !initializing {
		restarts = 0
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
			}
			if st.Ready {
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
	} else {
		for i := range pod.Status.ContainerStatuses {
			if pod.Status.ContainerStatuses[i].Ready {
				ready++
			}
		}
	}
	if pod.DeletionTimestamp != nil && pod.Status.Reason == "NodeLost" {
		reason = "Unknown"
	} else if pod.DeletionTimestamp != nil {
		reason = "Terminating"
	}
	return
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
	case "Pending", "ContainerCreating", "PodInitializing", "Terminating", "NotReady", "Unknown":
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
