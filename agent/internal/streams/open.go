package streams

import (
	"context"
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/zyno-io/v8s/agent/internal/kube"
	"github.com/zyno-io/v8s/agent/internal/ops"
	"github.com/zyno-io/v8s/agent/pkg/protocol"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/tools/remotecommand"
)

const podsGVR = "v1/pods"

func podRefOf(p protocol.StreamOpenParams) (*protocol.ObjectRef, error) {
	if p.Ref == nil || p.Ref.Name == "" || p.Ref.Cluster == "" {
		return nil, ops.NewError("bad_request", "ref (cluster, namespace, name) is required")
	}
	if p.Ref.GVR != "" && p.Ref.GVR != podsGVR {
		return nil, ops.NewError("bad_request", "ref must be a pod (v1/pods)")
	}
	return p.Ref, nil
}

// defaultContainer is the container a command runs in when none is named: the kubectl default-container
// annotation, else the first container.
func defaultContainer(pod *corev1.Pod) string {
	if c := pod.Annotations["kubectl.kubernetes.io/default-container"]; c != "" {
		return c
	}
	if len(pod.Spec.Containers) > 0 {
		return pod.Spec.Containers[0].Name
	}
	return ""
}

// ---- logs ------------------------------------------------------------------------------

// workloadSelector derives the pod label selector of a workload (spec.selector) or a service (spec.selector map).
func workloadSelector(o *unstructured.Unstructured) (string, error) {
	if m, ok, _ := unstructured.NestedStringMap(o.Object, "spec", "selector"); ok && o.GetKind() == "Service" {
		return labels.SelectorFromSet(m).String(), nil
	}
	raw, ok, _ := unstructured.NestedMap(o.Object, "spec", "selector")
	if !ok {
		return "", ops.NewError("bad_request", "%s %s has no spec.selector", o.GetKind(), o.GetName())
	}
	var ls metav1.LabelSelector
	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(raw, &ls); err != nil {
		return "", ops.NewError("bad_request", "bad selector: %s", err.Error())
	}
	sel, err := metav1.LabelSelectorAsSelector(&ls)
	if err != nil {
		return "", ops.NewError("bad_request", "bad selector: %s", err.Error())
	}
	return sel.String(), nil
}

// logTargets expands pods into the containers whose logs exist (started pods only).
func logTargets(pods []corev1.Pod, container string) []LogTarget {
	var out []LogTarget
	for i := range pods {
		p := &pods[i]
		if p.Status.Phase == corev1.PodPending {
			continue
		}
		for _, c := range p.Spec.Containers {
			if container == "" || c.Name == container {
				out = append(out, LogTarget{Pod: p.Name, Container: c.Name})
			}
		}
	}
	sortTargets(out)
	return out
}

func (m *Manager) openLogs(ctx context.Context, p protocol.StreamOpenParams) (protocol.StreamOpenResult, error) {
	var res protocol.StreamOpenResult
	cluster, ns := "", ""
	var selector, podName string
	var ref *protocol.ObjectRef
	switch {
	case p.Selector != nil && p.Ref == nil:
		cluster, ns, selector = p.Selector.Cluster, p.Selector.Namespace, p.Selector.LabelSelector
		if cluster == "" || ns == "" || selector == "" {
			return res, ops.NewError("bad_request", "selector needs cluster, namespace and labelSelector")
		}
		if _, err := labels.Parse(selector); err != nil {
			return res, ops.NewError("bad_request", "bad labelSelector: %s", err.Error())
		}
	case p.Ref != nil && p.Ref.Name != "" && p.Ref.Cluster != "":
		ref = p.Ref
		cluster, ns = ref.Cluster, ref.Namespace
		if ref.GVR == "" || ref.GVR == podsGVR {
			podName = ref.Name
		}
	default:
		return res, ops.NewError("bad_request", "logs needs ref or selector")
	}
	routed, rt, err := m.route(ctx, cluster, "get", podsGVR, "log", ns, podName)
	if err != nil {
		return res, err
	}
	cs := rt.Clientset()
	spec := &LogSpec{Namespace: ns, Follow: p.Follow, Previous: p.Previous, Timestamps: p.Timestamps, SinceSeconds: p.SinceSeconds, TailLines: p.TailLines}
	if spec.TailLines == nil && !p.Download {
		t := int64(protocol.DefaultLogTailLines)
		spec.TailLines = &t
	}
	if p.Download {
		spec.Follow = false
	}
	switch {
	case podName != "":
		pod, gerr := cs.CoreV1().Pods(ns).Get(ctx, podName, metav1.GetOptions{})
		if gerr != nil {
			return res, ops.MapError(gerr, routed.ViaWrite)
		}
		c := p.Container
		if c == "" {
			c = defaultContainer(pod)
		}
		spec.Targets = []LogTarget{{Pod: podName, Container: c}}
	default:
		if ref != nil { // a workload or service: its pods by spec.selector
			g, perr := parseRefGVR(ref.GVR)
			if perr != nil {
				return res, perr
			}
			obj, gerr := routed.Conn.Dynamic().Resource(g).Namespace(ns).Get(ctx, ref.Name, metav1.GetOptions{})
			if gerr != nil {
				return res, ops.MapError(gerr, routed.ViaWrite)
			}
			if selector, err = workloadSelector(obj); err != nil {
				return res, err
			}
		}
		pods, lerr := cs.CoreV1().Pods(ns).List(ctx, metav1.ListOptions{LabelSelector: selector})
		if lerr != nil {
			return res, ops.MapError(lerr, routed.ViaWrite)
		}
		spec.Targets = logTargets(pods.Items, p.Container)
		if len(spec.Targets) == 0 {
			return res, ops.NewError("not_found", "no started pod matches %q in %s", selector, ns)
		}
		if len(spec.Targets) > maxLogPods {
			return res, ops.NewError("bad_request", "%d containers match; a merged log stream follows at most %d, narrow the selector", len(spec.Targets), maxLogPods)
		}
	}
	open := kubeLogOpener(rt)
	res.UsedContext = routed.ContextID
	if p.Download {
		res.StreamID = m.add(&pendingStream{typ: "download", download: func(ctx context.Context, w io.Writer) error { return DownloadLog(ctx, open, spec, w) }})
		res.DownloadURL = "/stream/download?streamId=" + res.StreamID
		return res, nil
	}
	via := routed.ViaWrite
	res.StreamID = m.add(&pendingStream{typ: "logs", run: func(ctx context.Context, t Transport) {
		lctx, lim := m.limitsFor(ctx, via)
		m.runLogs(lctx, t, open, spec, lim)
	}})
	return res, nil
}

