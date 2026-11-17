package fake

// The fake runtime: everything the stream, drain and Helm features need from a cluster, without one. Pod logs
// are synthetic lines at a configurable rate, exec is an echo shell, port-forward serves a tiny HTTP page, and
// drain/Helm run against a client-go fake clientset seeded with a small demo set (nodes, pods with a
// PodDisruptionBudget block, release Secrets).

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/zyno-io/v8s/agent/internal/flux"
	"github.com/zyno-io/v8s/agent/internal/helm"
	"github.com/zyno-io/v8s/agent/internal/kube"
	corev1 "k8s.io/api/core/v1"
	policyv1 "k8s.io/api/policy/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	k8sfake "k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/rest"
	ktesting "k8s.io/client-go/testing"
	utilexec "k8s.io/client-go/util/exec"
)

var _ kube.Runtime = (*Backend)(nil)

func (b *Backend) RESTConfig() *rest.Config { return nil }

// Clientset returns the cluster's fake clientset (shared by the read and the admin context).
func (b *Backend) Clientset() kubernetes.Interface { return b.w.clientset(b.c) }

type clientsetHolder struct {
	once sync.Once
	cs   *k8sfake.Clientset
}

func (w *World) clientset(c *Cluster) *k8sfake.Clientset {
	w.csh.once.Do(func() { w.csh.cs = buildClientset(w, c) })
	return w.csh.cs
}

// Clientset returns the fake clientset of a world (tests).
func (w *World) Clientset(c *Cluster) *k8sfake.Clientset { return w.clientset(c) }

func objMetaOf(ns, name string, lbl map[string]string) metav1.ObjectMeta {
	return metav1.ObjectMeta{Name: name, Namespace: ns, Labels: lbl, UID: types.UID("demo-uid-" + ns + "-" + name),
		CreationTimestamp: metav1.NewTime(time.Now().Add(-6 * time.Hour))}
}

func ctrl(kind, name string) []metav1.OwnerReference {
	t := true
	return []metav1.OwnerReference{{APIVersion: "apps/v1", Kind: kind, Name: name, UID: types.UID("uid-" + name), Controller: &t}}
}

func readyPod(ns, name, node string, lbl map[string]string, owner []metav1.OwnerReference) *corev1.Pod {
	m := objMetaOf(ns, name, lbl)
	m.OwnerReferences = owner
	return &corev1.Pod{ObjectMeta: m,
		Spec: corev1.PodSpec{NodeName: node, Containers: []corev1.Container{{Name: "app", Image: "registry.example.com/demo/app:1"}}},
		Status: corev1.PodStatus{Phase: corev1.PodRunning, Conditions: []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue}},
			ContainerStatuses: []corev1.ContainerStatus{{Name: "app", Ready: true, State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}}}}}}
}

// drainDemo seeds nodes node-3 (blocked by a PDB until two attempts passed) and node-4..8 (plain).
func drainDemoObjects() []runtime.Object {
	var objs []runtime.Object
	web := map[string]string{"app": "web"}
	for i := 0; i < 3; i++ {
		objs = append(objs, readyPod("team-1", fmt.Sprintf("web-5d9f7c-%d", i), "node-3", web, ctrl("ReplicaSet", "web-5d9f7c")))
	}
	objs = append(objs, &policyv1.PodDisruptionBudget{ObjectMeta: objMetaOf("team-1", "web-pdb", nil),
		Spec:   policyv1.PodDisruptionBudgetSpec{Selector: &metav1.LabelSelector{MatchLabels: web}},
		Status: policyv1.PodDisruptionBudgetStatus{DisruptionsAllowed: 0, CurrentHealthy: 3, ExpectedPods: 3}})
	objs = append(objs, readyPod("kube-system", "node-exporter-x7k2p", "node-3", map[string]string{"app": "node-exporter"}, ctrl("DaemonSet", "node-exporter")))
	cache := readyPod("team-2", "cache-6c8b9d-0", "node-3", map[string]string{"app": "cache"}, ctrl("ReplicaSet", "cache-6c8b9d"))
	cache.Spec.Volumes = []corev1.Volume{{Name: "scratch", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}}}
	objs = append(objs, cache)
	objs = append(objs, readyPod("team-2", "debug-shell", "node-3", map[string]string{"run": "debug"}, nil)) // unmanaged
	done := readyPod("team-2", "migrate-job-x9q", "node-3", nil, ctrl("Job", "migrate-job"))
	done.Status.Phase = corev1.PodSucceeded
	objs = append(objs, done)
	for n := 4; n <= 8; n++ {
		node := fmt.Sprintf("node-%d", n)
		for i := 0; i < 3; i++ {
			objs = append(objs, readyPod("team-3", fmt.Sprintf("api-7b6d-%d-%d", n, i), node, map[string]string{"app": "api"}, ctrl("ReplicaSet", "api-7b6d")))
		}
	}
	return objs
}

