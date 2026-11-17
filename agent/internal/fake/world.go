package fake

// The fake "world" holds every non-pod object of a fake cluster in memory as unstructured maps,
// consistent with the pod generator: one Deployment and one current ReplicaSet per app (pods are
// owned by that ReplicaSet), plus StatefulSets, DaemonSets, Services, Ingresses, Nodes, Namespaces,
// Events for failing pods, Jobs/CronJobs, ConfigMaps, Secrets and two Flux-like CRDs. It serves
// list/watch to the ingest path (typed for curated kinds, Table for the rest) and the dynamic
// client to the objects API, so edits and actions made in fake mode show up in the views.

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/zyno-io/v8s/agent/internal/disco"
	"github.com/zyno-io/v8s/agent/internal/flux"
	"github.com/zyno-io/v8s/agent/internal/ingest"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/watch"
)

const (
	apps       = 400 // distinct apps (deployments) per cluster, as in the pod generator
	namespaces = 37
	nodes      = 150
)

// kindDef describes one fake kind.
type kindDef struct {
	gvr        string
	apiVersion string
	kind       string
	resource   string
	namespaced bool
	short      []string
	// Table mode (nil cols: the kind is served typed through its curated module).
	cols  []ingest.TableCol
	cells func(o map[string]any) []any
}

func (d *kindDef) group() string {
	g, _, _ := strings.Cut(d.apiVersion, "/")
	if !strings.Contains(d.apiVersion, "/") {
		return ""
	}
	return g
}

func (d *kindDef) version() string {
	if _, v, ok := strings.Cut(d.apiVersion, "/"); ok {
		return v
	}
	return d.apiVersion
}

func tc(name, typ string) ingest.TableCol { return ingest.TableCol{Name: name, Type: typ} }

