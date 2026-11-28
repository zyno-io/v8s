/**
 * Builds the object shown in the detail drawer (labels, containers, YAML,
 * events, log stream) from a row of any view. All synthetic.
 */
import {
  CLUSTERS, STATUS_TEXT, fmtAge, getFleet, mulberry32, podIp, podName,
  ST_CRASH, ST_DONE, ST_IMGPULL, ST_OOM, type NodeRec, type Problem,
} from './data';
import { getSource, type Tone, type ViewId } from './views';

export interface Subject {
  view: ViewId | 'other';
  idx: number;
  kind: string;
  cl: number;
  ns: string;
  name: string;
  tone: Tone;
  status: string;
  labels: [string, string][];
  containers: { name: string; image: string; ready: boolean; restarts: number; state: string }[];
  conditions: { type: string; ok: boolean; reason: string; since: string }[];
  facts: [string, string][];
  yaml: string[];
  /** Line index of the editable `replicas:` line, or -1. */
  editLine: number;
  replicas: number;
  events: { type: 'Normal' | 'Warning'; reason: string; age: string; msg: string }[];
  logSeed: number;
  logBad: number; // 0..1 chance of warn/error
  problem?: Problem;
  /** Pods: the owning Deployment row, for edit. */
  owner?: { view: ViewId; idx: number };
}

const hash = (s: string) => {
  let h = 2166136261;
  for (let i = 0; i < s.length; i++) h = Math.imul(h ^ s.charCodeAt(i), 16777619);
  return h >>> 0;
};

function podYaml(s: { name: string; ns: string; image: string; labels: [string, string][]; node: string; ip: string; cs: Subject['containers']; ready: boolean }): string[] {
  const y: string[] = ['apiVersion: v1', 'kind: Pod', 'metadata:', `  name: ${s.name}`, `  namespace: ${s.ns}`, '  labels:'];
  for (const [k, v] of s.labels) y.push(`    ${k}: ${v}`);
  y.push('  ownerReferences:', '    - apiVersion: apps/v1', '      kind: ReplicaSet', `      name: ${s.name.replace(/-[a-z0-9]{5}$/, '')}`, '      controller: true');
  y.push('spec:', `  nodeName: ${s.node}`, '  serviceAccountName: default', '  containers:');
  for (const c of s.cs) {
    y.push(`    - name: ${c.name}`, `      image: ${c.image}`, '      resources:', '        requests:', '          cpu: 250m', '          memory: 256Mi', '        limits:', '          memory: 512Mi');
  }
  y.push('status:', `  phase: ${s.ready ? 'Running' : 'Pending'}`, `  podIP: ${s.ip}`, '  containerStatuses:');
  for (const c of s.cs) y.push(`    - name: ${c.name}`, `      ready: ${c.ready}`, `      restartCount: ${c.restarts}`);
  return y;
}

