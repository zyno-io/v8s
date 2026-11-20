package ingest

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	appsv1 "k8s.io/api/apps/v1"
	autoscalingv2 "k8s.io/api/autoscaling/v2"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	networkingv1 "k8s.io/api/networking/v1"
	policyv1 "k8s.io/api/policy/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/intstr"

	"github.com/zyno-io/v8s/agent/pkg/protocol"
)

// typed wires a module to its typed list/object and projection function.
func typed[O any, PO interface {
	*O
	runtime.Object
}, L any, PL interface {
	*L
	runtime.Object
}](m *Module, items func(*L) []O, project func(*O) *Row) {
	m.Curated = true
	m.NewList = func() runtime.Object { return PL(new(L)) }
	m.Each = func(l runtime.Object, fn func(*Row)) {
		it := items((*L)(l.(PL)))
		for i := range it {
			fn(project(&it[i]))
		}
	}
	m.ProjectObj = func(o runtime.Object) *Row {
		if x, ok := o.(PO); ok {
			return project((*O)(x))
		}
		return nil
	}
	Register(m)
}

func ready(cur, want int32) string { return strconv.Itoa(int(cur)) + "/" + strconv.Itoa(int(want)) }

// scaleTone: success when all desired replicas are ready, neutral for scaled-to-zero, danger when
// none of the desired replicas are ready, warning otherwise.
func scaleTone(ready, want int32) uint8 {
	switch {
	case want == 0:
		return ToneNeutral
	case ready >= want:
		return ToneSuccess
	case ready == 0:
		return ToneDanger
	}
	return ToneWarning
}

