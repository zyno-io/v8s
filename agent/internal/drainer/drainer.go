// Package drainer implements node drain: the pre-flight classification of a node's pods and the drain job,
// which embeds kubectl's own drain logic (k8s.io/kubectl/pkg/drain: cordon, Eviction API with PDB-aware
// retries, DaemonSet and mirror pod filters, emptyDir and unmanaged-pod guards).
package drainer

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/zyno-io/v8s/agent/internal/jobs"
	"github.com/zyno-io/v8s/agent/internal/kube"
	"github.com/zyno-io/v8s/agent/internal/ops"
	"github.com/zyno-io/v8s/agent/pkg/protocol"
	corev1 "k8s.io/api/core/v1"
	policyv1 "k8s.io/api/policy/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/client-go/kubernetes"
	kdrain "k8s.io/kubectl/pkg/drain"
)

// Router routes the node mutation (ops.Service).
type Router interface {
	Route(ctx context.Context, cluster, verb, gvr, sub, ns, name string) (*ops.Routed, error)
}

// Config wires the service.
type Config struct {
	Router Router
	// Conn returns the connection of a context id (read context id == cluster id).
	Conn func(contextID string) (kube.Conn, error)
	// EvictRetryDelay is how long a drain waits before retrying an eviction a PDB refused (default 5 s).
	EvictRetryDelay time.Duration
}

// Service runs pre-flights and drains.
type Service struct{ cfg Config }

func New(cfg Config) *Service {
	if cfg.EvictRetryDelay == 0 {
		cfg.EvictRetryDelay = 5 * time.Second
	}
	return &Service{cfg: cfg}
}

func ignoreDS(o protocol.DrainOptions) bool { return o.IgnoreDaemonSets == nil || *o.IgnoreDaemonSets }

func controllerOf(p *corev1.Pod) *metav1.OwnerReference { return metav1.GetControllerOf(p) }

func ownerString(p *corev1.Pod) string {
	if c := controllerOf(p); c != nil {
		return c.Kind + "/" + c.Name
	}
	return ""
}

func hasEmptyDir(p *corev1.Pod) bool {
	for _, v := range p.Spec.Volumes {
		if v.EmptyDir != nil {
			return true
		}
	}
	return false
}

// Classify sorts the pods of a node into what a drain with opts would do with them (pure; unit-tested).
func Classify(pods []corev1.Pod, pdbs []policyv1.PodDisruptionBudget, opts protocol.DrainOptions) protocol.DrainPreflightResult {
	res := protocol.DrainPreflightResult{Evict: []protocol.DrainPod{}, Blocked: []protocol.DrainPod{}, Unmanaged: []protocol.DrainPod{},
		EmptyDir: []protocol.DrainPod{}, DaemonSets: []protocol.DrainPod{}, Mirror: []protocol.DrainPod{}, Finished: []protocol.DrainPod{}}
	var sel labels.Selector
	if opts.PodSelector != "" {
		sel, _ = labels.Parse(opts.PodSelector)
	}
	for i := range pods {
		p := &pods[i]
		if sel != nil && !sel.Matches(labels.Set(p.Labels)) {
			continue
		}
		dp := protocol.DrainPod{Namespace: p.Namespace, Name: p.Name, Owner: ownerString(p), EmptyDir: hasEmptyDir(p)}
		c := controllerOf(p)
		switch {
		case p.Annotations[corev1.MirrorPodAnnotationKey] != "":
			res.Mirror = append(res.Mirror, dp)
			continue
		case c != nil && c.Kind == "DaemonSet" && ignoreDS(opts):
			res.DaemonSets = append(res.DaemonSets, dp)
			continue
		case p.Status.Phase == corev1.PodSucceeded || p.Status.Phase == corev1.PodFailed:
			res.Finished = append(res.Finished, dp)
			continue
		}
		if dp.EmptyDir {
			res.EmptyDir = append(res.EmptyDir, dp)
		}
		if c == nil {
			res.Unmanaged = append(res.Unmanaged, dp)
			continue
		}
		if c.Kind == "DaemonSet" { // only reached with ignoreDaemonSets off: kubectl refuses the drain
			res.Blocked = append(res.Blocked, dp)
			continue
		}
		if pdb := blockingPDB(p, pdbs); pdb != nil && !opts.DisableEviction {
			dp.PDB = pdb
			res.Blocked = append(res.Blocked, dp)
			continue
		}
		res.Evict = append(res.Evict, dp)
	}
	for _, l := range [][]protocol.DrainPod{res.Evict, res.Blocked, res.Unmanaged, res.EmptyDir, res.DaemonSets, res.Mirror, res.Finished} {
		sort.Slice(l, func(i, j int) bool {
			if l[i].Namespace != l[j].Namespace {
				return l[i].Namespace < l[j].Namespace
			}
			return l[i].Name < l[j].Name
		})
	}
	return res
}