export function subjectFor(view: ViewId, idx: number): Subject {
  const f = getFleet();
  const src = getSource(view);
  const base: Subject = {
    view, idx, kind: '', cl: src.cl(idx), ns: src.ns(idx), name: src.name(idx), tone: src.tone(idx), status: src.status(idx),
    labels: [], containers: [], conditions: [], facts: [], yaml: [], editLine: -1, replicas: 0, events: [], logSeed: hash(src.name(idx)), logBad: 0.05,
  };
  const cname = CLUSTERS[base.cl].id;
  const t = (n: number) => fmtAge(n);

  if (view === 'pods') {
    const w = f.workloads[f.wl[idx]];
    const names = ['app', 'envoy', 'log-shipper'];
    const st = f.st[idx];
    base.name = podName(f, idx);
    base.kind = 'Pod';
    base.labels = [['app', w.name], ['app.kubernetes.io/part-of', w.ns], ['pod-template-hash', w.rs], ['version', `v${w.version}`]];
    base.containers = Array.from({ length: w.containers }, (_, k) => ({
      name: k === 0 ? w.name.length < 20 ? w.name : 'app' : names[k],
      image: k === 0 ? w.image : k === 1 ? 'docker.io/envoyproxy/envoy:v1.31.2' : 'cr.fluentbit.io/fluent/fluent-bit:3.2.2',
      ready: k < f.rdy[idx], restarts: k === 0 ? f.rst[idx] : 0,
      state: st === ST_CRASH ? 'Waiting: CrashLoopBackOff' : st === ST_IMGPULL ? 'Waiting: ImagePullBackOff' : st === ST_OOM ? 'Terminated: OOMKilled' : st === ST_DONE ? 'Terminated: Completed' : k < f.rdy[idx] ? 'Running' : 'Waiting: PodInitializing',
    }));
    const ok = st === 0 && f.rdy[idx] === w.containers;
    base.conditions = [
      { type: 'PodScheduled', ok: st !== 1, reason: st === 1 ? 'Unschedulable' : '', since: t(f.age[idx]) },
      { type: 'Initialized', ok: st !== 1 && st !== 2, reason: '', since: t(f.age[idx]) },
      { type: 'ContainersReady', ok, reason: ok ? '' : STATUS_TEXT[st], since: t(Math.min(f.age[idx], 900)) },
      { type: 'Ready', ok, reason: ok ? '' : 'ContainersNotReady', since: t(Math.min(f.age[idx], 900)) },
    ];
    base.facts = [['Node', f.nodes[f.node[idx]].name], ['Pod IP', podIp(f, idx)], ['Restarts', String(f.rst[idx])], ['Created', `${t(f.age[idx])} ago`], ['Owner', `ReplicaSet/${w.name}-${w.rs}`]];
    base.yaml = podYaml({ name: base.name, ns: base.ns, image: w.image, labels: base.labels, node: f.nodes[f.node[idx]].name, ip: podIp(f, idx), cs: base.containers, ready: ok });
    if (w.dep >= 0) base.owner = { view: 'deploy', idx: w.dep };
    base.logBad = st === ST_CRASH || st === ST_OOM ? 0.55 : 0.05;
    base.events = podEvents(st, base.name, w.image, f.nodes.filter((x) => x.cl === w.cl));
  } else if (view === 'deploy') {
    const w = f.workloads[f.deployments[idx]];
    base.kind = 'Deployment';
    base.replicas = w.replicas;
    base.labels = [['app', w.name], ['app.kubernetes.io/part-of', w.ns], ['tier', w.ns === 'edge' ? 'edge' : 'backend']];
    base.containers = Array.from({ length: w.containers }, (_, k) => ({
      name: k === 0 ? 'app' : k === 1 ? 'envoy' : 'log-shipper', image: k === 0 ? w.image : 'docker.io/envoyproxy/envoy:v1.31.2', ready: true, restarts: 0, state: 'Template',
    }));
    const avail = w.ready >= w.replicas * 0.75;
    base.conditions = [
      { type: 'Available', ok: avail, reason: avail ? 'MinimumReplicasAvailable' : 'MinimumReplicasUnavailable', since: t(w.ageSec) },
      { type: 'Progressing', ok: w.ready >= w.replicas * 0.5, reason: w.ready >= w.replicas * 0.5 ? 'NewReplicaSetAvailable' : 'ProgressDeadlineExceeded', since: t(3600 * 2) },
    ];
    base.facts = [['Replicas', `${w.ready} ready of ${w.replicas}`], ['Strategy', 'RollingUpdate 25% / 25%'], ['Created', `${t(w.ageSec)} ago`], ['Image', w.image]];
    const y = ['apiVersion: apps/v1', 'kind: Deployment', 'metadata:', `  name: ${w.name}`, `  namespace: ${w.ns}`, '  labels:'];
    for (const [k, v] of base.labels) y.push(`    ${k}: ${v}`);
    y.push('  annotations:', `    deployment.kubernetes.io/revision: "${3 + (hash(w.name) % 30)}"`, '    kustomize.toolkit.fluxcd.io/name: apps', 'spec:');
    base.editLine = y.length;
    y.push(`  replicas: ${w.replicas}`, '  revisionHistoryLimit: 5', '  selector:', '    matchLabels:', `      app: ${w.name}`, '  strategy:', '    type: RollingUpdate', '    rollingUpdate:', '      maxSurge: 25%', '      maxUnavailable: 25%', '  template:', '    metadata:', '      labels:', `        app: ${w.name}`, '    spec:', '      containers:');
    for (const c of base.containers) y.push(`        - name: ${c.name}`, `          image: ${c.image}`, '          ports:', '            - containerPort: 8080', '          resources:', '            limits:', '              memory: 512Mi');
    y.push('status:', `  replicas: ${w.replicas}`, `  readyReplicas: ${w.ready}`, `  availableReplicas: ${w.ready}`);
    base.yaml = y;
    base.logBad = w.ready < w.replicas * 0.75 ? 0.4 : 0.05;
    base.events = [
      { type: 'Normal', reason: 'ScalingReplicaSet', age: '2h', msg: `Scaled up replica set ${w.name}-${w.rs} to ${w.replicas}` },
      ...(avail ? [] : [{ type: 'Warning' as const, reason: 'ProgressDeadlineExceeded', age: '18m', msg: `ReplicaSet "${w.name}-${w.rs}" has timed out progressing` }]),
    ];
  } else if (view === 'nodes') {
    const n = f.nodes[idx];
    base.kind = 'Node';
    base.labels = [['kubernetes.io/arch', 'amd64'], ['node.kubernetes.io/instance-type', n.type], ['topology.kubernetes.io/region', CLUSTERS[n.cl].region]];
    base.conditions = [
      { type: 'Ready', ok: n.status !== 'NotReady', reason: n.status === 'NotReady' ? 'KubeletNotReady' : 'KubeletReady', since: '3h' },
      { type: 'MemoryPressure', ok: true, reason: 'KubeletHasSufficientMemory', since: t(n.ageSec) },
      { type: 'DiskPressure', ok: true, reason: 'KubeletHasNoDiskPressure', since: t(n.ageSec) },
    ];
    base.facts = [['Roles', n.role], ['Version', n.version], ['Internal IP', n.ip], ['Pods', String(n.pods)], ['Instance type', n.type]];
    base.yaml = ['apiVersion: v1', 'kind: Node', 'metadata:', `  name: ${n.name}`, '  labels:', ...base.labels.map(([k, v]) => `    ${k}: ${v}`), 'spec:', `  unschedulable: ${n.status === 'Ready,SchedulingDisabled'}`, `  providerID: aws:///${CLUSTERS[n.cl].region}a/i-0${hash(n.name).toString(16)}`, 'status:', '  capacity:', '    cpu: "16"', '    memory: 64Gi', '    pods: "110"', `  nodeInfo:`, `    kubeletVersion: ${n.version}`];
    base.events = n.status === 'NotReady' ? [{ type: 'Warning', reason: 'NodeNotReady', age: '27m', msg: 'Node status is now: NodeNotReady' }] : [{ type: 'Normal', reason: 'NodeReady', age: t(n.ageSec), msg: 'Node status is now: NodeReady' }];
    base.logBad = 0.1;
  } else if (view === 'helm') {
    const h = f.helm[idx];
    base.kind = 'HelmRelease';
    base.labels = [['helm.toolkit.fluxcd.io/name', h.name], ['app.kubernetes.io/managed-by', 'Helm']];
    base.facts = [['Chart', `${h.chart}@${h.version}`], ['App version', h.appVersion], ['Revision', String(h.revision)], ['Message', h.msg]];
    base.conditions = [{ type: 'Ready', ok: h.status === 'deployed', reason: h.status === 'deployed' ? 'UpgradeSucceeded' : 'UpgradeFailed', since: t(h.ageSec) }];
    base.yaml = ['apiVersion: helm.toolkit.fluxcd.io/v2', 'kind: HelmRelease', 'metadata:', `  name: ${h.name}`, `  namespace: ${h.ns}`, 'spec:', '  interval: 10m', '  chart:', '    spec:', `      chart: ${h.chart}`, `      version: ${h.version}`, '      sourceRef:', '        kind: HelmRepository', '        name: charts', 'status:', `  lastReleaseRevision: ${h.revision}`, `  lastAttemptedRevision: ${h.version}`];
    base.events = [{ type: h.status === 'failed' ? 'Warning' : 'Normal', reason: h.status === 'failed' ? 'UpgradeFailed' : 'UpgradeSucceeded', age: t(h.ageSec), msg: h.msg }];
  } else if (view === 'ks') {
    const k = f.ks[idx];
    base.kind = 'Kustomization';
    base.labels = [['kustomize.toolkit.fluxcd.io/name', k.name]];
    base.facts = [['Path', k.path], ['Source', k.source], ['Revision', k.revision], ['Message', k.msg]];
    base.conditions = [{ type: 'Ready', ok: k.status === 'Ready', reason: k.status === 'Ready' ? 'ReconciliationSucceeded' : 'BuildFailed', since: t(k.ageSec) }];
    base.yaml = ['apiVersion: kustomize.toolkit.fluxcd.io/v1', 'kind: Kustomization', 'metadata:', `  name: ${k.name}`, `  namespace: ${k.ns}`, 'spec:', '  interval: 10m', `  path: ${k.path}`, '  prune: true', '  sourceRef:', '    kind: GitRepository', '    name: flux-system', 'status:', `  lastAppliedRevision: ${k.revision}`];
    base.events = [{ type: k.status === 'Ready' ? 'Normal' : 'Warning', reason: k.status === 'Ready' ? 'ReconciliationSucceeded' : 'BuildFailed', age: t(k.ageSec), msg: k.msg }];
  } else {
    const s = f.svc[idx];
    base.kind = 'Service';
    base.labels = [['app', f.workloads[s.wl].name]];
    base.facts = [['Type', s.type], ['Cluster IP', s.clusterIp], ['External IP', s.external || 'none'], ['Ports', s.ports]];
    base.yaml = ['apiVersion: v1', 'kind: Service', 'metadata:', `  name: ${s.name}`, `  namespace: ${s.ns}`, 'spec:', `  type: ${s.type}`, `  clusterIP: ${s.clusterIp}`, '  selector:', `    app: ${f.workloads[s.wl].name}`, '  ports:', `    - port: ${parseInt(s.ports)}`, '      protocol: TCP'];
    base.events = [];
  }
  void cname;
  if (!base.events.length) base.events = [{ type: 'Normal', reason: 'Synced', age: '1h', msg: 'Object reconciled' }];
  return base;
}