var kindDefs = []*kindDef{
	{gvr: "v1/pods", apiVersion: "v1", kind: "Pod", resource: "pods", namespaced: true, short: []string{"po"}},
	{gvr: "v1/nodes", apiVersion: "v1", kind: "Node", resource: "nodes", short: []string{"no"}},
	{gvr: "v1/namespaces", apiVersion: "v1", kind: "Namespace", resource: "namespaces", short: []string{"ns"}},
	{gvr: "v1/services", apiVersion: "v1", kind: "Service", resource: "services", namespaced: true, short: []string{"svc"}},
	{gvr: "v1/events", apiVersion: "v1", kind: "Event", resource: "events", namespaced: true, short: []string{"ev"}},
	{gvr: "v1/persistentvolumeclaims", apiVersion: "v1", kind: "PersistentVolumeClaim", resource: "persistentvolumeclaims", namespaced: true, short: []string{"pvc"}},
	{gvr: "v1/persistentvolumes", apiVersion: "v1", kind: "PersistentVolume", resource: "persistentvolumes", short: []string{"pv"}},
	{gvr: "v1/serviceaccounts", apiVersion: "v1", kind: "ServiceAccount", resource: "serviceaccounts", namespaced: true, short: []string{"sa"}},
	{gvr: "apps/v1/deployments", apiVersion: "apps/v1", kind: "Deployment", resource: "deployments", namespaced: true, short: []string{"deploy"}},
	{gvr: "apps/v1/replicasets", apiVersion: "apps/v1", kind: "ReplicaSet", resource: "replicasets", namespaced: true, short: []string{"rs"}},
	{gvr: "apps/v1/statefulsets", apiVersion: "apps/v1", kind: "StatefulSet", resource: "statefulsets", namespaced: true, short: []string{"sts"}},
	{gvr: "apps/v1/daemonsets", apiVersion: "apps/v1", kind: "DaemonSet", resource: "daemonsets", namespaced: true, short: []string{"ds"}},
	{gvr: "batch/v1/jobs", apiVersion: "batch/v1", kind: "Job", resource: "jobs", namespaced: true},
	{gvr: "batch/v1/cronjobs", apiVersion: "batch/v1", kind: "CronJob", resource: "cronjobs", namespaced: true, short: []string{"cj"}},
	{gvr: "networking.k8s.io/v1/ingresses", apiVersion: "networking.k8s.io/v1", kind: "Ingress", resource: "ingresses", namespaced: true, short: []string{"ing"}},
	{gvr: "networking.k8s.io/v1/networkpolicies", apiVersion: "networking.k8s.io/v1", kind: "NetworkPolicy", resource: "networkpolicies", namespaced: true, short: []string{"netpol"}},
	{gvr: "discovery.k8s.io/v1/endpointslices", apiVersion: "discovery.k8s.io/v1", kind: "EndpointSlice", resource: "endpointslices", namespaced: true},
	{gvr: "autoscaling/v2/horizontalpodautoscalers", apiVersion: "autoscaling/v2", kind: "HorizontalPodAutoscaler", resource: "horizontalpodautoscalers", namespaced: true, short: []string{"hpa"}},
	{gvr: "policy/v1/poddisruptionbudgets", apiVersion: "policy/v1", kind: "PodDisruptionBudget", resource: "poddisruptionbudgets", namespaced: true, short: []string{"pdb"}},
	// Table mode
	{gvr: "v1/configmaps", apiVersion: "v1", kind: "ConfigMap", resource: "configmaps", namespaced: true, short: []string{"cm"},
		cols:  []ingest.TableCol{tc("Name", "string"), tc("Data", "integer"), tc("Age", "string")},
		cells: func(o map[string]any) []any { return []any{nameOf(o), float64(len(mapOf(o, "data"))), "1d"} }},
	{gvr: "v1/secrets", apiVersion: "v1", kind: "Secret", resource: "secrets", namespaced: true,
		cols: []ingest.TableCol{tc("Name", "string"), tc("Type", "string"), tc("Data", "integer"), tc("Age", "string")},
		cells: func(o map[string]any) []any {
			t, _ := o["type"].(string)
			return []any{nameOf(o), t, float64(len(mapOf(o, "data"))), "1d"}
		}},
	{gvr: "storage.k8s.io/v1/storageclasses", apiVersion: "storage.k8s.io/v1", kind: "StorageClass", resource: "storageclasses", short: []string{"sc"},
		cols: []ingest.TableCol{tc("Name", "string"), tc("Provisioner", "string"), tc("ReclaimPolicy", "string"), tc("VolumeBindingMode", "string"), tc("Age", "string")},
		cells: func(o map[string]any) []any {
			p, _ := o["provisioner"].(string)
			return []any{nameOf(o), p, "Delete", "WaitForFirstConsumer", "30d"}
		}},
	{gvr: "apiextensions.k8s.io/v1/customresourcedefinitions", apiVersion: "apiextensions.k8s.io/v1", kind: "CustomResourceDefinition", resource: "customresourcedefinitions", short: []string{"crd"},
		cols:  []ingest.TableCol{tc("Name", "string"), tc("Created At", "date")},
		cells: func(o map[string]any) []any { return []any{nameOf(o), "30d"} }},
	// Flux kinds are read in mode B (typed partial JSON) through their curated modules
	{gvr: "source.toolkit.fluxcd.io/v1/gitrepositories", apiVersion: "source.toolkit.fluxcd.io/v1", kind: "GitRepository", resource: "gitrepositories", namespaced: true, short: []string{"gitrepo"}},
	{gvr: "source.toolkit.fluxcd.io/v1/ocirepositories", apiVersion: "source.toolkit.fluxcd.io/v1", kind: "OCIRepository", resource: "ocirepositories", namespaced: true, short: []string{"ocirepo"}},
	{gvr: "source.toolkit.fluxcd.io/v1/helmrepositories", apiVersion: "source.toolkit.fluxcd.io/v1", kind: "HelmRepository", resource: "helmrepositories", namespaced: true, short: []string{"helmrepo"}},
	{gvr: "source.toolkit.fluxcd.io/v1/helmcharts", apiVersion: "source.toolkit.fluxcd.io/v1", kind: "HelmChart", resource: "helmcharts", namespaced: true, short: []string{"hc"}},
	{gvr: "source.toolkit.fluxcd.io/v1/buckets", apiVersion: "source.toolkit.fluxcd.io/v1", kind: "Bucket", resource: "buckets", namespaced: true},
	{gvr: "kustomize.toolkit.fluxcd.io/v1/kustomizations", apiVersion: "kustomize.toolkit.fluxcd.io/v1", kind: "Kustomization", resource: "kustomizations", namespaced: true, short: []string{"ks"}},
	{gvr: "helm.toolkit.fluxcd.io/v2/helmreleases", apiVersion: "helm.toolkit.fluxcd.io/v2", kind: "HelmRelease", resource: "helmreleases", namespaced: true, short: []string{"hr"}},
}

