package helm

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/zyno-io/v8s/agent/internal/jobs"
	"github.com/zyno-io/v8s/agent/pkg/protocol"
	"helm.sh/helm/v4/pkg/action"
	"helm.sh/helm/v4/pkg/kube"
	"helm.sh/helm/v4/pkg/release/common"
	releasev1 "helm.sh/helm/v4/pkg/release/v1"
	"helm.sh/helm/v4/pkg/storage/driver"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/discovery/cached/memory"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/restmapper"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
)

// Prepared describes an accepted action for the job record.
type Prepared struct{ Cluster, Subject, UsedContext string }

const defaultTimeout = 300 * time.Second

func timeoutOf(sec int) time.Duration {
	if sec <= 0 {
		return defaultTimeout
	}
	return time.Duration(sec) * time.Second
}

// budgetTimeout is timeoutOf clamped to what is left of the job's budget (the context deadline), so a Helm call
// that cannot be cancelled still ends before the job is due.
func budgetTimeout(ctx context.Context, sec int) time.Duration {
	d := timeoutOf(sec)
	if dl, ok := ctx.Deadline(); ok {
		if rem := time.Until(dl); rem < d {
			d = max(rem, time.Second)
		}
	}
	return d
}

func waitStrategy(wait bool) kube.WaitStrategy {
	if wait {
		return kube.StatusWatcherStrategy
	}
	return kube.HookOnlyStrategy
}

// guard validates synchronously: the release exists, the Flux guard passes, and the write context
// resolves. It returns the latest revision and the context id.
func (s *Service) guard(ctx context.Context, cluster, ns, name string, force bool) (revision, []revision, string, error) {
	if err := s.validate(cluster, ns, name); err != nil {
		return revision{}, nil, "", err
	}
	rt, err := s.cfg.Runtime(cluster)
	if err != nil {
		return revision{}, nil, "", err
	}
	revs, err := s.revisions(ctx, rt, ns, name)
	if err != nil {
		return revision{}, nil, "", err
	}
	if len(revs) == 0 {
		return revision{}, nil, "", actionErr("not_found", "release %s/%s not found", ns, name)
	}
	last := revs[len(revs)-1]
	if last.fluxName != "" && !force {
		return revision{}, nil, "", &ActionError{Body: protocol.ErrorBody{
			Code:    "gitops_managed",
			Message: fmt.Sprintf("release is managed by Flux HelmRelease %s/%s", last.fluxNS, last.fluxName),
			Hint:    "Use the Flux actions (reconcile, force, reset) on the HelmRelease; pass force=true to run the Helm action anyway (Flux may revert or reinstall)",
		}}
	}
	ctxID, _, err := s.cfg.Route(ctx, cluster, "update", "v1/secrets", "", ns, "")
	if err != nil {
		return revision{}, nil, "", err
	}
	return last, revs, ctxID, nil
}

// PrepareRollback validates a rollback and returns the job body.
func (s *Service) PrepareRollback(ctx context.Context, p protocol.HelmRollbackParams) (jobs.Run, *Prepared, error) {
	last, revs, ctxID, err := s.guard(ctx, p.Cluster, p.Namespace, p.Name, p.Force)
	if err != nil {
		return nil, nil, err
	}
	target := p.Revision
	if target == 0 {
		target = last.version - 1
	}
	ok := false
	for _, r := range revs {
		ok = ok || r.version == target
	}
	if !ok {
		return nil, nil, actionErr("bad_request", "release %s/%s has no revision %d", p.Namespace, p.Name, target)
	}
	subject := p.Namespace + "/" + p.Name
	run := func(ctx context.Context, jc *jobs.Ctx) error {
		jc.SetContext(ctxID)
		jc.Progress(fmt.Sprintf("Rolling back to revision %d", target), subject, "running")
		cfg, err := s.newConfig(ctx, ctxID, p.Namespace)
		if err != nil {
			return s.fail(jc, subject, err)
		}
		a := action.NewRollback(cfg)
		a.Version = p.Revision
		a.Timeout = budgetTimeout(ctx, p.TimeoutSeconds)
		a.WaitStrategy = waitStrategy(p.Wait)
		a.CleanupOnFail = p.CleanupOnFail
		a.ForceReplace = p.ForceReplace
		if err := runUncancellable(ctx, jc, subject, func() error { return a.Run(p.Name) }); err != nil {
			return s.fail(jc, subject, err)
		}
		jc.Progress(fmt.Sprintf("Rolled back to revision %d", target), subject, "done")
		return nil
	}
	return run, &Prepared{Cluster: p.Cluster, Subject: subject, UsedContext: ctxID}, nil
}