/** The scheduler's message for a Pending pod: every node of its cluster, by the reason it was ruled out. */
function unschedulable(nodes: NodeRec[]): string {
  const cp = nodes.filter((n) => n.role === 'control-plane').length;
  const cordoned = nodes.filter((n) => n.status === 'Ready,SchedulingDisabled').length;
  const down = nodes.filter((n) => n.status === 'NotReady').length;
  const parts = [`${nodes.length - cp - cordoned - down} Insufficient memory`];
  if (cp) parts.push(`${cp} node(s) had untolerated taint {node-role.kubernetes.io/control-plane: }`);
  if (down) parts.push(`${down} node(s) had untolerated taint {node.kubernetes.io/unreachable: }`);
  if (cordoned) parts.push(`${cordoned} node(s) were unschedulable`);
  return `0/${nodes.length} nodes are available: ${parts.join(', ')}.`;
}

function podEvents(st: number, name: string, image: string, nodes: NodeRec[]): Subject['events'] {
  const ev: Subject['events'] = [];
  if (st === ST_CRASH) ev.push({ type: 'Warning', reason: 'BackOff', age: '12s', msg: `Back-off restarting failed container in pod ${name}` });
  if (st === ST_IMGPULL) ev.push({ type: 'Warning', reason: 'Failed', age: '40s', msg: `Failed to pull image "${image}": not found` });
  if (st === ST_OOM) ev.push({ type: 'Warning', reason: 'OOMKilling', age: '4m', msg: 'Memory cgroup out of memory: killed process 28113' });
  if (st === 1) ev.push({ type: 'Warning', reason: 'FailedScheduling', age: '3m', msg: unschedulable(nodes) });
  ev.push(
    { type: 'Normal', reason: 'Started', age: '2h', msg: 'Started container app' },
    { type: 'Normal', reason: 'Created', age: '2h', msg: 'Created container app' },
    { type: 'Normal', reason: 'Pulled', age: '2h', msg: `Container image "${image}" already present on machine` },
    { type: 'Normal', reason: 'Scheduled', age: '2h', msg: `Successfully assigned to node` },
  );
  return ev;
}