var kindByGVR = func() map[string]*kindDef {
	m := map[string]*kindDef{}
	for _, d := range kindDefs {
		m[d.gvr] = d
	}
	return m
}()

func mapOf(o map[string]any, k string) map[string]any {
	m, _ := o[k].(map[string]any)
	return m
}

func nameOf(o map[string]any) string {
	s, _ := mapOf(o, "metadata")["name"].(string)
	return s
}

// ---- store ------------------------------------------------------------------------------

type event struct {
	rv  uint64
	typ watch.EventType
	gvr string
	obj *unstructured.Unstructured
}

const worldLogSize = 20000

// World is the in-memory object store of one fake cluster.
type World struct {
	cluster string

	mu       sync.Mutex
	rv       uint64
	objs     map[string]map[string]*unstructured.Unstructured // gvr → "ns/name" → object
	log      []event
	logStart uint64
	watchers map[*kwatch]struct{}
	nextUID  uint64
	csh      clientsetHolder
}

func key(ns, name string) string { return ns + "/" + name }

func ts(t time.Time) string { return t.UTC().Format(time.RFC3339) }

// NewWorld generates the objects for a fake cluster whose pods are in c.
func NewWorld(c *Cluster) *World {
	w := &World{cluster: c.name, rv: 1_000_000, objs: map[string]map[string]*unstructured.Unstructured{},
		watchers: map[*kwatch]struct{}{}, logStart: 0}
	w.generate(c)
	return w
}

func (w *World) uid(kind string) string {
	w.nextUID++
	return fmt.Sprintf("%08x-%s-4000-9000-%012x", w.nextUID, kind[:min(4, len(kind))], len(w.cluster))
}

func (w *World) putLocked(gvr string, o map[string]any) {
	d := kindByGVR[gvr]
	w.rv++
	o["apiVersion"], o["kind"] = d.apiVersion, d.kind
	md := mapOf(o, "metadata")
	md["uid"] = w.uid(d.kind)
	md["resourceVersion"] = strconv.FormatUint(w.rv, 10)
	if md["creationTimestamp"] == nil {
		md["creationTimestamp"] = ts(time.Now().Add(-48 * time.Hour))
	}
	m := w.objs[gvr]
	if m == nil {
		m = map[string]*unstructured.Unstructured{}
		w.objs[gvr] = m
	}
	ns, _ := md["namespace"].(string)
	m[key(ns, nameOf(o))] = &unstructured.Unstructured{Object: o}
}

func objMeta(ns, name string, labels map[string]string) map[string]any {
	md := map[string]any{"name": name}
	if ns != "" {
		md["namespace"] = ns
	}
	if len(labels) > 0 {
		l := map[string]any{}
		for k, v := range labels {
			l[k] = v
		}
		md["labels"] = l
	}
	return md
}

func managed(manager, op, apiVersion string, fields map[string]any) map[string]any {
	return map[string]any{"manager": manager, "operation": op, "apiVersion": apiVersion, "time": ts(time.Now().Add(-24 * time.Hour)),
		"fieldsType": "FieldsV1", "fieldsV1": fields}
}

func owner(apiVersion, kind, name, uid string) []any {
	return []any{map[string]any{"apiVersion": apiVersion, "kind": kind, "name": name, "uid": uid, "controller": true, "blockOwnerDeletion": true}}
}

func podTemplate(app string, image string) map[string]any {
	return map[string]any{
		"metadata": map[string]any{"labels": map[string]any{"app": app}},
		"spec": map[string]any{"containers": []any{map[string]any{"name": app, "image": image,
			"ports": []any{map[string]any{"containerPort": int64(8080)}}}}},
	}
}

func teamNS(i int) string { return "team-" + strconv.Itoa(i%namespaces) }