func parseRefGVR(s string) (schema.GroupVersionResource, error) {
	parts := strings.Split(s, "/")
	switch len(parts) {
	case 2:
		return schema.GroupVersionResource{Version: parts[0], Resource: parts[1]}, nil
	case 3:
		return schema.GroupVersionResource{Group: parts[0], Version: parts[1], Resource: parts[2]}, nil
	}
	return schema.GroupVersionResource{}, ops.NewError("bad_request", "invalid gvr %q", s)
}

// ---- exec ------------------------------------------------------------------------------

func execRunner(rt kube.Runtime, ns, pod, container string, tty bool, attach bool) Runner {
	return func(ctx context.Context, cmd []string, in io.Reader, out, errw io.Writer, resize remotecommand.TerminalSizeQueue) error {
		req := kube.ExecRequest{Namespace: ns, Pod: pod, Container: container, Command: cmd, Attach: attach, TTY: tty, Stdin: in, Stdout: out, Stderr: errw, Resize: resize}
		return rt.Exec(ctx, req)
	}
}

func shellCandidates(cmd []string) [][]string {
	if len(cmd) > 0 {
		return [][]string{cmd}
	}
	out := make([][]string, len(DefaultShells))
	for i, s := range DefaultShells {
		out[i] = []string{s}
	}
	return out
}

func (m *Manager) openExec(ctx context.Context, p protocol.StreamOpenParams) (protocol.StreamOpenResult, error) {
	var res protocol.StreamOpenResult
	ref, err := podRefOf(p)
	if err != nil {
		return res, err
	}
	routed, rt, err := m.route(ctx, ref.Cluster, "create", podsGVR, "exec", ref.Namespace, ref.Name)
	if err != nil {
		return res, err
	}
	container := p.Container
	if container == "" {
		pod, gerr := rt.Clientset().CoreV1().Pods(ref.Namespace).Get(ctx, ref.Name, metav1.GetOptions{})
		if gerr != nil {
			return res, m.mapRouted(ctx, gerr, routed, "get", podsGVR, "", ref.Namespace, ref.Name)
		}
		container = defaultContainer(pod)
	}
	tty := p.TTY == nil || *p.TTY
	cands := shellCandidates(p.Command)
	runner := execRunner(rt, ref.Namespace, ref.Name, container, tty, false)
	via, used := routed.ViaWrite, routed.ContextID
	res.UsedContext = used
	res.StreamID = m.add(&pendingStream{typ: "exec", run: func(ctx context.Context, t Transport) {
		lctx, lim := m.limitsFor(ctx, via)
		defer lim.Stop()
		s := &interactive{t: t, touch: lim.Touch}
		_ = s.run(lctx, cands, protocol.StreamReady{UsedContext: used, Container: container}, runner)
	}})
	return res, nil
}

// ---- debug -----------------------------------------------------------------------------

func debugPatch(name, image, target string) []byte {
	return []byte(fmt.Sprintf(`{"spec":{"ephemeralContainers":[{"name":%q,"image":%q,"imagePullPolicy":"IfNotPresent","stdin":true,"tty":true,"targetContainerName":%q}]}}`, name, image, target))
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