// blockingPDB returns a PDB covering p that allows no disruption right now. A nil selector matches no pod; an
// empty one matches every pod in the namespace.
func blockingPDB(p *corev1.Pod, pdbs []policyv1.PodDisruptionBudget) *protocol.DrainPDB {
	for i := range pdbs {
		b := &pdbs[i]
		if b.Namespace != p.Namespace || b.Spec.Selector == nil {
			continue
		}
		s, err := metav1.LabelSelectorAsSelector(b.Spec.Selector)
		if err != nil || !s.Matches(labels.Set(p.Labels)) {
			continue
		}
		if b.Status.DisruptionsAllowed <= 0 {
			return &protocol.DrainPDB{Name: b.Name, DisruptionsAllowed: int(b.Status.DisruptionsAllowed)}
		}
	}
	return nil
}

func (s *Service) readClientset(cluster string) (kubernetes.Interface, error) {
	c, err := s.cfg.Conn(cluster)
	if err != nil {
		return nil, ops.NewError("internal", "context %q unavailable: %s", cluster, err.Error())
	}
	rt, ok := kube.RuntimeOf(c)
	if !ok {
		return nil, ops.NewError("unavailable", "context %q does not support drain", cluster)
	}
	return rt.Clientset(), nil
}

func validate(p protocol.DrainParams) error {
	if p.Cluster == "" || p.Node == "" {
		return ops.NewError("bad_request", "drain needs cluster and node")
	}
	if p.Options.PodSelector != "" {
		if _, err := labels.Parse(p.Options.PodSelector); err != nil {
			return ops.NewError("bad_request", "bad podSelector: %s", err.Error())
		}
	}
	return nil
}

// Preflight reports what a drain would do, without changing anything (read context).
func (s *Service) Preflight(ctx context.Context, p protocol.DrainPreflightParams) (protocol.DrainPreflightResult, error) {
	if err := validate(p); err != nil {
		return protocol.DrainPreflightResult{}, err
	}
	cs, err := s.readClientset(p.Cluster)
	if err != nil {
		return protocol.DrainPreflightResult{}, err
	}
	node, err := cs.CoreV1().Nodes().Get(ctx, p.Node, metav1.GetOptions{})
	if err != nil {
		return protocol.DrainPreflightResult{}, ops.MapError(err, false)
	}
	pods, err := cs.CoreV1().Pods(metav1.NamespaceAll).List(ctx, metav1.ListOptions{FieldSelector: fields.OneTermEqualSelector("spec.nodeName", p.Node).String()})
	if err != nil {
		return protocol.DrainPreflightResult{}, ops.MapError(err, false)
	}
	pdbs, err := cs.PolicyV1().PodDisruptionBudgets(metav1.NamespaceAll).List(ctx, metav1.ListOptions{})
	if err != nil {
		return protocol.DrainPreflightResult{}, ops.MapError(err, false)
	}
	res := Classify(pods.Items, pdbs.Items, p.Options)
	res.Node, res.Cordoned, res.UsedContext = p.Node, node.Spec.Unschedulable, p.Cluster
	return res, nil
}

// Prepared describes a validated, routed drain.
type Prepared struct{ Cluster, Subject, UsedContext string }

// Prepare validates and routes the drain (patch nodes, evict pods through the write context when the read context
// is denied) and returns the job body.
func (s *Service) Prepare(ctx context.Context, p protocol.DrainParams) (jobs.Run, *Prepared, error) {
	if err := validate(p); err != nil {
		return nil, nil, err
	}
	routed, err := s.cfg.Router.Route(ctx, p.Cluster, "patch", "v1/nodes", "", "", p.Node)
	if err != nil {
		return nil, nil, err
	}
	rt, ok := kube.RuntimeOf(routed.Conn)
	if !ok {
		return nil, nil, ops.NewError("unavailable", "context %q does not support drain", routed.ContextID)
	}
	prep := &Prepared{Cluster: p.Cluster, Subject: p.Node, UsedContext: routed.ContextID}
	return func(ctx context.Context, jc *jobs.Ctx) error {
		jc.SetContext(routed.ContextID)
		return s.run(ctx, jc, rt.Clientset(), p, routed)
	}, prep, nil
}

// progressWriter turns kubectl drain's messages into job progress: the eviction retries a PDB forces.
type progressWriter struct{ jc *jobs.Ctx }

var retryRe = regexp.MustCompile(`error when evicting pods/"([^"]+)" -n "([^"]+)" \(will retry after [^)]*\): (.*)`)