// PrepareUninstall validates an uninstall and returns the job body.
func (s *Service) PrepareUninstall(ctx context.Context, p protocol.HelmUninstallParams) (jobs.Run, *Prepared, error) {
	_, _, ctxID, err := s.guard(ctx, p.Cluster, p.Namespace, p.Name, p.Force)
	if err != nil {
		return nil, nil, err
	}
	switch p.Cascade {
	case "", "background", "foreground", "orphan":
	default:
		return nil, nil, actionErr("bad_request", "cascade must be background, foreground or orphan")
	}
	subject := p.Namespace + "/" + p.Name
	run := func(ctx context.Context, jc *jobs.Ctx) error {
		jc.SetContext(ctxID)
		jc.Progress("Uninstalling release", subject, "running")
		cfg, err := s.newConfig(ctx, ctxID, p.Namespace)
		if err != nil {
			return s.fail(jc, subject, err)
		}
		a := action.NewUninstall(cfg)
		a.KeepHistory = p.KeepHistory
		a.Timeout = budgetTimeout(ctx, p.TimeoutSeconds)
		a.WaitStrategy = waitStrategy(p.Wait)
		a.DeletionPropagation = p.Cascade
		if a.DeletionPropagation == "" {
			a.DeletionPropagation = "background"
		}
		if err := runUncancellable(ctx, jc, subject, func() error { _, err := a.Run(p.Name); return err }); err != nil {
			return s.fail(jc, subject, err)
		}
		jc.Progress("Uninstalled release", subject, "done")
		return nil
	}
	return run, &Prepared{Cluster: p.Cluster, Subject: subject, UsedContext: ctxID}, nil
}

// PrepareRetry validates a retry and returns the job body: re-run the last upgrade with the stored chart
// and user values (no chart repository access).
func (s *Service) PrepareRetry(ctx context.Context, p protocol.HelmRetryParams) (jobs.Run, *Prepared, error) {
	_, _, ctxID, err := s.guard(ctx, p.Cluster, p.Namespace, p.Name, p.Force)
	if err != nil {
		return nil, nil, err
	}
	subject := p.Namespace + "/" + p.Name
	run := func(ctx context.Context, jc *jobs.Ctx) error {
		jc.SetContext(ctxID)
		jc.Progress("Retrying release", subject, "running")
		cfg, err := s.newConfig(ctx, ctxID, p.Namespace)
		if err != nil {
			return s.fail(jc, subject, err)
		}
		li, err := cfg.Releases.Last(p.Name)
		if err != nil {
			return s.fail(jc, subject, err)
		}
		rel, ok := li.(*releasev1.Release)
		if !ok || rel.Info == nil || rel.Chart == nil {
			return s.fail(jc, subject, errors.New("stored release has no chart"))
		}
		if isPending(rel.Info.Status) {
			if !p.MarkFailed {
				f := &jobs.Fail{Body: protocol.ErrorBody{Code: "conflict",
					Message: fmt.Sprintf("revision %d is stuck in %s; set markFailed to mark it failed before retrying", rel.Version, rel.Info.Status)}}
				jc.Progress(f.Body.Message, subject, "error")
				return f
			}
			jc.Progress(fmt.Sprintf("Marking revision %d failed", rel.Version), subject, "marking-failed")
			rel.SetStatus(common.StatusFailed, fmt.Sprintf("marked failed by v8s (was %s)", rel.Info.Status))
			if err := cfg.Releases.Update(rel); err != nil {
				return s.fail(jc, subject, err)
			}
		}
		jc.Progress("Upgrading with the stored chart and values", subject, "upgrading")
		up := action.NewUpgrade(cfg)
		up.Namespace = p.Namespace
		up.Timeout = budgetTimeout(ctx, p.TimeoutSeconds)
		up.WaitStrategy = waitStrategy(p.Wait)
		up.ReuseValues = false
		if _, err := up.RunWithContext(ctx, p.Name, rel.Chart, rel.Config); err != nil {
			return s.fail(jc, subject, err)
		}
		jc.Progress("Retried release", subject, "done")
		return nil
	}
	return run, &Prepared{Cluster: p.Cluster, Subject: subject, UsedContext: ctxID}, nil
}

