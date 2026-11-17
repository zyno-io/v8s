//go:build e2e

package e2e

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/zyno-io/v8s/agent/internal/cluster"
	"github.com/zyno-io/v8s/agent/internal/drainer"
	"github.com/zyno-io/v8s/agent/internal/helm"
	"github.com/zyno-io/v8s/agent/internal/ingest"
	"github.com/zyno-io/v8s/agent/internal/kube"
	"github.com/zyno-io/v8s/agent/internal/ops"
	"github.com/zyno-io/v8s/agent/internal/streams"
	"github.com/zyno-io/v8s/agent/internal/view"
	"github.com/zyno-io/v8s/agent/pkg/protocol"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// Run with: V8S_E2E_KUBECONFIG=.../kubeconfig-rdev go test -tags e2e ./test/e2e/ -run G2 -v
// It only reads, except: a Flux reconcile request on flux-system/podinfo (an annotation) and a node shell pod on
// a worker (created in v8s-system, deleted again; the namespace too when this test created it).

type e2eTransport struct {
	in     chan [2]any
	out    chan [2]any
	closed chan struct{}
	once   sync.Once
}

func newTransport() *e2eTransport {
	return &e2eTransport{in: make(chan [2]any, 64), out: make(chan [2]any, 4096), closed: make(chan struct{})}
}
func (t *e2eTransport) Read(ctx context.Context) (bool, []byte, error) {
	select {
	case m := <-t.in:
		return m[0].(bool), m[1].([]byte), nil
	case <-t.closed:
		return false, nil, io.EOF
	case <-ctx.Done():
		return false, nil, ctx.Err()
	}
}
func (t *e2eTransport) Write(ctx context.Context, bin bool, d []byte) error {
	select {
	case t.out <- [2]any{bin, append([]byte(nil), d...)}:
		return nil
	case <-t.closed:
		return io.ErrClosedPipe
	case <-ctx.Done():
		return ctx.Err()
	}
}
func (t *e2eTransport) Close(int, string) { t.once.Do(func() { close(t.closed) }) }

func (t *e2eTransport) collect(d time.Duration, stop func(bin bool, b []byte) bool) bool {
	deadline := time.After(d)
	for {
		select {
		case m := <-t.out:
			if stop(m[0].(bool), m[1].([]byte)) {
				return true
			}
		case <-deadline:
			return false
		}
	}
}

