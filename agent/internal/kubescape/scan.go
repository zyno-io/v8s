package kubescape

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/zyno-io/v8s/agent/internal/ops"
	"github.com/zyno-io/v8s/agent/internal/rpc"
	"github.com/zyno-io/v8s/agent/internal/security"
	"github.com/zyno-io/v8s/agent/pkg/protocol"
	batchv1 "k8s.io/api/batch/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

var (
	cronJobs = schema.GroupVersionResource{Group: "batch", Version: "v1", Resource: "cronjobs"}
	jobs     = schema.GroupVersionResource{Group: "batch", Version: "v1", Resource: "jobs"}
)

// isScanCron recognizes the operator's scheduled-scan CronJobs (kubescape-scheduler, kubevuln-scheduler).
func isScanCron(ns, name string) bool {
	return (ns == "kubescape" || strings.Contains(ns, "kubescape")) && strings.HasSuffix(name, "-scheduler") &&
		(strings.HasPrefix(name, "kubescape") || strings.HasPrefix(name, "kubevuln"))
}

func registerScan() {
	security.RegisterHandler("kubescape.scanNow", func(cfg *security.Config) rpc.Handler {
		return func(ctx context.Context, decode func(any) error) (any, error) {
			var p protocol.KubescapeScanNowParams
			if err := decode(&p); err != nil {
				return nil, ops.NewError("bad_request", "params: %s", err.Error())
			}
			return ScanNow(ctx, cfg, p)
		}
	})
}

// ScanNow starts a scan by creating a Job from each of the operator's scan CronJobs (what `kubectl create job
// --from=cronjob/...` does). Creating OperatorCommand objects directly is deferred until that API is stable.
func ScanNow(ctx context.Context, cfg *security.Config, p protocol.KubescapeScanNowParams) (*protocol.KubescapeScanNowResult, error) {
	if p.Cluster == "" {
		return nil, ops.NewError("bad_request", "cluster is required")
	}
	ctx = ops.WithUseWrite(ctx, p.UseWriteContext)
	routed, err := cfg.Route(ctx, p.Cluster, "create", "batch/v1/jobs", "", "", "")
	if err != nil {
		return nil, err
	}
	list, err := routed.Conn.Dynamic().Resource(cronJobs).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, ops.MapError(err, routed.ViaWrite)
	}
	res := &protocol.KubescapeScanNowResult{UsedContext: routed.ContextID, Jobs: []string{}}
	stamp := time.Now().Unix() % 1000000
	for i := range list.Items {
		cj := &list.Items[i]
		if !isScanCron(cj.GetNamespace(), cj.GetName()) {
			continue
		}
		var typed batchv1.CronJob
		if err := runtime.DefaultUnstructuredConverter.FromUnstructured(cj.Object, &typed); err != nil {
			return nil, ops.NewError("internal", "decode cronjob %s: %s", cj.GetName(), err.Error())
		}
		job := &batchv1.Job{
			ObjectMeta: metav1.ObjectMeta{
				Name: fmt.Sprintf("%s-manual-%d", cj.GetName(), stamp), Namespace: cj.GetNamespace(),
				Labels:          typed.Spec.JobTemplate.Labels,
				Annotations:     map[string]string{"cronjob.kubernetes.io/instantiate": "manual"},
				OwnerReferences: []metav1.OwnerReference{{APIVersion: "batch/v1", Kind: "CronJob", Name: cj.GetName(), UID: cj.GetUID()}},
			},
			Spec: typed.Spec.JobTemplate.Spec,
		}
		job.Name = trimName(job.Name)
		obj, err := runtime.DefaultUnstructuredConverter.ToUnstructured(job)
		if err != nil {
			return nil, ops.NewError("internal", "%s", err.Error())
		}
		obj["apiVersion"], obj["kind"] = "batch/v1", "Job"
		if _, err := routed.Conn.Dynamic().Resource(jobs).Namespace(cj.GetNamespace()).Create(ctx, &unstructured.Unstructured{Object: obj}, metav1.CreateOptions{FieldManager: "v8s"}); err != nil {
			return nil, ops.MapError(err, routed.ViaWrite)
		}
		res.Jobs = append(res.Jobs, cj.GetNamespace()+"/"+job.Name)
	}
	if len(res.Jobs) == 0 {
		return nil, ops.NewError("not_found", "no Kubescape scan CronJob found in %s (the operator's kubescape-scheduler / kubevuln-scheduler)", p.Cluster)
	}
	return res, nil
}

func trimName(n string) string {
	if len(n) > 63 {
		return strings.TrimRight(n[len(n)-63:], "-.")
	}
	return n
}