func (w *World) generate(c *Cluster) {
	// pods per app (for replica counts) and warning events for failing pods
	perApp := make([]int, apps)
	type failing struct {
		ns, name, uid, reason, msg string
		count                      int
	}
	var fails []failing
	c.mu.Lock()
	for i := range c.pods {
		p := &c.pods[i]
		if p.state == stDead {
			continue
		}
		perApp[int(p.app)%apps]++
		var f failing
		switch p.state {
		case stCrash:
			f = failing{reason: "BackOff", msg: "Back-off restarting failed container"}
		case stImagePull:
			f = failing{reason: "Failed", msg: "Failed to pull image: manifest unknown"}
		case stOOM:
			f = failing{reason: "OOMKilling", msg: "Memory cgroup out of memory: Killed process"}
		case stPending:
			f = failing{reason: "FailedScheduling", msg: "0/150 nodes are available: insufficient cpu"}
		case stError:
			f = failing{reason: "Failed", msg: "Error: container exited with code 1"}
		default:
			continue
		}
		f.ns, f.name, f.uid, f.count = "team-"+strconv.Itoa(int(p.ns)), p.name(), p.uid(c.name), 1+int(p.restarts)
		if len(fails) < 400 {
			fails = append(fails, f)
		}
	}
	c.mu.Unlock()

	w.mu.Lock()
	defer w.mu.Unlock()
	for _, ns := range []string{"kube-system", "default", "flux-system", "podinfo", "monitoring"} {
		w.putLocked("v1/namespaces", map[string]any{"metadata": objMeta("", ns, map[string]string{"kubernetes.io/metadata.name": ns}), "status": map[string]any{"phase": "Active"}})
	}
	for i := 0; i < namespaces; i++ {
		w.putLocked("v1/namespaces", map[string]any{"metadata": objMeta("", "team-"+strconv.Itoa(i), map[string]string{"kubernetes.io/metadata.name": "team-" + strconv.Itoa(i)}),
			"status": map[string]any{"phase": "Active"}})
		w.putLocked("v1/serviceaccounts", map[string]any{"metadata": objMeta(teamNS(i), "default", nil), "secrets": []any{}})
	}
	for i := 0; i < nodes; i++ {
		labels := map[string]string{"kubernetes.io/hostname": "node-" + strconv.Itoa(i)}
		if i < 3 {
			labels["node-role.kubernetes.io/control-plane"] = ""
		} else {
			labels["node-role.kubernetes.io/worker"] = ""
		}
		ready := "True"
		if i%47 == 5 {
			ready = "False"
		}
		w.putLocked("v1/nodes", map[string]any{"metadata": objMeta("", "node-"+strconv.Itoa(i), labels),
			"spec": map[string]any{},
			"status": map[string]any{
				"conditions":  []any{map[string]any{"type": "Ready", "status": ready}},
				"addresses":   []any{map[string]any{"type": "InternalIP", "address": fmt.Sprintf("10.0.%d.%d", i/200, 10+i%200)}},
				"allocatable": map[string]any{"cpu": "15890m", "memory": "63Gi", "pods": "110"},
				"nodeInfo": map[string]any{"kubeletVersion": "v1.31.4", "osImage": "Ubuntu 24.04 LTS", "kernelVersion": "6.8.0-51-generic",
					"containerRuntimeVersion": "containerd://1.7.24"},
			}})
	}
	for a := 0; a < apps; a++ {
		name, ns := "svc-"+strconv.Itoa(a), teamNS(a)
		replicas := int64(perApp[a])
		avail := replicas
		if a%23 == 4 {
			avail = max(0, replicas-1)
		}
		if a%61 == 7 {
			avail = 0
		}
		labels := map[string]string{"app": name}
		image := "registry.example.com/" + ns + "/" + name + ":" + strconv.Itoa(1+a%9) + ".0.0"
		rs := fmt.Sprintf("%s-%010x", name, rsHash(a))
		oldRS := fmt.Sprintf("%s-%010x", name, rsHash(a)^0x5a5a5a5a5a&0xffffffffff)
		dmeta := objMeta(ns, name, labels)
		var fieldsOwner string
		if a%3 == 0 {
			dmeta["labels"].(map[string]any)["kustomize.toolkit.fluxcd.io/name"] = "apps"
			dmeta["labels"].(map[string]any)["kustomize.toolkit.fluxcd.io/namespace"] = "team-" + strconv.Itoa(a%namespaces)
			fieldsOwner = "kustomize-controller"
		} else if a%3 == 1 {
			dmeta["labels"].(map[string]any)["app.kubernetes.io/managed-by"] = "Helm"
			dmeta["annotations"] = map[string]any{"meta.helm.sh/release-name": name, "meta.helm.sh/release-namespace": ns}
			fieldsOwner = "helm"
		} else {
			fieldsOwner = "kubectl-client-side-apply"
		}
		dmeta["managedFields"] = []any{
			managed(fieldsOwner, "Apply", "apps/v1", map[string]any{"f:spec": map[string]any{"f:replicas": map[string]any{},
				"f:template": map[string]any{"f:spec": map[string]any{"f:containers": map[string]any{
					`k:{"name":"` + name + `"}`: map[string]any{".": map[string]any{}, "f:image": map[string]any{}, "f:name": map[string]any{}}}}}}}),
			managed("kube-controller-manager", "Update", "apps/v1", map[string]any{"f:status": map[string]any{"f:availableReplicas": map[string]any{}, "f:readyReplicas": map[string]any{}}}),
		}
		w.putLocked("apps/v1/deployments", map[string]any{"metadata": dmeta,
			"spec":   map[string]any{"replicas": replicas, "selector": map[string]any{"matchLabels": map[string]any{"app": name}}, "template": podTemplate(name, image)},
			"status": map[string]any{"replicas": replicas, "readyReplicas": avail, "availableReplicas": avail, "updatedReplicas": replicas}})
		duid := w.objs["apps/v1/deployments"][key(ns, name)].GetUID()
		for i, rsn := range []string{oldRS, rs} {
			rmeta := objMeta(ns, rsn, map[string]string{"app": name, "pod-template-hash": fmt.Sprintf("%x", rsHash(a))})
			rmeta["annotations"] = map[string]any{"deployment.kubernetes.io/revision": strconv.Itoa(i + 1)}
			rmeta["ownerReferences"] = owner("apps/v1", "Deployment", name, string(duid))
			want, got := int64(0), int64(0)
			tmplImage := "registry.example.com/" + ns + "/" + name + ":0." + strconv.Itoa(a%9) + ".0"
			if i == 1 {
				want, got, tmplImage = replicas, avail, image
			}
			w.putLocked("apps/v1/replicasets", map[string]any{"metadata": rmeta,
				"spec":   map[string]any{"replicas": want, "selector": map[string]any{"matchLabels": map[string]any{"app": name}}, "template": podTemplate(name, tmplImage)},
				"status": map[string]any{"replicas": want, "readyReplicas": got}})
		}
		w.putLocked("v1/services", map[string]any{"metadata": objMeta(ns, name, labels),
			"spec": map[string]any{"type": "ClusterIP", "clusterIP": fmt.Sprintf("10.96.%d.%d", a/250, 1+a%250), "selector": map[string]any{"app": name},
				"ports": []any{map[string]any{"port": int64(80), "protocol": "TCP", "targetPort": int64(8080)}}}})
		eps := []any{}
		for e := int64(0); e < min(replicas, 3); e++ {
			eps = append(eps, map[string]any{"addresses": []any{fmt.Sprintf("10.244.%d.%d", a%250, 1+e)}, "conditions": map[string]any{"ready": e < avail}})
		}
		esm := objMeta(ns, name+"-x"+strconv.Itoa(a%10), map[string]string{"kubernetes.io/service-name": name})
		w.putLocked("discovery.k8s.io/v1/endpointslices", map[string]any{"metadata": esm, "addressType": "IPv4", "endpoints": eps})
		if a%7 == 0 {
			w.putLocked("networking.k8s.io/v1/ingresses", map[string]any{"metadata": objMeta(ns, name, labels),
				"spec":   map[string]any{"ingressClassName": "nginx", "rules": []any{map[string]any{"host": name + "." + ns + ".example.com"}}, "tls": []any{map[string]any{}}},
				"status": map[string]any{"loadBalancer": map[string]any{"ingress": []any{map[string]any{"ip": "203.0.113.10"}}}}})
		}
		if a%40 == 0 {
			w.putLocked("autoscaling/v2/horizontalpodautoscalers", map[string]any{"metadata": objMeta(ns, name, labels),
				"spec": map[string]any{"scaleTargetRef": map[string]any{"kind": "Deployment", "name": name}, "minReplicas": int64(2), "maxReplicas": int64(10),
					"metrics": []any{map[string]any{"type": "Resource", "resource": map[string]any{"name": "cpu", "target": map[string]any{"type": "Utilization", "averageUtilization": int64(70)}}}}},
				"status": map[string]any{"currentReplicas": replicas}})
		}
		if a%40 == 1 {
			w.putLocked("policy/v1/poddisruptionbudgets", map[string]any{"metadata": objMeta(ns, name, labels),
				"spec":   map[string]any{"minAvailable": int64(1), "selector": map[string]any{"matchLabels": map[string]any{"app": name}}},
				"status": map[string]any{"disruptionsAllowed": max(0, replicas-1), "currentHealthy": avail, "expectedPods": replicas}})
		}
	}
	for i := 0; i < 20; i++ {
		name, ns := "db-"+strconv.Itoa(i), teamNS(i+3)
		w.putLocked("apps/v1/statefulsets", map[string]any{"metadata": objMeta(ns, name, map[string]string{"app": name}),
			"spec":   map[string]any{"replicas": int64(3), "serviceName": name, "template": podTemplate(name, "postgres:16")},
			"status": map[string]any{"replicas": int64(3), "readyReplicas": int64(3 - i%2)}})
		w.putLocked("v1/persistentvolumeclaims", map[string]any{"metadata": objMeta(ns, "data-"+name+"-0", nil),
			"spec":   map[string]any{"storageClassName": "fast", "volumeName": "pv-" + strconv.Itoa(i), "accessModes": []any{"ReadWriteOnce"}},
			"status": map[string]any{"phase": "Bound", "capacity": map[string]any{"storage": "50Gi"}, "accessModes": []any{"ReadWriteOnce"}}})
		w.putLocked("v1/persistentvolumes", map[string]any{"metadata": objMeta("", "pv-"+strconv.Itoa(i), nil),
			"spec": map[string]any{"capacity": map[string]any{"storage": "50Gi"}, "accessModes": []any{"ReadWriteOnce"}, "storageClassName": "fast",
				"persistentVolumeReclaimPolicy": "Delete", "claimRef": map[string]any{"namespace": ns, "name": "data-" + name + "-0"}},
			"status": map[string]any{"phase": "Bound"}})
	}
	for _, ds := range []string{"kube-proxy", "node-exporter", "fluent-bit", "cilium", "csi-node", "falco"} {
		w.putLocked("apps/v1/daemonsets", map[string]any{"metadata": objMeta("team-0", ds, map[string]string{"app": ds}),
			"spec": map[string]any{"template": podTemplate(ds, "registry.example.com/infra/"+ds+":latest")},
			"status": map[string]any{"desiredNumberScheduled": int64(nodes), "currentNumberScheduled": int64(nodes), "numberReady": int64(nodes),
				"updatedNumberScheduled": int64(nodes), "numberAvailable": int64(nodes)}})
	}
	for i := 0; i < 8; i++ {
		name, ns := "report-"+strconv.Itoa(i), teamNS(5)
		cm := map[string]any{"metadata": objMeta(ns, name, nil), "spec": map[string]any{"schedule": fmt.Sprintf("%d */%d * * *", i*5%60, 1+i%6), "suspend": i == 3,
			"jobTemplate": map[string]any{"spec": map[string]any{"template": map[string]any{"spec": map[string]any{
				"restartPolicy": "OnFailure", "containers": []any{map[string]any{"name": "job", "image": "registry.example.com/jobs/report:1"}}}}}}},
			"status": map[string]any{"lastScheduleTime": ts(time.Now().Add(-90 * time.Minute))}}
		w.putLocked("batch/v1/cronjobs", cm)
		cuid := w.objs["batch/v1/cronjobs"][key(ns, name)].GetUID()
		for j := 0; j < 2; j++ {
			jm := objMeta(ns, fmt.Sprintf("%s-2894%d%d", name, i, j), nil)
			jm["ownerReferences"] = owner("batch/v1", "CronJob", name, string(cuid))
			cond := "Complete"
			if (i+j)%5 == 4 {
				cond = "Failed"
			}
			w.putLocked("batch/v1/jobs", map[string]any{"metadata": jm, "spec": map[string]any{"completions": int64(1)},
				"status": map[string]any{"succeeded": int64(map[bool]int{true: 1, false: 0}[cond == "Complete"]), "startTime": ts(time.Now().Add(-3 * time.Hour)),
					"completionTime": ts(time.Now().Add(-3*time.Hour + 42*time.Second)),
					"conditions":     []any{map[string]any{"type": cond, "status": "True"}}}})
		}
	}
	for i := 0; i < 5; i++ {
		w.putLocked("networking.k8s.io/v1/networkpolicies", map[string]any{"metadata": objMeta(teamNS(i), "deny-all", nil),
			"spec": map[string]any{"podSelector": map[string]any{}, "policyTypes": []any{"Ingress"}}})
	}
	for i, f := range fails {
		ev := objMeta(f.ns, fmt.Sprintf("%s.%x", f.name, 0x17a0000+i), nil)
		w.putLocked("v1/events", map[string]any{"metadata": ev, "type": "Warning", "reason": f.reason, "message": f.msg, "count": int64(f.count),
			"involvedObject": map[string]any{"kind": "Pod", "name": f.name, "namespace": f.ns, "uid": f.uid},
			"lastTimestamp":  ts(time.Now().Add(-time.Duration(i%120) * time.Minute))})
	}
	for _, sc := range []string{"fast", "standard", "archive"} {
		w.putLocked("storage.k8s.io/v1/storageclasses", map[string]any{"metadata": objMeta("", sc, nil), "provisioner": "ebs.csi.aws.com"})
	}
	for i := 0; i < 120; i++ {
		data := map[string]any{"config.yaml": "key: value", "app.env": "A=1"}
		w.putLocked("v1/configmaps", map[string]any{"metadata": objMeta(teamNS(i), "config-"+strconv.Itoa(i), nil), "data": data})
	}
	for i := 0; i < 80; i++ {
		t := "Opaque"
		if i%4 == 0 {
			t = "kubernetes.io/tls"
		}
		w.putLocked("v1/secrets", map[string]any{"metadata": objMeta(teamNS(i), "secret-"+strconv.Itoa(i), nil), "type": t,
			"data": map[string]any{"tls.crt": "REDACTED", "tls.key": "REDACTED"}})
	}
	for _, crd := range []string{"gitrepositories.source.toolkit.fluxcd.io", "ocirepositories.source.toolkit.fluxcd.io", "helmrepositories.source.toolkit.fluxcd.io",
		"helmcharts.source.toolkit.fluxcd.io", "buckets.source.toolkit.fluxcd.io", "kustomizations.kustomize.toolkit.fluxcd.io", "helmreleases.helm.toolkit.fluxcd.io"} {
		w.putLocked("apiextensions.k8s.io/v1/customresourcedefinitions", map[string]any{"metadata": objMeta("", crd, nil), "spec": map[string]any{"group": strings.SplitN(crd, ".", 2)[1]}})
	}
	for _, o := range flux.DemoObjects(w.cluster, time.Now()) {
		for _, d := range kindDefs {
			if d.apiVersion == o.GetAPIVersion() && d.kind == o.GetKind() {
				w.putLocked(d.gvr, o.Object)
			}
		}
	}
	for _, g := range extGen {
		g(w, c)
	}
}

// ---- discovery ------------------------------------------------------------------------------

// Discovery returns the fake cluster's resources.
func Discovery() []disco.Resource {
	out := make([]disco.Resource, 0, len(kindDefs))
	for _, d := range kindDefs {
		out = append(out, disco.Resource{Group: d.group(), Version: d.version(), Resource: d.resource, Kind: d.kind, Namespaced: d.namespaced,
			ShortNames: d.short, Verbs: []string{"create", "delete", "get", "list", "patch", "update", "watch"}})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].GVR() < out[j].GVR() })
	return out
}