func selectorString(m map[string]string) string {
	if len(m) == 0 {
		return ""
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	for i, k := range keys {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(k + "=" + m[k])
	}
	return b.String()
}

func images(ps *corev1.PodSpec) string {
	var seen []string
	for i := range ps.Containers {
		im := ps.Containers[i].Image
		dup := false
		for _, s := range seen {
			dup = dup || s == im
		}
		if !dup {
			seen = append(seen, im)
		}
	}
	return strings.Join(seen, ",")
}

func i32(p *int32, def int32) int32 {
	if p == nil {
		return def
	}
	return *p
}

func init() {
	registerPods()
	registerWorkloads()
	registerNetworking()
	registerClusterKinds()
	registerPolicyKinds()
}

// hide marks a column hidden until the user shows it from the Columns menu.
func hide(c Col) Col { c.Hidden = true; return c }

func narrow(c Col, w int) Col { c.Width = w; return c }

func registerPods() {
	m := &Module{GVR: PodsGVR, Kind: "Pod", Namespaced: true, Short: []string{"po"}, Tier0: true,
		Cols: []Col{cCluster(), cName(), narrow(cNS(), 120),
			cInt("ready", "Ready", 64), cInt("containers", "Containers", 84), cDict("status", "Status", 120),
			cTone(), cInt("restarts", "Restarts", 68), cDict("vpa", "VPA", 36),
			// Usage block: the schema order below is the visible order; the UI draws the visible ones as one block.
			cUse("cpuUse", "CPU", "", UseCPU, "millicores", 44, false),
			cRes("cpuReq", "CPU req", "req", ResCPUReq, "millicores", 44, true),
			cRes("cpuLim", "CPU lim", "lim", ResCPULim, "millicores", 44, true),
			cUse("cpuPctReq", "%CPU/R", "%R", UseCPUPctReq, "permille", 36, false),
			cUse("cpuPctLim", "%CPU/L", "%L", UseCPUPctLim, "permille", 36, false),
			cUse("cpuPctNode", "%CPU/N", "%N", UseCPUPctNode, "permille", 36, true),
			cUse("memUse", "MEM", "", UseMem, "MiB", 48, false),
			cRes("memReq", "MEM req", "req", ResMemReq, "MiB", 48, true),
			cRes("memLim", "MEM lim", "lim", ResMemLim, "MiB", 48, true),
			cUse("memPctReq", "%MEM/R", "%R", UseMemPctReq, "permille", 36, false),
			cUse("memPctLim", "%MEM/L", "%L", UseMemPctLim, "permille", 36, false),
			cUse("memPctNode", "%MEM/N", "%N", UseMemPctNode, "permille", 36, true),
			cDict("node", "Node", 120), hide(cIP("ip", "IP", 120, FPodIP)), narrow(cAge(), 56)},
		Usage: "pods",
		IPs:   true, IPOrder: 10}
	typed(m, func(l *corev1.PodList) []corev1.Pod { return l.Items }, Project)
}

func registerWorkloads() {
	typed(&Module{GVR: "apps/v1/deployments", Kind: "Deployment", Namespaced: true, Short: []string{"deploy"}, Tier0: true,
		Cols: []Col{cCluster(), cName(), cNS(), cStr("ready", "Ready", 70), cInt("upToDate", "Up-to-date", 90), cInt("available", "Available", 84),
			cStr("images", "Images", 280), cTone(), cAge()}},
		func(l *appsv1.DeploymentList) []appsv1.Deployment { return l.Items },
		func(d *appsv1.Deployment) *Row {
			r := newRow(&d.ObjectMeta, 4)
			want := i32(d.Spec.Replicas, 1)
			r.putS(ready(d.Status.ReadyReplicas, want))
			r.putN(int64(d.Status.UpdatedReplicas))
			r.putN(int64(d.Status.AvailableReplicas))
			r.putS(images(&d.Spec.Template.Spec))
			r.tone = scaleTone(d.Status.AvailableReplicas, want)
			setFacts(r, deploymentFacts(d))
			return r
		})

	typed(&Module{GVR: "apps/v1/statefulsets", Kind: "StatefulSet", Namespaced: true, Short: []string{"sts"}, Tier0: true,
		Cols: []Col{cCluster(), cName(), cNS(), cStr("ready", "Ready", 70), cStr("images", "Images", 280), cTone(), cAge()}},
		func(l *appsv1.StatefulSetList) []appsv1.StatefulSet { return l.Items },
		func(s *appsv1.StatefulSet) *Row {
			r := newRow(&s.ObjectMeta, 2)
			want := i32(s.Spec.Replicas, 1)
			r.putS(ready(s.Status.ReadyReplicas, want))
			r.putS(images(&s.Spec.Template.Spec))
			r.tone = scaleTone(s.Status.ReadyReplicas, want)
			setFacts(r, statefulSetFacts(s))
			return r
		})

	typed(&Module{GVR: "apps/v1/daemonsets", Kind: "DaemonSet", Namespaced: true, Short: []string{"ds"}, Tier0: true,
		Cols: []Col{cCluster(), cName(), cNS(), cInt("desired", "Desired", 70), cInt("current", "Current", 70), cInt("ready", "Ready", 60),
			cInt("upToDate", "Up-to-date", 90), cInt("available", "Available", 84), cStr("images", "Images", 280), cTone(), cAge()}},
		func(l *appsv1.DaemonSetList) []appsv1.DaemonSet { return l.Items },
		func(d *appsv1.DaemonSet) *Row {
			r := newRow(&d.ObjectMeta, 6)
			s := &d.Status
			r.putN(int64(s.DesiredNumberScheduled))
			r.putN(int64(s.CurrentNumberScheduled))
			r.putN(int64(s.NumberReady))
			r.putN(int64(s.UpdatedNumberScheduled))
			r.putN(int64(s.NumberAvailable))
			r.putS(images(&d.Spec.Template.Spec))
			r.tone = scaleTone(s.NumberReady, s.DesiredNumberScheduled)
			setFacts(r, daemonSetFacts(d))
			return r
		})

	typed(&Module{GVR: "apps/v1/replicasets", Kind: "ReplicaSet", Namespaced: true, Short: []string{"rs"}, Tier0: true,
		Cols: []Col{cCluster(), cName(), cNS(), cInt("desired", "Desired", 70), cInt("current", "Current", 70), cInt("ready", "Ready", 60),
			{Column: protocol.Column{Key: "owner", Type: protocol.ColDict, Label: "Owner", Width: 200}, Src: SrcOwner, Sortable: true, Filterable: true},
			cTone(), cAge()}},
		func(l *appsv1.ReplicaSetList) []appsv1.ReplicaSet { return l.Items },
		func(s *appsv1.ReplicaSet) *Row {
			r := newRow(&s.ObjectMeta, 3)
			want := i32(s.Spec.Replicas, 1)
			r.putN(int64(want))
			r.putN(int64(s.Status.Replicas))
			r.putN(int64(s.Status.ReadyReplicas))
			r.tone = scaleTone(s.Status.ReadyReplicas, want)
			if want == 0 {
				r.tone = ToneNeutral
			}
			return r
		})

	typed(&Module{GVR: "batch/v1/jobs", Kind: "Job", Namespaced: true, Tier0: true,
		Cols: []Col{cCluster(), cName(), cNS(), cStr("completions", "Completions", 100), cStr("duration", "Duration", 80), cDict("status", "Status", 100),
			{Column: protocol.Column{Key: "owner", Type: protocol.ColDict, Label: "Owner", Width: 200}, Src: SrcOwner, Sortable: true, Filterable: true},
			cTone(), cAge()}},
		func(l *batchv1.JobList) []batchv1.Job { return l.Items },
		func(j *batchv1.Job) *Row {
			r := newRow(&j.ObjectMeta, 3)
			want := i32(j.Spec.Completions, 1)
			r.putS(ready(j.Status.Succeeded, want))
			dur := ""
			if j.Status.StartTime != nil && j.Status.CompletionTime != nil {
				dur = j.Status.CompletionTime.Sub(j.Status.StartTime.Time).Round(1e9).String()
			}
			r.putS(dur)
			status, tone := "Pending", ToneNeutral
			switch {
			case jobCond(j, batchv1.JobFailed):
				status, tone = "Failed", ToneDanger
			case jobCond(j, batchv1.JobComplete):
				status, tone = "Complete", ToneSuccess
			case jobCond(j, batchv1.JobSuspended):
				status, tone = "Suspended", ToneWarning
			case j.Status.Active > 0:
				status, tone = "Running", ToneInfo
			}
			r.putS(status)
			r.tone = tone
			setFacts(r, jobFacts(j))
			return r
		})

	typed(&Module{GVR: "batch/v1/cronjobs", Kind: "CronJob", Namespaced: true, Short: []string{"cj"}, Tier0: true,
		Cols: []Col{cCluster(), cName(), cNS(), cStr("schedule", "Schedule", 130), cDict("suspend", "Suspend", 76), cInt("active", "Active", 64),
			cTime("lastSchedule", "Last schedule", 110), cTone(), cAge()}},
		func(l *batchv1.CronJobList) []batchv1.CronJob { return l.Items },
		func(c *batchv1.CronJob) *Row {
			r := newRow(&c.ObjectMeta, 4)
			r.putS(c.Spec.Schedule)
			susp := c.Spec.Suspend != nil && *c.Spec.Suspend
			r.putS(strconv.FormatBool(susp))
			r.putN(int64(len(c.Status.Active)))
			var last int64
			if c.Status.LastScheduleTime != nil {
				last = c.Status.LastScheduleTime.UnixMilli()
			}
			r.putN(last)
			if susp {
				r.tone = ToneWarning
			}
			r.facts = cronFacts(c)
			return r
		})
}

func jobCond(j *batchv1.Job, t batchv1.JobConditionType) bool {
	for i := range j.Status.Conditions {
		if j.Status.Conditions[i].Type == t && j.Status.Conditions[i].Status == corev1.ConditionTrue {
			return true
		}
	}
	return false
}

func servicePorts(ports []corev1.ServicePort) string {
	parts := make([]string, 0, len(ports))
	for _, p := range ports {
		s := strconv.Itoa(int(p.Port))
		if p.NodePort != 0 {
			s += ":" + strconv.Itoa(int(p.NodePort))
		}
		parts = append(parts, s+"/"+string(p.Protocol))
	}
	return strings.Join(parts, ",")
}

func registerNetworking() {
	typed(&Module{GVR: "v1/services", Kind: "Service", Namespaced: true, Short: []string{"svc"}, Tier0: true, IPs: true, IPOrder: 20,
		Cols: []Col{cCluster(), cName(), cNS(), cDict("type", "Type", 100), cStr("clusterIP", "Cluster IP", 120), cStr("externalIP", "External IP", 130),
			cStr("ports", "Ports", 160), cStr("selector", "Selector", 200), cTone(), cAge()}},
		func(l *corev1.ServiceList) []corev1.Service { return l.Items },
		func(s *corev1.Service) *Row {
			r := newRow(&s.ObjectMeta, 5)
			r.putS(string(s.Spec.Type))
			r.putS(s.Spec.ClusterIP)
			var ext []string
			ext = append(ext, s.Spec.ExternalIPs...)
			for _, in := range s.Status.LoadBalancer.Ingress {
				if in.IP != "" {
					ext = append(ext, in.IP)
				} else if in.Hostname != "" {
					ext = append(ext, in.Hostname)
				}
			}
			r.putS(strings.Join(ext, ","))
			r.putS(servicePorts(s.Spec.Ports))
			r.putS(selectorString(s.Spec.Selector))
			if s.Spec.Type == corev1.ServiceTypeLoadBalancer && len(s.Status.LoadBalancer.Ingress) == 0 {
				r.tone = ToneWarning
			}
			r.ips = serviceIPs(s)
			return r
		})

	typed(&Module{GVR: "networking.k8s.io/v1/ingresses", Kind: "Ingress", Namespaced: true, Short: []string{"ing"}, Tier0: true, IPs: true, IPOrder: 50,
		Cols: []Col{cCluster(), cName(), cNS(), cDict("class", "Class", 100), cStr("hosts", "Hosts", 260), cStr("address", "Address", 140),
			cDict("ports", "Ports", 80), cTone(), cAge()}},
		func(l *networkingv1.IngressList) []networkingv1.Ingress { return l.Items },
		func(in *networkingv1.Ingress) *Row {
			r := newRow(&in.ObjectMeta, 4)
			class := ""
			if in.Spec.IngressClassName != nil {
				class = *in.Spec.IngressClassName
			} else if v, ok := in.Annotations["kubernetes.io/ingress.class"]; ok {
				class = v
			}
			r.putS(class)
			var hosts []string
			for _, ru := range in.Spec.Rules {
				if ru.Host != "" {
					hosts = append(hosts, ru.Host)
				}
			}
			r.putS(strings.Join(hosts, ","))
			var addr []string
			for _, lb := range in.Status.LoadBalancer.Ingress {
				if lb.IP != "" {
					addr = append(addr, lb.IP)
				} else if lb.Hostname != "" {
					addr = append(addr, lb.Hostname)
				}
			}
			r.putS(strings.Join(addr, ","))
			ports := "80"
			if len(in.Spec.TLS) > 0 {
				ports = "80, 443"
			}
			r.putS(ports)
			if len(addr) == 0 {
				r.tone = ToneWarning
			}
			setFacts(r, ingressFacts(in))
			r.ips = ingressIPs(in)
			return r
		})

	typed(&Module{GVR: "discovery.k8s.io/v1/endpointslices", Kind: "EndpointSlice", Namespaced: true, Tier0: true, IPs: true, IPOrder: 40, IPDetailCol: "service",
		Cols: []Col{cCluster(), cName(), cNS(), cDict("service", "Service", 200), cDict("addressType", "Address type", 100),
			cInt("ready", "Ready", 60), cInt("total", "Total", 60), cTone(), cAge()}},
		func(l *discoveryv1.EndpointSliceList) []discoveryv1.EndpointSlice { return l.Items },
		func(e *discoveryv1.EndpointSlice) *Row {
			r := newRow(&e.ObjectMeta, 4)
			r.putS(e.Labels[discoveryv1.LabelServiceName])
			r.putS(string(e.AddressType))
			rd := 0
			for i := range e.Endpoints {
				if c := e.Endpoints[i].Conditions.Ready; c == nil || *c {
					rd++
				}
			}
			r.putN(int64(rd))
			r.putN(int64(len(e.Endpoints)))
			if len(e.Endpoints) > 0 && rd == 0 {
				r.tone = ToneDanger
			} else if rd < len(e.Endpoints) {
				r.tone = ToneWarning
			}
			setFacts(r, endpointSliceFacts(e))
			r.ips = endpointSliceIPs(e)
			return r
		})

	typed(&Module{GVR: "networking.k8s.io/v1/networkpolicies", Kind: "NetworkPolicy", Namespaced: true, Short: []string{"netpol"},
		Cols: []Col{cCluster(), cName(), cNS(), cStr("podSelector", "Pod selector", 220), cDict("policyTypes", "Types", 120),
			cInt("ingress", "Ingress rules", 100), cInt("egress", "Egress rules", 100), cAge()}},
		func(l *networkingv1.NetworkPolicyList) []networkingv1.NetworkPolicy { return l.Items },
		func(n *networkingv1.NetworkPolicy) *Row {
			r := newRow(&n.ObjectMeta, 4)
			sel := "<all pods>"
			if len(n.Spec.PodSelector.MatchLabels) > 0 {
				sel = selectorString(n.Spec.PodSelector.MatchLabels)
			}
			r.putS(sel)
			var ty []string
			for _, t := range n.Spec.PolicyTypes {
				ty = append(ty, string(t))
			}
			r.putS(strings.Join(ty, ","))
			r.putN(int64(len(n.Spec.Ingress)))
			r.putN(int64(len(n.Spec.Egress)))
			return r
		})
}

func nodeRoles(l map[string]string) string {
	var roles []string
	for k := range l {
		if role, ok := strings.CutPrefix(k, "node-role.kubernetes.io/"); ok && role != "" {
			roles = append(roles, role)
		}
	}
	if len(roles) == 0 {
		if v := l["kubernetes.io/role"]; v != "" {
			roles = append(roles, v)
		}
	}
	sort.Strings(roles)
	if len(roles) == 0 {
		return "<none>"
	}
	return strings.Join(roles, ",")
}

func registerClusterKinds() {
	typed(&Module{GVR: "v1/nodes", Kind: "Node", Short: []string{"no"}, Tier0: true, IPs: true, IPOrder: 30,
		Cols: []Col{cCluster(), cName(), cDict("status", "Status", 132), cDict("roles", "Roles", 110), cDict("version", "Version", 84),
			cStr("internalIP", "Internal IP", 116), hide(cDict("os", "OS image", 180)), hide(cDict("kernel", "Kernel", 130)), hide(cDict("runtime", "Runtime", 140)),
			// Usage block (docs/design-system.md): usage, % of the node's allocatable, allocatable. The keys cpu and memory are
			// kept from the string columns they replace so saved layouts still apply.
			cUse("cpuUse", "CPU", "", UseCPU, "millicores", 44, false),
			cUse("cpuPctAlloc", "%CPU", "%", UseCPUPctNode, "permille", 36, false),
			cRes("cpu", "CPU alloc", "alloc", ResCPUReq, "millicores", 52, false),
			cUse("memUse", "MEM", "", UseMem, "MiB", 48, false),
			cUse("memPctAlloc", "%MEM", "%", UseMemPctNode, "permille", 36, false),
			cRes("memory", "MEM alloc", "alloc", ResMemReq, "MiB", 56, false),
			cInt("pods", "Pods", 52), hide(cInt("taints", "Taints", 64)), cTone(), narrow(cAge(), 56)},
		Usage: "nodes"},
		func(l *corev1.NodeList) []corev1.Node { return l.Items },
		func(n *corev1.Node) *Row {
			r := newRow(&n.ObjectMeta, 9)
			status, tone := "Unknown", ToneWarning
			for i := range n.Status.Conditions {
				if c := &n.Status.Conditions[i]; c.Type == corev1.NodeReady {
					switch c.Status {
					case corev1.ConditionTrue:
						status, tone = "Ready", ToneSuccess
					case corev1.ConditionFalse:
						status, tone = "NotReady", ToneDanger
					}
				}
			}
			if n.Spec.Unschedulable {
				status += ",SchedulingDisabled"
				if tone == ToneSuccess {
					tone = ToneWarning
				}
			}
			r.putS(status)
			r.putS(nodeRoles(n.Labels))
			r.putS(n.Status.NodeInfo.KubeletVersion)
			ip := ""
			for _, a := range n.Status.Addresses {
				if a.Type == corev1.NodeInternalIP {
					ip = a.Address
					break
				}
			}
			r.putS(ip)
			r.putS(n.Status.NodeInfo.OSImage)
			r.putS(n.Status.NodeInfo.KernelVersion)
			r.putS(n.Status.NodeInfo.ContainerRuntimeVersion)
			cpu, mem, pods := n.Status.Allocatable[corev1.ResourceCPU], n.Status.Allocatable[corev1.ResourceMemory], n.Status.Allocatable[corev1.ResourcePods]
			r.putN(pods.Value())
			r.putN(int64(len(n.Spec.Taints)))
			r.tone = tone
			r.res = Res{CPUReq: clampU32(cpu.MilliValue()), MemReq: clampU64(mem.Value())} // allocatable, not capacity
			setFacts(r, nodeFacts(n))
			r.ips = nodeIPs(n)
			return r
		})

	typed(&Module{GVR: "v1/namespaces", Kind: "Namespace", Short: []string{"ns"}, Tier0: true,
		Cols: []Col{cCluster(), cName(), cDict("status", "Status", 100), cTone(), cAge()}},
		func(l *corev1.NamespaceList) []corev1.Namespace { return l.Items },
		func(n *corev1.Namespace) *Row {
			r := newRow(&n.ObjectMeta, 1)
			r.putS(string(n.Status.Phase))
			if n.Status.Phase == corev1.NamespaceTerminating {
				r.tone = ToneWarning
			}
			return r
		})

	typed(&Module{GVR: "v1/persistentvolumeclaims", Kind: "PersistentVolumeClaim", Namespaced: true, Short: []string{"pvc"}, Tier0: true,
		Cols: []Col{cCluster(), cName(), cNS(), cDict("status", "Status", 90), cStr("volume", "Volume", 240), cDict("capacity", "Capacity", 80),
			cDict("accessModes", "Access modes", 100), cDict("storageClass", "Storage class", 130), cTone(), cAge()}},
		func(l *corev1.PersistentVolumeClaimList) []corev1.PersistentVolumeClaim { return l.Items },
		func(p *corev1.PersistentVolumeClaim) *Row {
			r := newRow(&p.ObjectMeta, 5)
			r.putS(string(p.Status.Phase))
			r.putS(p.Spec.VolumeName)
			c := p.Status.Capacity[corev1.ResourceStorage]
			r.putS(c.String())
			r.putS(accessModes(p.Status.AccessModes))
			sc := ""
			if p.Spec.StorageClassName != nil {
				sc = *p.Spec.StorageClassName
			}
			r.putS(sc)
			switch p.Status.Phase {
			case corev1.ClaimBound:
				r.tone = ToneSuccess
			case corev1.ClaimPending:
				r.tone = ToneWarning
			case corev1.ClaimLost:
				r.tone = ToneDanger
			}
			return r
		})

	typed(&Module{GVR: "v1/persistentvolumes", Kind: "PersistentVolume", Short: []string{"pv"}, GateClass: "pv.failed",
		Cols: []Col{cCluster(), cName(), cDict("status", "Status", 90), cStr("claim", "Claim", 240), cDict("capacity", "Capacity", 80),
			cDict("accessModes", "Access modes", 100), cDict("reclaimPolicy", "Reclaim", 90), cDict("storageClass", "Storage class", 130), cTone(), cAge()}},
		func(l *corev1.PersistentVolumeList) []corev1.PersistentVolume { return l.Items },
		func(p *corev1.PersistentVolume) *Row {
			r := newRow(&p.ObjectMeta, 6)
			r.putS(string(p.Status.Phase))
			claim := ""
			if p.Spec.ClaimRef != nil {
				claim = p.Spec.ClaimRef.Namespace + "/" + p.Spec.ClaimRef.Name
			}
			r.putS(claim)
			c := p.Spec.Capacity[corev1.ResourceStorage]
			r.putS(c.String())
			r.putS(accessModes(p.Spec.AccessModes))
			r.putS(string(p.Spec.PersistentVolumeReclaimPolicy))
			r.putS(p.Spec.StorageClassName)
			switch p.Status.Phase {
			case corev1.VolumeBound, corev1.VolumeAvailable:
				r.tone = ToneSuccess
			case corev1.VolumeReleased, corev1.VolumePending:
				r.tone = ToneWarning
			case corev1.VolumeFailed:
				r.tone = ToneDanger
			}
			return r
		})

	m := &Module{GVR: EventsGVR, Kind: "Event", Namespaced: true, Short: []string{"ev"}, Tier0: true,
		// The agent only ever holds Warning events (docs/performance.md §5).
		Tweak:       func(o *metav1.ListOptions) { o.FieldSelector = "type=Warning" },
		DefaultSort: []protocol.Sort{{Col: "lastSeen", Desc: true}},
		Cols:        eventCols()}
	typed(m, func(l *corev1.EventList) []corev1.Event { return l.Items },
		func(e *corev1.Event) *Row {
			r := newRow(&e.ObjectMeta, 7)
			r.putS(e.Type)
			r.putS(e.Reason)
			r.putS(e.InvolvedObject.Kind + "/" + e.InvolvedObject.Name)
			r.putS(e.Message)
			cnt := int64(e.Count)
			if cnt == 0 && e.Series != nil {
				cnt = int64(e.Series.Count)
			}
			r.putN(cnt)
			last := e.LastTimestamp.UnixMilli()
			switch {
			case e.Series != nil && !e.Series.LastObservedTime.IsZero():
				last = e.Series.LastObservedTime.UnixMilli()
			case e.LastTimestamp.IsZero() && !e.EventTime.IsZero():
				last = e.EventTime.UnixMilli()
			case e.LastTimestamp.IsZero():
				last = e.CreationTimestamp.UnixMilli()
			}
			r.putN(last)
			src := e.Source.Component
			if src == "" {
				src = e.ReportingController
			}
			r.putS(src)
			if e.InvolvedObject.Namespace != e.Namespace { // an event about a cluster-scoped object lives in another namespace
				r.SetFacts(&EventFacts{InvolvedNS: e.InvolvedObject.Namespace})
			}
			if e.Type == corev1.EventTypeWarning {
				r.tone = ToneWarning
			}
			return r
		})

	// The view-only module behind an involved-object events view (docs/protocol.md "Events of one object"): same
	// columns and projection, but not curated, tier-0 or served by discovery.
	Register(&Module{GVR: ObjectEventsGVR, Kind: "Event", Namespaced: true, DefaultSort: m.DefaultSort, Cols: eventCols(),
		NewList: m.NewList, Each: m.Each, ProjectObj: m.ProjectObj})

	typed(&Module{GVR: "v1/serviceaccounts", Kind: "ServiceAccount", Namespaced: true, Short: []string{"sa"},
		Cols: []Col{cCluster(), cName(), cNS(), cInt("secrets", "Secrets", 70), cAge()}},
		func(l *corev1.ServiceAccountList) []corev1.ServiceAccount { return l.Items },
		func(s *corev1.ServiceAccount) *Row {
			r := newRow(&s.ObjectMeta, 1)
			r.putN(int64(len(s.Secrets)))
			return r
		})
}

func accessModes(m []corev1.PersistentVolumeAccessMode) string {
	parts := make([]string, 0, len(m))
	for _, a := range m {
		switch a {
		case corev1.ReadWriteOnce:
			parts = append(parts, "RWO")
		case corev1.ReadOnlyMany:
			parts = append(parts, "ROX")
		case corev1.ReadWriteMany:
			parts = append(parts, "RWX")
		case corev1.ReadWriteOncePod:
			parts = append(parts, "RWOP")
		}
	}
	return strings.Join(parts, ",")
}

func intOrStr(v *intstr.IntOrString) string {
	if v == nil {
		return ""
	}
	return v.String()
}

func hpaTargets(h *autoscalingv2.HorizontalPodAutoscaler) string {
	var parts []string
	for i, m := range h.Spec.Metrics {
		cur := "<unknown>"
		if i < len(h.Status.CurrentMetrics) {
			c := h.Status.CurrentMetrics[i]
			switch {
			case c.Resource != nil && c.Resource.Current.AverageUtilization != nil:
				cur = strconv.Itoa(int(*c.Resource.Current.AverageUtilization)) + "%"
			case c.Resource != nil && c.Resource.Current.AverageValue != nil:
				cur = c.Resource.Current.AverageValue.String()
			case c.Pods != nil && c.Pods.Current.AverageValue != nil:
				cur = c.Pods.Current.AverageValue.String()
			case c.External != nil && c.External.Current.AverageValue != nil:
				cur = c.External.Current.AverageValue.String()
			case c.Object != nil && c.Object.Current.Value != nil:
				cur = c.Object.Current.Value.String()
			}
		}
		tgt, name := "?", string(m.Type)
		switch {
		case m.Resource != nil:
			name = string(m.Resource.Name)
			if m.Resource.Target.AverageUtilization != nil {
				tgt = strconv.Itoa(int(*m.Resource.Target.AverageUtilization)) + "%"
			} else if m.Resource.Target.AverageValue != nil {
				tgt = m.Resource.Target.AverageValue.String()
			}
		case m.Pods != nil:
			name = m.Pods.Metric.Name
			if m.Pods.Target.AverageValue != nil {
				tgt = m.Pods.Target.AverageValue.String()
			}
		case m.External != nil:
			name = m.External.Metric.Name
			if m.External.Target.AverageValue != nil {
				tgt = m.External.Target.AverageValue.String()
			} else if m.External.Target.Value != nil {
				tgt = m.External.Target.Value.String()
			}
		case m.Object != nil:
			name = m.Object.Metric.Name
			if m.Object.Target.Value != nil {
				tgt = m.Object.Target.Value.String()
			}
		}
		parts = append(parts, fmt.Sprintf("%s: %s/%s", name, cur, tgt))
	}
	return strings.Join(parts, ", ")
}

func registerPolicyKinds() {
	typed(&Module{GVR: "autoscaling/v2/horizontalpodautoscalers", Kind: "HorizontalPodAutoscaler", Namespaced: true, Short: []string{"hpa"}, GateClass: "hpa.unable",
		Cols: []Col{cCluster(), cName(), cNS(), cStr("reference", "Reference", 220), cStr("targets", "Targets", 220), cInt("minPods", "Min", 56),
			cInt("maxPods", "Max", 56), cInt("replicas", "Replicas", 72), cTone(), cAge()}},
		func(l *autoscalingv2.HorizontalPodAutoscalerList) []autoscalingv2.HorizontalPodAutoscaler {
			return l.Items
		},
		func(h *autoscalingv2.HorizontalPodAutoscaler) *Row {
			r := newRow(&h.ObjectMeta, 5)
			r.putS(h.Spec.ScaleTargetRef.Kind + "/" + h.Spec.ScaleTargetRef.Name)
			r.putS(hpaTargets(h))
			r.putN(int64(i32(h.Spec.MinReplicas, 1)))
			r.putN(int64(h.Spec.MaxReplicas))
			r.putN(int64(h.Status.CurrentReplicas))
			for i := range h.Status.Conditions {
				if c := &h.Status.Conditions[i]; c.Type == autoscalingv2.ScalingActive && c.Status == corev1.ConditionFalse {
					r.tone = ToneDanger
				}
			}
			if r.tone == ToneNeutral && h.Status.CurrentReplicas >= h.Spec.MaxReplicas && h.Spec.MaxReplicas > 0 {
				r.tone = ToneWarning
			}
			setFacts(r, hpaFacts(h))
			return r
		})

	typed(&Module{GVR: "policy/v1/poddisruptionbudgets", Kind: "PodDisruptionBudget", Namespaced: true, Short: []string{"pdb"},
		Cols: []Col{cCluster(), cName(), cNS(), cDict("minAvailable", "Min available", 100), cDict("maxUnavailable", "Max unavailable", 110),
			cInt("allowed", "Allowed disruptions", 130), cInt("healthy", "Healthy", 70), cInt("expected", "Expected", 70), cTone(), cAge()}},
		func(l *policyv1.PodDisruptionBudgetList) []policyv1.PodDisruptionBudget { return l.Items },
		func(p *policyv1.PodDisruptionBudget) *Row {
			r := newRow(&p.ObjectMeta, 5)
			r.putS(intOrStr(p.Spec.MinAvailable))
			r.putS(intOrStr(p.Spec.MaxUnavailable))
			r.putN(int64(p.Status.DisruptionsAllowed))
			r.putN(int64(p.Status.CurrentHealthy))
			r.putN(int64(p.Status.ExpectedPods))
			if p.Status.DisruptionsAllowed == 0 && p.Status.ExpectedPods > 0 {
				r.tone = ToneWarning
			}
			return r
		})
}

// setFacts attaches typed facts only when non-nil (a nil *T stored in an interface is not nil).
func setFacts[T any](r *Row, f *T) {
	if f != nil {
		r.facts = f
	}
}

// eventLastSeenCell is the cell index of the lastSeen column (the rank of an event in an object-events store).
const eventLastSeenCell = 5

// EventLastSeen is an event row's lastSeen in epoch milliseconds.
func EventLastSeen(r *Row) int64 { return r.Cell(eventLastSeenCell).N }

func eventCols() []Col {
	return []Col{cCluster(), cName(), cNS(), cDict("type", "Type", 80), cDict("reason", "Reason", 150), cStr("object", "Object", 260),
		cStr("message", "Message", 480), cInt("count", "Count", 64), cTime("lastSeen", "Last seen", 90), cDict("source", "Source", 140), cTone(), cAge()}
}
