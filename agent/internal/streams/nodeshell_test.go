package streams

import (
	"context"
	"encoding/json"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/zyno-io/v8s/agent/internal/kube"
	"github.com/zyno-io/v8s/agent/pkg/protocol"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestNodeShellPodSpec(t *testing.T) {
	now := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	p := NodeShellPod(NodeShellSpec{Node: "worker-1", Namespace: "v8s-system", Image: "busybox@sha256:abc", User: "sean", Correlation: "corr1", TTL: time.Hour, Now: now})
	s := p.Spec
	if p.GenerateName != "v8s-node-shell-" || p.Namespace != "v8s-system" || s.NodeName != "worker-1" || !s.HostPID || !s.HostNetwork || !s.HostIPC ||
		s.RestartPolicy != corev1.RestartPolicyNever || *s.ActiveDeadlineSeconds != 3600 || *s.TerminationGracePeriodSeconds != 0 {
		t.Fatalf("%+v", p)
	}
	if len(s.Tolerations) != 1 || s.Tolerations[0].Operator != corev1.TolerationOpExists {
		t.Fatalf("tolerations %+v", s.Tolerations)
	}
	c := s.Containers[0]
	if !c.Stdin || !c.TTY || !*c.SecurityContext.Privileged || c.Image != "busybox@sha256:abc" ||
		strings.Join(c.Command, " ") != "nsenter --target 1 --mount --uts --ipc --net --pid -- sh -l" {
		t.Fatalf("container %+v", c)
	}
	if p.Labels["app.kubernetes.io/managed-by"] != "v8s" || p.Labels["v8s.io/correlation"] != "corr1" || p.Labels["v8s.io/node-hash"] == "" ||
		p.Annotations["v8s.io/user"] != "sean" || p.Annotations["v8s.io/expires-at"] != "2026-01-02T04:04:05Z" || p.Annotations["v8s.io/node"] != "worker-1" {
		t.Fatalf("meta %+v %+v", p.Labels, p.Annotations)
	}
	if strings.Contains(DefaultNodeShellImage, ":latest") || !strings.Contains(DefaultNodeShellImage, "@sha256:") {
		t.Fatalf("default image must be pinned by digest: %s", DefaultNodeShellImage)
	}
	// long node names still give a valid label value
	long := NodeShellPod(NodeShellSpec{Node: strings.Repeat("n", 200), Namespace: "x", Now: now, TTL: time.Hour})
	if len(long.Labels["v8s.io/node-hash"]) > 63 {
		t.Fatal("label too long")
	}
}

func TestExpiredNodeShellPods(t *testing.T) {
	now := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	mk := func(name, expires string, managed bool) corev1.Pod {
		p := corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: name, Labels: map[string]string{}, Annotations: map[string]string{"v8s.io/expires-at": expires}}}
		if managed {
			p.Labels["app.kubernetes.io/managed-by"] = "v8s"
		}
		return p
	}
	pods := []corev1.Pod{
		mk("v8s-node-shell-old", "2026-01-02T03:00:00Z", true),
		mk("v8s-node-shell-new", "2026-01-02T04:00:00Z", true),
		mk("v8s-node-shell-bad", "garbage", true),
		mk("other-old", "2026-01-02T03:00:00Z", true),
		mk("v8s-node-shell-foreign", "2026-01-02T03:00:00Z", false),
	}
	got := ExpiredNodeShellPods(pods, now)
	if len(got) != 1 || got[0] != "v8s-node-shell-old" {
		t.Fatalf("%v", got)
	}
}

func TestNodeShellSettingsPerCluster(t *testing.T) {
	m := New(Config{Settings: func() protocol.Settings {
		return protocol.Settings{NodeShell: map[string]protocol.NodeShellSettings{"*": {Image: "reg.example/bb@sha256:1"}, "c2": {Namespace: "ops", PriorityClass: "high"}}}
	}})
	a, b := m.nodeShellSettings("c1"), m.nodeShellSettings("c2")
	if a.Image != "reg.example/bb@sha256:1" || a.Namespace != "v8s-system" || b.Image != "reg.example/bb@sha256:1" || b.Namespace != "ops" || b.PriorityClass != "high" {
		t.Fatalf("%+v %+v", a, b)
	}
	if d := New(Config{}).nodeShellSettings("x"); d.Image != DefaultNodeShellImage || d.Namespace != "v8s-system" {
		t.Fatalf("%+v", d)
	}
}