func buildClientset(w *World, c *Cluster) *k8sfake.Clientset {
	objs := drainDemoObjects()
	for i := 0; i < nodes; i++ {
		n := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: fmt.Sprintf("node-%d", i), UID: types.UID("uid-node-" + fmt.Sprint(i))},
			Status: corev1.NodeStatus{Conditions: []corev1.NodeCondition{{Type: corev1.NodeReady, Status: corev1.ConditionTrue}}}}
		objs = append(objs, n)
	}
	for _, ns := range []string{"podinfo", "default", "monitoring", "flux-system", "kube-system"} {
		objs = append(objs, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}})
	}
	for _, s := range helm.DemoSecrets(w.cluster, time.Now()) {
		objs = append(objs, s)
	}
	cs := k8sfake.NewSimpleClientset(objs...)
	// eviction support for kubectl's drain helper (discovery)
	setEvictionDiscovery(cs)
	attempts := map[string]int{}
	var mu sync.Mutex
	// list pods: field selector spec.nodeName, plus the generated world pods for label selectors
	cs.PrependReactor("list", "pods", func(a ktesting.Action) (bool, runtime.Object, error) {
		la := a.(ktesting.ListAction)
		ns := la.GetNamespace()
		r := la.GetListRestrictions()
		obj, err := cs.Tracker().List(corev1.SchemeGroupVersion.WithResource("pods"), corev1.SchemeGroupVersion.WithKind("Pod"), ns)
		if err != nil {
			return true, nil, err
		}
		pl := obj.(*corev1.PodList)
		out := &corev1.PodList{}
		node, hasNode := r.Fields.RequiresExactMatch("spec.nodeName")
		for i := range pl.Items {
			p := &pl.Items[i]
			if hasNode && p.Spec.NodeName != node {
				continue
			}
			if !r.Labels.Matches(labels.Set(p.Labels)) {
				continue
			}
			out.Items = append(out.Items, *p)
		}
		if app, ok := appSelector(r.Labels); ok && ns != "" && !hasNode {
			out.Items = append(out.Items, c.podsOfApp(ns, app)...)
		}
		return true, out, nil
	})
	cs.PrependReactor("get", "pods", func(a ktesting.Action) (bool, runtime.Object, error) {
		ga := a.(ktesting.GetAction)
		obj, err := cs.Tracker().Get(corev1.SchemeGroupVersion.WithResource("pods"), ga.GetNamespace(), ga.GetName())
		if err == nil {
			return true, obj, nil
		}
		if u, uerr := c.podObject(ga.GetNamespace(), ga.GetName()); uerr == nil {
			var p corev1.Pod
			if runtime.DefaultUnstructuredConverter.FromUnstructured(u.Object, &p) == nil {
				return true, &p, nil
			}
		}
		return true, nil, apierrors.NewNotFound(schema.GroupResource{Resource: "pods"}, ga.GetName())
	})
	cs.PrependReactor("get", "services", func(a ktesting.Action) (bool, runtime.Object, error) {
		ga := a.(ktesting.GetAction)
		if u, ok := w.Get("v1/services", ga.GetNamespace(), ga.GetName()); ok {
			var s corev1.Service
			if runtime.DefaultUnstructuredConverter.FromUnstructured(u.Object, &s) == nil {
				return true, &s, nil
			}
		}
		return true, nil, apierrors.NewNotFound(schema.GroupResource{Resource: "services"}, ga.GetName())
	})
	// cordon/uncordon through the clientset is mirrored into the world so the node views show it
	cs.PrependReactor("patch", "nodes", func(a ktesting.Action) (bool, runtime.Object, error) {
		pa := a.(ktesting.PatchAction)
		w.mirrorUnschedulable(pa.GetName(), pa.GetPatch())
		return false, nil, nil
	})
	// the Eviction API: a PDB-covered pod is refused (429) twice, as if its replacement were still starting
	cs.PrependReactor("create", "pods", func(a ktesting.Action) (bool, runtime.Object, error) {
		if a.GetSubresource() != "eviction" {
			return false, nil, nil
		}
		ca := a.(ktesting.CreateAction)
		var name, ns string
		switch ev := ca.GetObject().(type) {
		case *policyv1.Eviction:
			name, ns = ev.Name, ev.Namespace
		default:
			if m, ok := ev.(metav1.Object); ok {
				name, ns = m.GetName(), m.GetNamespace()
			}
		}
		obj, err := cs.Tracker().Get(corev1.SchemeGroupVersion.WithResource("pods"), ns, name)
		if err != nil {
			return true, nil, err
		}
		pod := obj.(*corev1.Pod)
		pdbs, _ := cs.Tracker().List(policyv1.SchemeGroupVersion.WithResource("poddisruptionbudgets"), policyv1.SchemeGroupVersion.WithKind("PodDisruptionBudget"), ns)
		for _, pdb := range pdbs.(*policyv1.PodDisruptionBudgetList).Items {
			sel, serr := metav1.LabelSelectorAsSelector(pdb.Spec.Selector)
			if serr != nil || !sel.Matches(labels.Set(pod.Labels)) || pdb.Status.DisruptionsAllowed > 0 {
				continue
			}
			mu.Lock()
			attempts[ns+"/"+name]++
			n := attempts[ns+"/"+name]
			mu.Unlock()
			if n <= 2 {
				return true, nil, apierrors.NewTooManyRequestsError("Cannot evict pod as it would violate the pod's disruption budget.")
			}
		}
		_ = cs.Tracker().Delete(corev1.SchemeGroupVersion.WithResource("pods"), ns, name)
		return true, nil, nil
	})
	return cs
}

