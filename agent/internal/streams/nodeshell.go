package streams

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"strings"
	"time"

	"github.com/zyno-io/v8s/agent/internal/ops"
	"github.com/zyno-io/v8s/agent/pkg/protocol"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

// Node shell defaults (docs/operations.md: Node shell).
const (
	DefaultNodeShellNamespace = "v8s-system"
	// DefaultNodeShellImage is a minimal busybox (it ships nsenter), pinned by digest (never :latest). Override per
	// cluster in settings (nodeShell.<cluster>.image, "*" for all) for air-gapped registries.
	DefaultNodeShellImage = "busybox:1.37.0@sha256:a7dbc319b1438115a207e11930dc4bb3e643dcb3ec0bdd694bb36cd8fad289ec"
	nodeShellPrefix       = "v8s-node-shell-"
	labelManagedBy        = "app.kubernetes.io/managed-by"
	labelCorrelation      = "v8s.io/correlation"
	labelNodeHash         = "v8s.io/node-hash"
	annUser               = "v8s.io/user"
	annExpires            = "v8s.io/expires-at"
	annNode               = "v8s.io/node"
)

// NsenterCommand enters the host namespaces of PID 1 and starts a login shell.
var NsenterCommand = []string{"nsenter", "--target", "1", "--mount", "--uts", "--ipc", "--net", "--pid", "--", "sh", "-l"}

// NodeShellSpec parameterizes the node shell pod.
type NodeShellSpec struct {
	Node, Namespace, Image, PriorityClass, User, Correlation string
	TTL                                                      time.Duration
	Now                                                      time.Time
}

func nodeHash(node string) string {
	h := sha1.Sum([]byte(node))
	return hex.EncodeToString(h[:6])
}

// NodeShellPod builds the privileged, node-pinned pod of a node shell. Its container's own command already
// entered the host namespaces, so the agent attaches to its TTY.
func NodeShellPod(s NodeShellSpec) *corev1.Pod {
	ttl := int64(s.TTL / time.Second)
	zero := int64(0)
	priv, tty, stdin := true, true, true
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			GenerateName: nodeShellPrefix,
			Namespace:    s.Namespace,
			Labels:       map[string]string{labelManagedBy: "v8s", labelCorrelation: s.Correlation, labelNodeHash: nodeHash(s.Node)},
			Annotations:  map[string]string{annUser: s.User, annExpires: s.Now.Add(s.TTL).UTC().Format(time.RFC3339), annNode: s.Node},
		},
		Spec: corev1.PodSpec{
			NodeName:                      s.Node,
			HostPID:                       true,
			HostNetwork:                   true,
			HostIPC:                       true,
			RestartPolicy:                 corev1.RestartPolicyNever,
			ActiveDeadlineSeconds:         &ttl, // a hard ceiling even if v8s dies
			TerminationGracePeriodSeconds: &zero,
			Tolerations:                   []corev1.Toleration{{Operator: corev1.TolerationOpExists}},
			PriorityClassName:             s.PriorityClass,
			Containers: []corev1.Container{{
				Name: "shell", Image: s.Image, Command: NsenterCommand, Stdin: stdin, TTY: tty,
				SecurityContext: &corev1.SecurityContext{Privileged: &priv},
			}},
		},
	}
	return pod
}

// ExpiredNodeShellPods names the v8s node shell pods whose expires-at has passed.
func ExpiredNodeShellPods(pods []corev1.Pod, now time.Time) []string {
	var out []string
	for i := range pods {
		p := &pods[i]
		if !strings.HasPrefix(p.Name, nodeShellPrefix) || p.Labels[labelManagedBy] != "v8s" {
			continue
		}
		exp, err := time.Parse(time.RFC3339, p.Annotations[annExpires])
		if err == nil && now.After(exp) {
			out = append(out, p.Name)
		}
	}
	return out
}

func (m *Manager) nodeShellSettings(cluster string) protocol.NodeShellSettings {
	out := protocol.NodeShellSettings{Image: DefaultNodeShellImage, Namespace: DefaultNodeShellNamespace}
	if m.cfg.Settings == nil {
		return out
	}
	s := m.cfg.Settings().NodeShell
	for _, k := range []string{"*", cluster} { // the cluster's own entry wins
		if v, ok := s[k]; ok {
			if v.Image != "" {
				out.Image = v.Image
			}
			if v.Namespace != "" {
				out.Namespace = v.Namespace
			}
			if v.PriorityClass != "" {
				out.PriorityClass = v.PriorityClass
			}
		}
	}
	return out
}

type nodeShells struct{ m *Manager }

func newNodeShells(m *Manager) *nodeShells { return &nodeShells{m} }

// gc deletes expired node shell pods (best effort, through the context the user is using now).
func gcNodeShells(ctx context.Context, cs kubernetes.Interface, ns string, now time.Time) {
	list, err := cs.CoreV1().Pods(ns).List(ctx, metav1.ListOptions{LabelSelector: labelManagedBy + "=v8s"})
	if err != nil {
		return
	}
	for _, name := range ExpiredNodeShellPods(list.Items, now) {
		zero := int64(0)
		_ = cs.CoreV1().Pods(ns).Delete(ctx, name, metav1.DeleteOptions{GracePeriodSeconds: &zero})
	}
}

func ensureNamespace(ctx context.Context, cs kubernetes.Interface, ns string) error {
	if _, err := cs.CoreV1().Namespaces().Get(ctx, ns, metav1.GetOptions{}); err == nil {
		return nil
	} else if !apierrors.IsNotFound(err) {
		return err
	}
	_, err := cs.CoreV1().Namespaces().Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns,
		Labels: map[string]string{"pod-security.kubernetes.io/enforce": "privileged", labelManagedBy: "v8s"}}}, metav1.CreateOptions{})
	if err != nil && !apierrors.IsAlreadyExists(err) {
		return err
	}
	return nil
}