// Session: GC of expired pods, the namespace with the privileged PSA label, create, attach, delete on end.
func TestNodeShellSessionLifecycle(t *testing.T) {
	old := nodeShellWait
	nodeShellWait = 3 * time.Second
	defer func() { nodeShellWait = old }()
	expired := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: "v8s-system", Name: "v8s-node-shell-stale",
		Labels:      map[string]string{"app.kubernetes.io/managed-by": "v8s"},
		Annotations: map[string]string{"v8s.io/expires-at": time.Now().Add(-time.Hour).UTC().Format(time.RFC3339)}}}
	m, _, rd := newMgr(t, expired)
	var attached atomicString
	rd.execFn = func(req kube.ExecRequest) error {
		if !req.Attach {
			t.Errorf("first session must attach: %+v", req)
		}
		attached.Store(req.Pod)
		// the pod is running by now: mark it so the wait loop passes before this is called
		buf := make([]byte, 16)
		_, _ = req.Stdin.Read(buf)
		_, _ = req.Stdout.Write([]byte("Linux fake\n"))
		_, _ = io.Copy(io.Discard, req.Stdin) // the shell runs until the session closes
		return nil
	}
	// the "kubelet": makes created pods run
	cs := rd.cs
	go func() {
		for i := 0; i < 200; i++ {
			l, _ := cs.CoreV1().Pods("v8s-system").List(context.Background(), metav1.ListOptions{})
			for _, p := range l.Items {
				if strings.HasPrefix(p.Name, "v8s-node-shell-") && p.Status.Phase != corev1.PodRunning && p.Name != "v8s-node-shell-stale" {
					p.Status.Phase = corev1.PodRunning
					p.Status.ContainerStatuses = []corev1.ContainerStatus{{Name: "shell", State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}}}}
					_, _ = cs.CoreV1().Pods("v8s-system").UpdateStatus(context.Background(), &p, metav1.UpdateOptions{})
				}
			}
			time.Sleep(20 * time.Millisecond)
		}
	}()
	res, err := m.Open(context.Background(), protocol.StreamOpenParams{Type: "nodeshell", Cluster: "c1", Node: "worker-1"})
	if err != nil {
		t.Fatal(err)
	}
	tr, done := serve(t, m, res.StreamID)
	tr.in <- msg{true, append([]byte{protocol.StreamChanStdin}, "uname -a\n"...)}
	var out string
	var ready protocol.StreamReady
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) && !strings.Contains(out, "Linux") {
		f, ok := tr.next(100 * time.Millisecond)
		if !ok {
			continue
		}
		if f.binary {
			out += string(f.data[1:])
		} else {
			var sm protocol.StreamServerMsg
			_ = json.Unmarshal(f.data, &sm)
			if sm.Ready != nil {
				ready = *sm.Ready
			}
		}
	}
	if !strings.Contains(out, "Linux") || !ready.Attached || !strings.HasPrefix(ready.Pod, "v8s-node-shell-") {
		t.Fatalf("out=%q ready=%+v", out, ready)
	}
	// namespace was created with the privileged pod-security label; the expired pod was collected
	ns, err := cs.CoreV1().Namespaces().Get(context.Background(), "v8s-system", metav1.GetOptions{})
	if err != nil || ns.Labels["pod-security.kubernetes.io/enforce"] != "privileged" {
		t.Fatalf("namespace %v %+v", err, ns)
	}
	if _, err := cs.CoreV1().Pods("v8s-system").Get(context.Background(), "v8s-node-shell-stale", metav1.GetOptions{}); err == nil {
		t.Fatal("expired pod was not collected")
	}
	created, err := cs.CoreV1().Pods("v8s-system").Get(context.Background(), ready.Pod, metav1.GetOptions{})
	if err != nil || created.Spec.NodeName != "worker-1" || !*created.Spec.Containers[0].SecurityContext.Privileged {
		t.Fatalf("pod %v %+v", err, created)
	}
	// the session ends: the pod is deleted (cleanup layer 1)
	tr.Close(CloseNormal, "")
	<-done
	if _, err := cs.CoreV1().Pods("v8s-system").Get(context.Background(), ready.Pod, metav1.GetOptions{}); err == nil {
		t.Fatal("pod not deleted when the session ended")
	}
	_ = io.EOF
}

type atomicString struct{ v chan string }

func (a *atomicString) Store(s string) {
	if a.v == nil {
		a.v = make(chan string, 4)
	}
	select {
	case a.v <- s:
	default:
	}
}