export function subjectForProblem(p: Problem): Subject {
  if (p.ref) {
    const s = subjectFor(p.ref.view, p.ref.idx);
    s.problem = p;
    return s;
  }
  const s: Subject = {
    view: 'other', idx: -1, kind: p.kind, cl: p.cl, ns: p.ns, name: p.object, tone: p.sev === 3 ? 'info' : p.sev === 2 ? 'warning' : 'danger',
    status: p.cls, labels: [['app', 'demo']], containers: [], conditions: [{ type: 'Healthy', ok: false, reason: p.cls, since: fmtAge(p.ageSec) }],
    facts: [['Class', p.cls], ['Family', p.family]],
    yaml: ['apiVersion: v1', `kind: ${p.kind}`, 'metadata:', `  name: ${p.object}`, ...(p.ns ? [`  namespace: ${p.ns}`] : []), 'status:', `  message: "${p.message.replace(/"/g, "'")}"`],
    editLine: -1, replicas: 0, events: [{ type: 'Warning', reason: p.cls, age: fmtAge(p.ageSec), msg: p.message }], logSeed: hash(p.object), logBad: 0.2, problem: p,
  };
  return s;
}

// ---- logs -----------------------------------------------------------------------
const MSGS: Record<string, string[]> = {
  payments: ['authorize request accepted', 'ledger entry committed', 'idempotency key replayed', 'settlement window opened', 'card tokenized'],
  shop: ['cart updated', 'checkout session created', 'catalog page rendered', 'price lookup', 'inventory reserved'],
  search: ['query executed', 'shard refreshed', 'bulk index batch applied', 'suggest cache hit'],
  data: ['batch committed', 'offset advanced', 'consumer lag sampled', 'schema resolved', 'partition rebalanced'],
  edge: ['request routed', 'rate limit evaluated', 'upstream selected', 'token validated'],
  billing: ['invoice rendered', 'usage record flushed', 'tax computed'],
};
export interface LogLine { ts: string; level: 'info' | 'warn' | 'error' | 'debug'; text: string }