func isPending(st common.Status) bool {
	return st == common.StatusPendingInstall || st == common.StatusPendingUpgrade || st == common.StatusPendingRollback
}

// runUncancellable runs a Helm call that takes no context (Helm v4 rollback / uninstall). Abandoning it would
// leave a release operation running behind a finished job, so on cancellation the job stays running (progress
// state "cancelling") until the call returns; its own timeout (clamped to the job budget) bounds the wait.
func runUncancellable(ctx context.Context, jc *jobs.Ctx, subject string, fn func() error) error {
	ch := make(chan error, 1)
	go func() { ch <- fn() }()
	select {
	case err := <-ch:
		return err
	case <-ctx.Done():
	}
	jc.Progress("Cancelling: waiting for the Helm operation to finish", subject, "cancelling")
	if err := <-ch; err != nil {
		return context.Cause(ctx)
	}
	return nil
}

// fail reports a failed step and maps the error to a job failure (cancellation passes through).
func (s *Service) fail(jc *jobs.Ctx, subject string, err error) error {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	f := MapError(err)
	jc.Progress(f.Body.Message, subject, "error")
	return f
}

// MapError maps a Helm or API error to a short job failure.
func MapError(err error) *jobs.Fail {
	var f *jobs.Fail
	if errors.As(err, &f) {
		return f
	}
	code := "internal"
	switch {
	case errors.Is(err, driver.ErrReleaseNotFound), apierrors.IsNotFound(err):
		code = "not_found"
	case apierrors.IsForbidden(err), apierrors.IsUnauthorized(err):
		code = "forbidden"
	case apierrors.IsConflict(err), strings.Contains(err.Error(), "another operation"):
		code = "conflict"
	case apierrors.IsInvalid(err):
		code = "invalid"
	}
	msg := strings.SplitN(err.Error(), "\n", 2)[0]
	if len(msg) > 300 {
		msg = msg[:300] + "..."
	}
	return &jobs.Fail{Body: protocol.ErrorBody{Code: code, Message: msg}}
}

// ---- action configuration ---------------------------------------------------------------------

func (s *Service) newConfig(ctx context.Context, contextID, ns string) (*action.Configuration, error) {
	if s.cfg.NewActionConfig != nil {
		return s.cfg.NewActionConfig(ctx, contextID, ns)
	}
	rt, err := s.cfg.Runtime(contextID)
	if err != nil {
		return nil, err
	}
	rc := rt.RESTConfig()
	if rc == nil {
		return nil, errors.New("context has no rest config")
	}
	cfg := action.NewConfiguration(action.ConfigurationSetLogger(s.cfg.Log.Handler()))
	if err := cfg.Init(&restGetter{rc: rest.CopyConfig(rc), ns: ns}, ns, "secrets"); err != nil {
		return nil, err
	}
	return cfg, nil
}

// restGetter is a genericclioptions.RESTClientGetter over an in-memory rest.Config, so Helm never reads a
// kubeconfig: it acts through exactly the identity the agent resolved.
type restGetter struct {
	rc *rest.Config
	ns string
}

func (g *restGetter) ToRESTConfig() (*rest.Config, error) { return rest.CopyConfig(g.rc), nil }

func (g *restGetter) ToDiscoveryClient() (discovery.CachedDiscoveryInterface, error) {
	d, err := discovery.NewDiscoveryClientForConfig(rest.CopyConfig(g.rc))
	if err != nil {
		return nil, err
	}
	return memory.NewMemCacheClient(d), nil
}

func (g *restGetter) ToRESTMapper() (meta.RESTMapper, error) {
	d, err := g.ToDiscoveryClient()
	if err != nil {
		return nil, err
	}
	return restmapper.NewShortcutExpander(restmapper.NewDeferredDiscoveryRESTMapper(d), d, nil), nil
}

func (g *restGetter) ToRawKubeConfigLoader() clientcmd.ClientConfig {
	return clientcmd.NewDefaultClientConfig(*clientcmdapi.NewConfig(), &clientcmd.ConfigOverrides{Context: clientcmdapi.Context{Namespace: g.ns}})
}