func setEvictionDiscovery(cs *k8sfake.Clientset) {
	cs.Resources = []*metav1.APIResourceList{{GroupVersion: "v1", APIResources: []metav1.APIResource{
		{Name: "pods", Namespaced: true, Kind: "Pod"},
		{Name: "pods/eviction", Namespaced: true, Kind: "Eviction", Group: "policy", Version: "v1"},
	}}}
}

func appSelector(s labels.Selector) (string, bool) {
	reqs, _ := s.Requirements()
	for _, r := range reqs {
		if r.Key() == "app" && len(r.Values().List()) == 1 {
			return r.Values().List()[0], true
		}
	}
	return "", false
}

// mirrorUnschedulable applies a cordon patch to the world's node object.
func (w *World) mirrorUnschedulable(node string, patch []byte) {
	val := strings.Contains(string(patch), `"unschedulable":true`)
	if !val && !strings.Contains(string(patch), `"unschedulable":false`) {
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	cur := w.getLocked("v1/nodes", "", node)
	if cur == nil {
		return
	}
	o := cur.DeepCopy()
	_ = unstructured.SetNestedField(o.Object, val, "spec", "unschedulable")
	w.storeLocked("v1/nodes", o, false)
}

// ---- logs ------------------------------------------------------------------------------

func (b *Backend) podExists(ctx context.Context, ns, name string) bool {
	_, err := b.Clientset().CoreV1().Pods(ns).Get(ctx, name, metav1.GetOptions{})
	return err == nil
}

// PodLogs generates synthetic log lines at b.rate lines per second.
func (b *Backend) PodLogs(ctx context.Context, ns, pod string, o *corev1.PodLogOptions) (io.ReadCloser, error) {
	if !b.podExists(ctx, ns, pod) {
		return nil, apierrors.NewNotFound(schema.GroupResource{Resource: "pods"}, pod)
	}
	rate := b.rate
	if rate <= 0 {
		rate = 20
	}
	tail := int64(100)
	if o.TailLines != nil {
		tail = *o.TailLines
	}
	if tail > 100000 {
		tail = 100000
	}
	pr, pw := io.Pipe()
	bw := bufio.NewWriterSize(pw, 32*1024)
	go func() {
		defer pw.Close()
		seq := 0
		emit := func(ts time.Time) error {
			seq++
			if _, err := bw.WriteString(logLine(pod, o.Container, seq, ts, o.Timestamps)); err != nil {
				return err
			}
			return nil
		}
		step := time.Second / time.Duration(rate)
		now := time.Now()
		for i := tail; i > 0; i-- {
			if emit(now.Add(-time.Duration(i)*step)) != nil {
				return
			}
		}
		if bw.Flush() != nil || !o.Follow {
			return
		}
		tick := time.NewTicker(10 * time.Millisecond)
		defer tick.Stop()
		var acc float64
		last := time.Now()
		for {
			select {
			case <-ctx.Done():
				return
			case t := <-tick.C:
				acc += float64(rate) * t.Sub(last).Seconds()
				last = t
				for ; acc >= 1; acc-- {
					if emit(t) != nil {
						return
					}
				}
				if bw.Flush() != nil {
					return
				}
			}
		}
	}()
	go func() { <-ctx.Done(); pr.Close() }()
	return pr, nil
}

func logLine(pod, container string, seq int, ts time.Time, withTS bool) string {
	level, msg := "info", fmt.Sprintf("handled GET /api/v1/items/%d status=200 dur=%dms", seq%977, 3+seq%41)
	switch {
	case seq%97 == 0:
		level, msg = "error", fmt.Sprintf("upstream timeout calling payments after %dms", 2000+seq%500)
	case seq%23 == 0:
		level, msg = "warn", "slow request /api/v1/report took 812ms"
	}
	prefix := ""
	if withTS {
		prefix = ts.UTC().Format(time.RFC3339Nano) + " "
	}
	return fmt.Sprintf("%slevel=%s container=%s pod=%s msg=%q seq=%d\n", prefix, level, container, pod, msg, seq)
}

// ---- exec ------------------------------------------------------------------------------

// Exec runs the echo shell (a shell path or an attach) or a tiny set of commands.
func (b *Backend) Exec(ctx context.Context, req kube.ExecRequest) error {
	if !b.podExists(ctx, req.Namespace, req.Pod) {
		return apierrors.NewNotFound(schema.GroupResource{Resource: "pods"}, req.Pod)
	}
	cmd := req.Command
	if req.Attach || len(cmd) == 0 || isShell(cmd[0]) || cmd[0] == "nsenter" {
		if len(cmd) > 0 && cmd[0] == "/bin/bash" && len(req.Pod)%2 == 1 { // some pods have no bash: shell discovery falls back
			return utilexec.CodeExitError{Err: errors.New(`OCI runtime exec failed: exec failed: unable to start container process: exec: "/bin/bash": stat /bin/bash: no such file or directory`), Code: 126}
		}
		return echoShell(ctx, req)
	}
	switch cmd[0] {
	case "echo":
		_, _ = fmt.Fprintf(req.Stdout, "%s\r\n", strings.Join(cmd[1:], " "))
	case "uname":
		_, _ = fmt.Fprint(req.Stdout, "Linux fake-node 6.8.0-51-generic #52-Ubuntu SMP PREEMPT_DYNAMIC x86_64 GNU/Linux\r\n")
	default:
		return utilexec.CodeExitError{Err: fmt.Errorf("exec: %q: executable file not found in $PATH", cmd[0]), Code: 127}
	}
	return nil
}

func isShell(s string) bool {
	return s == "/bin/bash" || s == "/bin/sh" || s == "/busybox/sh" || s == "sh" || s == "bash"
}

func echoShell(ctx context.Context, req kube.ExecRequest) error {
	prompt := "$ "
	fmt.Fprintf(req.Stdout, "fake shell in %s/%s (echo shell: type text, `exit` to leave)\r\n%s", req.Namespace, req.Pod, prompt)
	if req.Stdin == nil {
		return nil
	}
	var line []byte
	buf := make([]byte, 256)
	for {
		n, err := req.Stdin.Read(buf)
		for _, c := range buf[:n] {
			switch c {
			case '\r', '\n':
				text := strings.TrimSpace(string(line))
				line = line[:0]
				if text == "exit" {
					fmt.Fprint(req.Stdout, "\r\nlogout\r\n")
					return nil
				}
				if text == "" {
					fmt.Fprintf(req.Stdout, "\r\n%s", prompt)
				} else {
					fmt.Fprintf(req.Stdout, "\r\necho: %s\r\n%s", text, prompt)
				}
			case 0x7f, 0x08:
				if len(line) > 0 {
					line = line[:len(line)-1]
					fmt.Fprint(req.Stdout, "\b \b")
				}
			default:
				line = append(line, c)
				_, _ = req.Stdout.Write([]byte{c})
			}
		}
		if err != nil {
			return nil
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
	}
}

// ---- port-forward ----------------------------------------------------------------------

// PortForward binds a loopback listener that answers every connection with a small HTTP page.
func (b *Backend) PortForward(ctx context.Context, ns, pod string, remotePort, localPort int) (*kube.Forward, error) {
	if !b.podExists(ctx, ns, pod) {
		return nil, apierrors.NewNotFound(schema.GroupResource{Resource: "pods"}, pod)
	}
	ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", localPort))
	if err != nil {
		return nil, err
	}
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		fmt.Fprintf(w, "hello from fake pod %s/%s port %d\n", ns, pod, remotePort)
	}), ReadHeaderTimeout: 5 * time.Second}
	done := make(chan struct{})
	go func() { _ = srv.Serve(ln); close(done) }()
	var once sync.Once
	stop := func() { once.Do(func() { _ = srv.Close() }) }
	go func() {
		select {
		case <-ctx.Done():
			stop()
		case <-done:
		}
	}()
	return &kube.Forward{LocalPort: ln.Addr().(*net.TCPAddr).Port, Done: done, Err: func() error { return nil }, Stop: stop}, nil
}

// scheduleReconcileLocked makes Flux objects behave like their controllers: a reconcile request (the
// reconcile.fluxcd.io/requestedAt annotation) is handled about a second later (status.lastHandledReconcileAt).
func (w *World) scheduleReconcileLocked(gvr string, o *unstructured.Unstructured) {
	d := kindByGVR[gvr]
	if d == nil || !flux.IsFluxGroup(d.group()) || o.GetAnnotations()[flux.AnnRequestedAt] == "" {
		return
	}
	if h, _, _ := unstructured.NestedString(o.Object, "status", "lastHandledReconcileAt"); h == o.GetAnnotations()[flux.AnnRequestedAt] {
		return
	}
	ns, name := o.GetNamespace(), o.GetName()
	time.AfterFunc(reconcileDelay, func() {
		w.mu.Lock()
		defer w.mu.Unlock()
		cur := w.getLocked(gvr, ns, name)
		if cur == nil {
			return
		}
		n := cur.DeepCopy()
		if _, changed := flux.FakeReconcile(n); changed {
			w.storeLocked(gvr, n, false)
		}
	})
}

// reconcileDelay is how long a fake Flux controller takes to handle a reconcile request.
var reconcileDelay = time.Second