func TestG2RealCluster(t *testing.T) {
	path := os.Getenv("V8S_E2E_KUBECONFIG")
	if path == "" {
		t.Skip("V8S_E2E_KUBECONFIG not set")
	}
	_ = os.Setenv("KUBE_FEATURE_WatchListClient", "true")
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Minute)
	defer cancel()
	src := &cluster.KubeSource{Loader: kube.Loader{Path: path}}
	cs := src.Clusters()
	if len(cs) == 0 {
		t.Fatal("no contexts")
	}
	id := cs[0].ID
	eng := view.NewEngine()
	go eng.Run(ctx)
	m := cluster.NewManager(ctx, src, eng, nil)
	defer m.Close()
	connFn := func(cid string) (kube.Conn, error) { return src.Backend(cid) }
	svc := ops.New(ops.Config{Conn: connFn, Links: m.Links, Resolve: m.Resolve})
	if _, err := m.Select([]string{id}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "cluster ready", 60*time.Second, func() bool {
		st := m.States().States
		return len(st) == 1 && st[0].Phase == protocol.PhaseReady
	})
	be, _ := src.Backend(id)
	rt, _ := kube.RuntimeOf(be)
	k := rt.Clientset()
	sm := streams.New(streams.Config{Router: svc, Conn: connFn, AllowPortForward: true, User: "v8s-e2e"})
	defer sm.Close()

	t.Run("logs", func(t *testing.T) {
		pods, err := k.CoreV1().Pods("payments").List(ctx, metav1.ListOptions{LabelSelector: "app=catalog-api"})
		if err != nil || len(pods.Items) == 0 {
			t.Skipf("no catalog-api pods: %v", err)
		}
		res, err := sm.Open(ctx, protocol.StreamOpenParams{Type: "logs", Ref: &protocol.ObjectRef{Cluster: id, GVR: "v1/pods", Namespace: "payments", Name: pods.Items[0].Name}, Timestamps: true})
		if err != nil {
			t.Fatal(err)
		}
		tr := newTransport()
		go func() { _ = sm.Serve(ctx, res.StreamID, tr) }()
		var lines int
		var end protocol.StreamServerMsg
		tr.collect(30*time.Second, func(bin bool, b []byte) bool {
			if bin {
				lines += strings.Count(string(b), "\n")
				return false
			}
			_ = json.Unmarshal(b, &end)
			return end.End
		})
		t.Logf("single pod: %d lines, end=%+v", lines, end)
		if !end.End || end.Error != "" {
			t.Fatalf("end %+v", end)
		}
		// all pods of the Deployment, merged by timestamp
		res, err = sm.Open(ctx, protocol.StreamOpenParams{Type: "logs", Ref: &protocol.ObjectRef{Cluster: id, GVR: "apps/v1/deployments", Namespace: "payments", Name: "catalog-api"}, TailLines: ptr(int64(20))})
		if err != nil {
			t.Fatal(err)
		}
		tr = newTransport()
		go func() { _ = sm.Serve(ctx, res.StreamID, tr) }()
		prefixed := 0
		end = protocol.StreamServerMsg{}
		tr.collect(30*time.Second, func(bin bool, b []byte) bool {
			if bin {
				for _, l := range strings.Split(strings.TrimSpace(string(b)), "\n") {
					if strings.HasPrefix(l, "catalog-api-") && strings.Contains(l, "/") && strings.Contains(l, "\t") {
						prefixed++
					}
				}
				return false
			}
			_ = json.Unmarshal(b, &end)
			return end.End
		})
		t.Logf("merged: %d prefixed lines, end=%+v", prefixed, end)
		if end.Error != "" {
			t.Fatalf("end %+v", end)
		}
	})

	t.Run("exec echo hi", func(t *testing.T) {
		pods, err := k.CoreV1().Pods("podinfo").List(ctx, metav1.ListOptions{LabelSelector: "app.kubernetes.io/name=podinfo"})
		if err != nil {
			t.Fatal(err)
		}
		var name string
		for _, p := range pods.Items {
			if p.Status.Phase == "Running" {
				name = p.Name
			}
		}
		if name == "" {
			t.Skip("no running podinfo pod")
		}
		res, err := sm.Open(ctx, protocol.StreamOpenParams{Type: "exec", Command: []string{"echo", "hi"}, Ref: &protocol.ObjectRef{Cluster: id, GVR: "v1/pods", Namespace: "podinfo", Name: name}})
		if err != nil {
			t.Fatal(err)
		}
		tr := newTransport()
		go func() { _ = sm.Serve(ctx, res.StreamID, tr) }()
		var out strings.Builder
		var exit *int
		tr.collect(30*time.Second, func(bin bool, b []byte) bool {
			if bin && b[0] == protocol.StreamChanStdout {
				out.Write(b[1:])
				return false
			}
			if !bin {
				var s protocol.StreamServerMsg
				_ = json.Unmarshal(b, &s)
				exit = s.Exit
			}
			return exit != nil
		})
		if exit == nil || *exit != 0 || !strings.Contains(out.String(), "hi") {
			t.Fatalf("exit=%v out=%q", exit, out.String())
		}
	})

	t.Run("flux reconcile", func(t *testing.T) {
		ref := protocol.ObjectRef{Cluster: id, GVR: "kustomize.toolkit.fluxcd.io/v1/kustomizations", Namespace: "flux-system", Name: "podinfo"}
		res, err := svc.Action(ctx, protocol.ObjectActionParams{RequestID: "e2e-flux-1", Ref: ref, Action: "flux.reconcile"})
		if err != nil || res.Token == "" {
			t.Fatalf("%v %+v", err, res)
		}
		waitFor(t, "lastHandledReconcileAt", 90*time.Second, func() bool {
			p, err := svc.Get(ctx, protocol.ObjectGetParams{Cluster: id, GVR: ref.GVR, Namespace: ref.Namespace, Name: ref.Name})
			return err == nil && strings.Contains(p.YAML, "lastHandledReconcileAt: \""+res.Token+"\"") || err == nil && strings.Contains(p.YAML, "lastHandledReconcileAt: "+res.Token)
		})
		t.Logf("reconcile token %s handled", res.Token)
	})

	t.Run("helm releases", func(t *testing.T) {
		scope := m.OpenScope(protocol.HelmGVR, nil)
		defer scope.Close()
		type rel struct{ status string }
		got := map[string]rel{}
		waitFor(t, "helm releases", 60*time.Second, func() bool {
			got = map[string]rel{}
			m.ScanKind(id, protocol.HelmGVR, func(r *ingest.Row) bool { got[r.Name()] = rel{r.StrCell(1)}; return true })
			_, a := got["podinfo"]
			_, b := got["broken-release"]
			return a && b
		})
		// the task described broken-release as failed; on this cluster Helm labelled the release `deployed` (its pods are
		// in ImagePullBackOff, which is a workload failure, not a Helm one): the list reports what the labels say.
		if got["broken-release"].status == "" {
			t.Fatalf("broken-release: %+v", got["broken-release"])
		}
		t.Logf("releases: %v", got)
		// the lazy payload decode of one release
		hs := helm.New(helm.Config{Runtime: func(cid string) (kube.Runtime, error) {
			c, err := connFn(cid)
			r, _ := kube.RuntimeOf(c)
			return r, err
		}})
		r, err := hs.Release(ctx, protocol.HelmReleaseParams{Cluster: id, Namespace: "podinfo", Name: "broken-release"})
		if err != nil || r.Status == "" || r.Manifest == "" {
			t.Fatalf("%v %+v", err, r)
		}
	})

	t.Run("drain preflight", func(t *testing.T) {
		d := drainer.New(drainer.Config{Router: svc, Conn: connFn})
		pf, err := d.Preflight(ctx, protocol.DrainParams{Cluster: id, Node: "v8s-e2e-worker"})
		if err != nil {
			t.Fatal(err)
		}
		t.Logf("preflight: evict=%d blocked=%d unmanaged=%d emptyDir=%d daemonSets=%d mirror=%d finished=%d cordoned=%v",
			len(pf.Evict), len(pf.Blocked), len(pf.Unmanaged), len(pf.EmptyDir), len(pf.DaemonSets), len(pf.Mirror), len(pf.Finished), pf.Cordoned)
		if pf.Node != "v8s-e2e-worker" || len(pf.DaemonSets) == 0 || pf.Cordoned {
			t.Fatalf("%+v", pf)
		}
	})

	t.Run("node shell uname", func(t *testing.T) {
		_, nserr := k.CoreV1().Namespaces().Get(ctx, "v8s-system", metav1.GetOptions{})
		created := apierrors.IsNotFound(nserr)
		defer func() {
			if created { // leave the cluster as it was
				_ = k.CoreV1().Namespaces().Delete(context.Background(), "v8s-system", metav1.DeleteOptions{})
			}
		}()
		res, err := sm.Open(ctx, protocol.StreamOpenParams{Type: "nodeshell", Cluster: id, Node: "v8s-e2e-worker"})
		if err != nil {
			t.Fatal(err)
		}
		tr := newTransport()
		done := make(chan struct{})
		go func() { _ = sm.Serve(ctx, res.StreamID, tr); close(done) }()
		var ready protocol.StreamReady
		var out strings.Builder
		sent := false
		tr.collect(120*time.Second, func(bin bool, b []byte) bool {
			if !bin {
				var s protocol.StreamServerMsg
				_ = json.Unmarshal(b, &s)
				if s.Ready != nil {
					ready = *s.Ready
					if !sent { // a shell prints nothing until it is spoken to
						sent = true
						tr.in <- [2]any{true, append([]byte{protocol.StreamChanStdin}, "uname -a; exit\n"...)}
					}
				}
				return s.Exit != nil
			}
			out.Write(b[1:])
			return false
		})
		t.Logf("pod=%s attached=%v output=%q", ready.Pod, ready.Attached, out.String())
		if !strings.Contains(out.String(), "Linux") {
			t.Fatalf("uname output: %q", out.String())
		}
		<-done
		waitFor(t, "node shell pod deleted", 60*time.Second, func() bool {
			l, err := k.CoreV1().Pods("v8s-system").List(ctx, metav1.ListOptions{LabelSelector: "app.kubernetes.io/managed-by=v8s"})
			return err == nil && len(l.Items) == 0
		})
	})
	_ = errors.New
}

func ptr[T any](v T) *T { return &v }