// podReady reports a running shell container.
func podRunning(p *corev1.Pod) (bool, error) {
	if p.Status.Phase == corev1.PodFailed || p.Status.Phase == corev1.PodSucceeded {
		return false, fmt.Errorf("node shell pod ended (%s)", p.Status.Phase)
	}
	for _, st := range p.Status.ContainerStatuses {
		if w := st.State.Waiting; w != nil {
			switch w.Reason {
			case "ErrImagePull", "ImagePullBackOff", "InvalidImageName", "CreateContainerConfigError", "CreateContainerError":
				return false, fmt.Errorf("node shell image: %s: %s", w.Reason, w.Message)
			}
		}
		if st.State.Running != nil {
			return true, nil
		}
	}
	return false, nil
}

var nodeShellWait = 90 * time.Second

func waitPodRunning(ctx context.Context, cs kubernetes.Interface, ns, name string) error {
	deadline := time.Now().Add(nodeShellWait)
	for {
		p, err := cs.CoreV1().Pods(ns).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return err
		}
		if ok, perr := podRunning(p); perr != nil {
			return perr
		} else if ok {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("node shell pod %s did not start within %s", name, nodeShellWait)
		}
		select {
		case <-ctx.Done():
			return context.Cause(ctx)
		case <-time.After(400 * time.Millisecond):
		}
	}
}

func (m *Manager) openNodeShell(ctx context.Context, p protocol.StreamOpenParams) (protocol.StreamOpenResult, error) {
	var res protocol.StreamOpenResult
	cluster, node := p.Cluster, p.Node
	if p.Ref != nil && cluster == "" {
		cluster = p.Ref.Cluster
	}
	if cluster == "" || node == "" {
		return res, ops.NewError("bad_request", "nodeshell needs cluster and node")
	}
	set := m.nodeShellSettings(cluster)
	routed, rt, err := m.route(ctx, cluster, "create", podsGVR, "", set.Namespace, "")
	if err != nil {
		return res, err
	}
	cs := rt.Clientset()
	via, used := routed.ViaWrite, routed.ContextID
	res.UsedContext = used
	res.StreamID = m.add(&pendingStream{typ: "nodeshell", run: func(ctx context.Context, t Transport) {
		lctx, lim := m.limitsFor(ctx, via)
		defer lim.Stop()
		s := &interactive{t: t, touch: lim.Touch}
		_ = s.finish2(lctx, m.runNodeShell(lctx, s, rt, cs, set, cluster, node, routed))
	}})
	return res, nil
}

// runNodeShell starts (or joins) the node's shell pod and runs the session. It returns the session's error.
func (m *Manager) runNodeShell(ctx context.Context, s *interactive, rt kubeRuntime, cs kubernetes.Interface, set protocol.NodeShellSettings, cluster, node string, routed *ops.Routed) error {
	used := routed.ContextID
	now := m.cfg.Now()
	gcNodeShells(ctx, cs, set.Namespace, now) // layer 3 of the cleanup: expired pods of earlier sessions
	if err := ensureNamespace(ctx, cs, set.Namespace); err != nil {
		return fmt.Errorf("namespace %s: %s", set.Namespace, m.mapRouted(ctx, err, routed, "get", "v1/namespaces", "", "", set.Namespace).Error())
	}
	ready := protocol.StreamReady{UsedContext: used, Container: "shell"}
	// Another live node shell pod on this node: an attach would share the first session's TTY, so a further
	// session execs nsenter explicitly.
	list, _ := cs.CoreV1().Pods(set.Namespace).List(ctx, metav1.ListOptions{LabelSelector: labelManagedBy + "=v8s," + labelNodeHash + "=" + nodeHash(node)})
	for i := range list.Items {
		p := &list.Items[i]
		if p.DeletionTimestamp == nil && p.Status.Phase == corev1.PodRunning && strings.HasPrefix(p.Name, nodeShellPrefix) && len(ExpiredNodeShellPods([]corev1.Pod{*p}, now)) == 0 {
			ready.Pod, ready.Attached = p.Name, false
			runner := execRunner(rt, set.Namespace, p.Name, "shell", true, false)
			return s.runOnce(ctx, [][]string{NsenterCommand}, ready, runner)
		}
	}
	spec := NodeShellSpec{Node: node, Namespace: set.Namespace, Image: set.Image, PriorityClass: set.PriorityClass, User: m.cfg.User,
		Correlation: newID()[:16], TTL: m.cfg.NodeShellTTL, Now: now}
	pod := NodeShellPod(spec)
	pod.Name = nodeShellPrefix + randSuffix(5)
	created, err := cs.CoreV1().Pods(set.Namespace).Create(ctx, pod, metav1.CreateOptions{FieldManager: "v8s"})
	if err != nil {
		return fmt.Errorf("could not create the node shell pod: %s", m.mapRouted(ctx, err, routed, "create", podsGVR, "", set.Namespace, "").Error())
	}
	name := created.Name
	defer func() { // layer 1: delete the pod when the session ends
		dctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		defer cancel()
		zero := int64(0)
		_ = cs.CoreV1().Pods(set.Namespace).Delete(dctx, name, metav1.DeleteOptions{GracePeriodSeconds: &zero})
	}()
	if err := waitPodRunning(ctx, cs, set.Namespace, name); err != nil {
		return err
	}
	ready.Pod, ready.Attached = name, true
	runner := execRunner(rt, set.Namespace, name, "shell", true, true)
	return s.runOnce(ctx, [][]string{{"attach"}}, ready, runner)
}