func (w progressWriter) Write(b []byte) (int, error) {
	sc := bufio.NewScanner(strings.NewReader(string(b)))
	for sc.Scan() {
		if m := retryRe.FindStringSubmatch(sc.Text()); m != nil {
			msg := m[3]
			if strings.Contains(msg, "disruption budget") {
				msg = "waiting on a PodDisruptionBudget: " + msg
			}
			w.jc.Progress(msg, m[2]+"/"+m[1], "waiting-pdb")
		}
	}
	return len(b), nil
}

func (s *Service) run(ctx context.Context, jc *jobs.Ctx, cs kubernetes.Interface, p protocol.DrainParams, routed *ops.Routed) error {
	o := p.Options
	timeout := time.Duration(o.TimeoutSeconds) * time.Second
	if timeout == 0 {
		timeout = 5 * time.Minute
	}
	grace := -1
	if o.GracePeriodSeconds != nil {
		grace = *o.GracePeriodSeconds
	}
	h := &kdrain.Helper{
		Ctx: ctx, Client: cs, Force: o.Force, IgnoreAllDaemonSets: ignoreDS(o), DeleteEmptyDirData: o.DeleteEmptyDirData,
		GracePeriodSeconds: grace, Timeout: timeout, DisableEviction: o.DisableEviction, PodSelector: o.PodSelector,
		EvictErrorRetryDelay: s.cfg.EvictRetryDelay,
		Out:                  io.Discard, ErrOut: progressWriter{jc},
		OnPodDeletionOrEvictionStarted: func(pod *corev1.Pod, ev bool) {
			verb := "evicting"
			if !ev {
				verb = "deleting"
			}
			jc.Progress(verb+" pod", pod.Namespace+"/"+pod.Name, "evicting")
		},
		OnPodDeletionOrEvictionFinished: func(pod *corev1.Pod, ev bool, err error) {
			if err != nil {
				jc.Progress(err.Error(), pod.Namespace+"/"+pod.Name, "error")
				return
			}
			jc.Progress("pod terminated", pod.Namespace+"/"+pod.Name, "evicted")
		},
	}
	node, err := cs.CoreV1().Nodes().Get(ctx, p.Node, metav1.GetOptions{})
	if err != nil {
		return failWith(s.mapRouted(ctx, err, routed, "get", p.Node))
	}
	if node.Spec.Unschedulable {
		jc.Progress("node is already cordoned", p.Node, "cordoned")
	} else {
		jc.Progress("cordoning node", p.Node, "cordoning")
		if err := kdrain.RunCordonOrUncordon(h, node, true); err != nil {
			return failWith(s.mapRouted(ctx, err, routed, "patch", p.Node))
		}
		jc.Progress("node cordoned", p.Node, "cordoned")
	}
	list, errs := h.GetPodsForDeletion(p.Node)
	if len(errs) > 0 {
		msgs := make([]string, 0, len(errs))
		for _, e := range errs {
			msgs = append(msgs, e.Error())
		}
		return &jobs.Fail{Body: protocol.ErrorBody{Code: "invalid", Message: "the node cannot be drained with these options: " + strings.Join(msgs, "; "),
			Hint: "pods without a controller need force, pods with emptyDir data need deleteEmptyDirData"}}
	}
	if w := list.Warnings(); w != "" {
		jc.Progress(w, "", "warning")
	}
	pods := list.Pods()
	jc.Progress(fmt.Sprintf("%d pod(s) to evict", len(pods)), p.Node, "planned")
	if err := h.DeleteOrEvictPods(pods); err != nil {
		if ctx.Err() != nil {
			return ctx.Err() // cancelled: the node stays cordoned
		}
		return &jobs.Fail{Body: protocol.ErrorBody{Code: "unavailable", Message: err.Error(), Hint: "the node stays cordoned; re-run the drain to resume"}}
	}
	jc.Progress("drain complete", p.Node, "done")
	return nil
}

// mapRouted maps an error of a call through the routed context: a 403 through the write context that the
// write context itself would deny is write_unavailable (the Router, ops.Service, does the classification).
func (s *Service) mapRouted(ctx context.Context, err error, r *ops.Routed, verb, node string) error {
	if mm, ok := s.cfg.Router.(interface {
		MapRouted(ctx context.Context, err error, r *ops.Routed, verb, gvr, sub, ns, name string) error
	}); ok {
		return mm.MapRouted(ctx, err, r, verb, "v1/nodes", "", "", node)
	}
	return ops.MapError(err, r.ViaWrite)
}

func failWith(err error) error {
	var oe *ops.Error
	if errors.As(err, &oe) {
		return &jobs.Fail{Body: oe.Body}
	}
	return err
}