export function makeLogger(s: Subject): () => LogLine {
  const rng = mulberry32(s.logSeed);
  let clock = Date.UTC(2026, 10, 17, 10, 42, 0) + Math.floor(rng() * 1000);
  const msgs = MSGS[s.ns] ?? ['reconcile complete', 'health check ok', 'watch event processed', 'cache refreshed'];
  const bad = s.logBad;
  return () => {
    clock += 200 + Math.floor(rng() * 900);
    const r = rng();
    const level: LogLine['level'] = r < bad * 0.45 ? 'error' : r < bad ? 'warn' : r < 0.18 ? 'debug' : 'info';
    const id = Math.floor(rng() * 0xffffff).toString(16).padStart(6, '0');
    const dur = Math.floor(2 + rng() * rng() * 400);
    let text: string;
    if (level === 'error') {
      const e = ['connection refused: dial tcp 10.100.12.44:5432', 'context deadline exceeded after 5000ms', 'panic recovered: nil pointer dereference', 'upstream returned 503'][Math.floor(rng() * 4)];
      text = `{"level":"error","msg":"${e}","trace_id":"${id}"}`;
    } else if (level === 'warn') {
      text = `{"level":"warn","msg":"retrying after transient error","attempt":${1 + Math.floor(rng() * 4)},"trace_id":"${id}"}`;
    } else {
      const m = msgs[Math.floor(rng() * msgs.length)];
      text = `{"level":"${level}","msg":"${m}","status":${rng() < 0.9 ? 200 : 201},"dur_ms":${dur},"trace_id":"${id}"}`;
    }
    return { ts: new Date(clock).toISOString().slice(0, 23) + 'Z', level, text };
  };
}
