/**
 * Synthetic fleet for the demo: three clusters, ~50,000 pods in typed arrays,
 * plus deployments, nodes, Helm releases, Kustomizations, services and the
 * problems derived from them. Seeded, so every load (and the server render)
 * is identical. Nothing here touches the network.
 */

export function mulberry32(seed: number): () => number {
  let a = seed >>> 0;
  return () => {
    a = (a + 0x6d2b79f5) >>> 0;
    let t = a;
    t = Math.imul(t ^ (t >>> 15), t | 1);
    t ^= t + Math.imul(t ^ (t >>> 7), t | 61);
    return ((t ^ (t >>> 14)) >>> 0) / 4294967296;
  };
}

export interface Cluster {
  id: string;
  group: 'production' | 'staging';
  region: string;
  octet: number;
}

export const CLUSTERS: Cluster[] = [
  { id: 'prod-us', group: 'production', region: 'us-east-1', octet: 10 },
  { id: 'prod-eu', group: 'production', region: 'eu-west-1', octet: 20 },
  { id: 'staging', group: 'staging', region: 'us-east-1', octet: 30 },
];
export const GROUPS = ['production', 'staging'] as const;

export const NAMESPACES = [
  'payments', 'shop', 'search', 'data', 'edge', 'billing', 'logging', 'monitoring',
  'ingress-nginx', 'cert-manager', 'flux-system', 'kube-system', 'kyverno', 'trivy-system',
];

// Pod statuses.
export const ST_RUNNING = 0, ST_PENDING = 1, ST_CREATING = 2, ST_CRASH = 3, ST_IMGPULL = 4,
  ST_OOM = 5, ST_DONE = 6, ST_TERM = 7, ST_ERROR = 8;
export const STATUS_TEXT = [
  'Running', 'Pending', 'ContainerCreating', 'CrashLoopBackOff', 'ImagePullBackOff',
  'OOMKilled', 'Completed', 'Terminating', 'Error',
];

export type WlKind = 'Deployment' | 'StatefulSet' | 'DaemonSet' | 'CronJob';

export interface Workload {
  id: number;
  cl: number;
  ns: string;
  name: string;
  kind: WlKind;
  containers: number;
  replicas: number;
  rs: string;
  image: string;
  ageSec: number;
  first: number; // first pod index
  ready: number; // pods Running and fully ready
  version: string;
  /** Deployment ordinal within deployments (for the Deployments view), or -1. */
  dep: number;
}

export interface NodeRec {
  id: number;
  cl: number;
  name: string;
  status: 'Ready' | 'NotReady' | 'Ready,SchedulingDisabled';
  role: string;
  version: string;
  cpu: number;
  mem: number;
  ageSec: number;
  ip: string;
  pods: number;
  type: string;
}

export interface HelmRec {
  cl: number; ns: string; name: string; chart: string; version: string; appVersion: string;
  status: 'deployed' | 'failed' | 'pending-upgrade'; revision: number; ageSec: number; msg: string;
}
export interface KsRec {
  cl: number; ns: string; name: string; path: string; source: string; revision: string;
  status: 'Ready' | 'Not ready' | 'Reconciling'; ageSec: number; msg: string;
}
export interface SvcRec {
  cl: number; ns: string; name: string; type: string; clusterIp: string; external: string;
  ports: string; ageSec: number; wl: number;
}

export interface Problem {
  id: number;
  sev: 0 | 1 | 2 | 3; // critical, error, warning, info
  family: string;
  cl: number;
  ns: string;
  kind: string;
  object: string;
  cls: string;
  message: string;
  ageSec: number;
  /** Row this problem points at, if any. */
  ref?: { view: 'deploy' | 'nodes' | 'helm' | 'ks' | 'pods'; idx: number };
}
export const SEVERITY = ['critical', 'error', 'warning', 'info'] as const;
export const FAMILIES = ['Workloads', 'Nodes', 'Flux', 'Helm', 'Storage', 'Certificates', 'Policy', 'Security'];

export interface Fleet {
  podCount: number;
  wl: Uint16Array;
  cl: Uint8Array;
  st: Uint8Array;
  rdy: Uint8Array;
  rst: Uint16Array;
  node: Uint16Array;
  age: Uint32Array;
  sfx: Uint32Array;
  ipl: Uint8Array;
  workloads: Workload[];
  deployments: number[]; // workload ids
  nodes: NodeRec[];
  helm: HelmRec[];
  ks: KsRec[];
  svc: SvcRec[];
  problems: Problem[];
  stCounts: Uint16Array; // workloads * 9
}

type Kind = 'D' | 'S' | 'N' | 'C';
// [ns, name, kind, containers, weight, fixed replicas]
type Cat = [string, string, Kind, number, number, number?];

const BASE: Cat[] = [
  ['payments', 'payments-api', 'D', 2, 6],
  ['payments', 'ledger', 'D', 2, 5],
  ['payments', 'ledger-reconciler', 'D', 1, 0, 20],
  ['payments', 'payments-gateway', 'D', 2, 5],
  ['payments', 'fraud-scorer', 'D', 2, 4],
  ['payments', 'refunds-worker', 'D', 1, 3],
  ['payments', 'webhook-dispatcher', 'D', 1, 4],
  ['payments', 'settlement-batch', 'C', 1, 0, 6],
  ['payments', 'postgres-ledger', 'S', 2, 0, 3],
  ['shop', 'checkout', 'D', 2, 6],
  ['shop', 'cart', 'D', 2, 6],
  ['shop', 'catalog', 'D', 2, 0, 12],
  ['shop', 'storefront', 'D', 3, 10],
  ['shop', 'recommendations', 'D', 1, 0, 8],
  ['shop', 'pricing', 'D', 1, 3],
  ['shop', 'inventory-sync', 'D', 1, 3],
  ['shop', 'image-resizer', 'D', 1, 5],
  ['shop', 'session-redis', 'S', 2, 0, 3],
  ['shop', 'feature-flags', 'D', 1, 2],
  ['shop', 'notifier', 'D', 1, 3],
  ['search', 'search-api', 'D', 2, 6],
  ['search', 'search-indexer', 'D', 1, 8],
  ['search', 'search-query-router', 'D', 2, 4],
  ['search', 'suggest', 'D', 1, 2],
  ['search', 'opensearch-data', 'S', 1, 0, 5],
  ['data', 'events-consumer', 'D', 2, 14],
  ['data', 'events-producer', 'D', 1, 4],
  ['data', 'etl-runner', 'D', 1, 6],
  ['data', 'schema-registry', 'D', 1, 2],
  ['data', 'stream-processor', 'D', 2, 10],
  ['data', 'kafka', 'S', 2, 0, 5],
  ['data', 'clickhouse', 'S', 1, 0, 4],
  ['data', 'warehouse-loader', 'C', 1, 0, 8],
  ['edge', 'edge-gateway', 'D', 2, 6],
  ['edge', 'rate-limiter', 'D', 1, 3],
  ['edge', 'geo-router', 'D', 1, 2],
  ['edge', 'cdn-purger', 'D', 1, 1],
  ['edge', 'auth-proxy', 'D', 2, 5],
  ['billing', 'billing-api', 'D', 2, 3],
  ['billing', 'invoice-renderer', 'D', 1, 4],
  ['billing', 'usage-meter', 'D', 1, 5],
  ['billing', 'dunning', 'D', 1, 1],
  ['billing', 'tax-calc', 'D', 1, 2],
  ['billing', 'mailer', 'D', 1, 2],
  ['billing', 'billing-export', 'C', 1, 0, 6],
  ['logging', 'fluent-bit', 'N', 1, 0],
  ['logging', 'loki-write', 'D', 1, 4],
  ['logging', 'loki-read', 'D', 1, 3],
  ['logging', 'loki-backend', 'S', 2, 0, 3],
  ['logging', 'log-archiver', 'C', 1, 0, 4],
  ['monitoring', 'prometheus', 'S', 2, 0, 2],
  ['monitoring', 'grafana', 'D', 3, 0, 2],
  ['monitoring', 'alertmanager', 'S', 2, 0, 3],
  ['monitoring', 'kube-state-metrics', 'D', 1, 0, 1],
  ['monitoring', 'node-exporter', 'N', 1, 0],
  ['monitoring', 'blackbox-exporter', 'D', 1, 0, 2],
  ['monitoring', 'thanos-query', 'D', 1, 0, 2],
  ['ingress-nginx', 'ingress-nginx-controller', 'D', 1, 0, 6],
  ['cert-manager', 'cert-manager', 'D', 1, 0, 1],
  ['cert-manager', 'cert-manager-cainjector', 'D', 1, 0, 1],
  ['cert-manager', 'cert-manager-webhook', 'D', 1, 0, 2],
  ['flux-system', 'source-controller', 'D', 1, 0, 1],
  ['flux-system', 'kustomize-controller', 'D', 1, 0, 1],
  ['flux-system', 'helm-controller', 'D', 1, 0, 1],
  ['flux-system', 'notification-controller', 'D', 1, 0, 1],
  ['kube-system', 'coredns', 'D', 1, 0, 4],
  ['kube-system', 'metrics-server', 'D', 1, 0, 2],
  ['kube-system', 'kube-proxy', 'N', 1, 0],
  ['kube-system', 'aws-node', 'N', 2, 0],
  ['kube-system', 'ebs-csi-controller', 'D', 3, 0, 2],
  ['kube-system', 'ebs-csi-node', 'N', 3, 0],
  ['kube-system', 'cluster-autoscaler', 'D', 1, 0, 1],
  ['kyverno', 'kyverno-admission-controller', 'D', 1, 0, 3],
  ['kyverno', 'kyverno-background-controller', 'D', 1, 0, 2],
  ['kyverno', 'kyverno-reports-controller', 'D', 1, 0, 1],
  ['trivy-system', 'trivy-operator', 'D', 1, 0, 1],
];

function extras(): Cat[] {
  const out: Cat[] = [];
  for (const x of ['adyen', 'stripe', 'worldpay', 'braintree', 'klarna', 'paypal', 'checkoutcom', 'razorpay'])
    out.push(['payments', `psp-${x}-adapter`, 'D', 2, 3]);
  for (const x of ['en-us', 'en-gb', 'de-de', 'fr-fr', 'es-es', 'it-it', 'ja-jp', 'pt-br', 'nl-nl', 'sv-se'])
    out.push(['shop', `storefront-${x}`, 'D', 2, 3]);
  for (const x of ['salesforce', 'hubspot', 'stripe', 'shopify', 'zendesk', 'netsuite', 'segment', 'mixpanel', 'snowflake', 'bigquery', 'intercom', 'jira'])
    out.push(['data', `etl-${x}`, 'D', 1, 2.5]);
  for (const x of ['iad', 'sfo', 'fra', 'ams', 'sin', 'syd', 'nrt', 'gru', 'jnb', 'bom'])
    out.push(['edge', `edge-${x}-proxy`, 'D', 2, 2]);
  for (let i = 0; i < 16; i++) out.push(['search', `search-shard-${String(i).padStart(2, '0')}`, 'D', 1, 4]);
  for (const x of ['acme', 'globex', 'initech', 'umbrella', 'hooli', 'stark', 'wayne', 'wonka', 'cyberdyne', 'soylent', 'tyrell', 'aperture', 'vandelay', 'pied-piper', 'massive', 'oscorp', 'gringotts', 'dunder', 'bluth', 'prestige'])
    out.push(['data', `tenant-${x}-worker`, 'D', 1, 2]);
  return out;
}

const CLUSTER_SCALE = [1, 0.7, 0.1];
const TARGET_PODS = 50000;
const ALPHA = 'bcdfghjklmnpqrstvwxz2456789';

export function suffix5(seed: number): string {
  let s = '';
  let v = seed >>> 0;
  for (let i = 0; i < 5; i++) {
    s += ALPHA[v % 27];
    v = Math.floor(v / 27);
  }
  return s;
}

// Troubled workloads: key "cluster/name" -> [status, fraction of pods].
const TROUBLE: Record<string, [number, number]> = {
  'prod-eu/ledger-reconciler': [ST_CRASH, 0.7],
  'prod-us/image-resizer': [ST_OOM, 0.08],
  'prod-us/recommendations': [ST_IMGPULL, 0.4],
  'prod-us/catalog': [ST_CREATING, 0.3],
  'staging/webhook-dispatcher': [ST_CRASH, 0.5],
  'prod-us/settlement-batch': [ST_ERROR, 0.34],
};

let cached: Fleet | null = null;

export function getFleet(): Fleet {
  if (cached) return cached;
  const rng = mulberry32(0x5eed8);
  const rs = () => {
    let s = '';
    for (let i = 0; i < 9 + (rng() < 0.5 ? 1 : 0); i++) s += ALPHA[Math.floor(rng() * 27)];
    return s;
  };

  // --- nodes ---------------------------------------------------------------
  // Sized for 60-150 pods per worker (EKS allows 234 on these instance types). The production clusters also have
  // three small control-plane nodes, tainted, so they only run DaemonSet pods.
  const nodes: NodeRec[] = [];
  const nodeCount = [180, 150, 48];
  const clusterNodes: number[][] = [[], [], []];
  for (let c = 0; c < 3; c++) {
    for (let k = 0; k < nodeCount[c]; k++) {
      const cp = k < 3 && c < 2;
      const o2 = CLUSTERS[c].octet + 1;
      const o4 = 10 + Math.floor(rng() * 200);
      const name =
        c === 0 ? `ip-10-${o2}-${k}-${o4}.ec2.internal`
        : c === 1 ? `ip-10-${o2}-${k}-${o4}.eu-west-1.compute.internal`
        : `stg-pool-${'abcdefgh'[k % 8]}${suffix5(k * 104729 + 17).slice(0, 4)}`;
      let status: NodeRec['status'] = 'Ready';
      if (c === 1 && k === 7) status = 'NotReady';
      if (c === 2 && k === 5) status = 'NotReady';
      if (c === 0 && k === 11) status = 'Ready,SchedulingDisabled';
      const id = nodes.length;
      nodes.push({
        id, cl: c, name, status, role: cp ? 'control-plane' : 'worker',
        version: c === 2 ? 'v1.32.0' : 'v1.31.4',
        cpu: status === 'NotReady' ? 0 : 22 + Math.floor(rng() * 58),
        mem: status === 'NotReady' ? 0 : 30 + Math.floor(rng() * 55),
        ageSec: (30 + Math.floor(rng() * 300)) * 86400,
        ip: `10.${o2}.${k}.${o4}`,
        pods: 0,
        type: c === 2 ? 'm6i.4xlarge' : cp ? 'm6i.large' : (c === 0 ? ['c6i.8xlarge', 'c7i.8xlarge', 'c6a.8xlarge'] : ['m6i.8xlarge', 'm7i.8xlarge', 'm6a.8xlarge'])[k % 3],
      });
      clusterNodes[c].push(id);
    }
  }

  // --- workloads -------------------------------------------------------------
  const all = BASE.concat(extras());
  const workloads: Workload[] = [];
  const weights: number[] = [];
  const fixed: number[] = [];
  for (let c = 0; c < 3; c++) {
    for (const [ns, name, kind, containers, w, fx] of all) {
      const id = workloads.length;
      const k: WlKind = kind === 'D' ? 'Deployment' : kind === 'S' ? 'StatefulSet' : kind === 'N' ? 'DaemonSet' : 'CronJob';
      const ver = `${1 + Math.floor(rng() * 3)}.${Math.floor(rng() * 40)}.${Math.floor(rng() * 12)}`;
      const work: Workload = {
        id, cl: c, ns, name, kind: k, containers, replicas: 0, rs: k === 'CronJob' ? String(29390000 + Math.floor(rng() * 9999)) : rs(),
        image: `ghcr.io/acme-shop/${name}:${ver}`, ageSec: (20 + Math.floor(rng() * 380)) * 86400,
        first: 0, ready: 0, version: ver, dep: -1,
      };
      workloads.push(work);
      const jitter = 0.8 + rng() * 0.4;
      if (k === 'DaemonSet') {
        weights.push(0);
        fixed.push(clusterNodes[c].length - (clusterNodes[c].length > 12 ? 3 : 0));
      } else if (w > 0) {
        weights.push(w * CLUSTER_SCALE[c] * jitter);
        fixed.push(0);
      } else {
        weights.push(0);
        fixed.push(Math.max(1, Math.round((fx ?? 1) * (c === 2 && k === 'Deployment' && fx && fx > 3 ? 0.5 : 1))));
      }
    }
  }
  const fixedTotal = fixed.reduce((a, b) => a + b, 0);
  const wTotal = weights.reduce((a, b) => a + b, 0);
  const unit = (TARGET_PODS - fixedTotal) / wTotal;
  const deployments: number[] = [];
  let total = 0;
  for (const w of workloads) {
    w.replicas = fixed[w.id] > 0 ? fixed[w.id] : Math.max(2, Math.round(weights[w.id] * unit));
    // Troubled deployments keep their authored size.
    w.first = total;
    total += w.replicas;
    if (w.kind === 'Deployment') {
      w.dep = deployments.length;
      deployments.push(w.id);
    }
  }

  // --- pods ------------------------------------------------------------------
  const wl = new Uint16Array(total);
  const cl = new Uint8Array(total);
  const st = new Uint8Array(total);
  const rdy = new Uint8Array(total);
  const rst = new Uint16Array(total);
  const node = new Uint16Array(total);
  const age = new Uint32Array(total);
  const sfx = new Uint32Array(total);
  const ipl = new Uint8Array(total);
  const stCounts = new Uint16Array(workloads.length * 9);

  for (const w of workloads) {
    const cn = clusterNodes[w.cl];
    const healthy = cn.filter((id) => nodes[id].status !== 'NotReady' && nodes[id].role === 'worker');
    const notReady = cn.filter((id) => nodes[id].status === 'NotReady');
    const trouble = TROUBLE[`${CLUSTERS[w.cl].id}/${w.name}`];
    const isJob = w.kind === 'CronJob';
    for (let k = 0; k < w.replicas; k++) {
      const i = w.first + k;
      wl[i] = w.id;
      cl[i] = w.cl;
      const r = rng();
      let s: number;
      if (isJob) {
        s = r < (trouble && trouble[0] === ST_ERROR ? 1 - trouble[1] : 0.86) ? ST_DONE : r < 0.93 ? ST_RUNNING : ST_ERROR;
        if (trouble && r >= 1 - trouble[1]) s = ST_ERROR;
      } else if (trouble && r < trouble[1]) {
        s = trouble[0];
      } else if (w.kind === 'DaemonSet' || w.kind === 'StatefulSet') {
        s = r > 0.995 ? ST_PENDING : ST_RUNNING;
      } else if (r < 0.9515) s = ST_RUNNING;
      else if (r < 0.9575) s = ST_PENDING;
      else if (r < 0.9635) s = ST_CREATING;
      else if (r < 0.9665) s = ST_CRASH;
      else if (r < 0.9685) s = ST_IMGPULL;
      else if (r < 0.9695) s = ST_OOM;
      else if (r < 0.9715) s = ST_DONE;
      else if (r < 0.9755) s = ST_TERM;
      else s = ST_RUNNING;
      if (w.cl === 2 && s === ST_TERM) s = ST_RUNNING;
      st[i] = s;
      stCounts[w.id * 9 + s]++;
      const c = w.containers;
      rdy[i] =
        s === ST_RUNNING ? (rng() < 0.015 ? Math.max(0, c - 1) : c)
        : s === ST_TERM ? c
        : s === ST_CRASH || s === ST_OOM ? Math.floor(rng() * c * 0.99)
        : 0;
      if (s === ST_RUNNING && rdy[i] === c) stCountsReady(w);
      rst[i] =
        s === ST_CRASH ? 6 + Math.floor(rng() * 110)
        : s === ST_OOM ? 2 + Math.floor(rng() * 9)
        : rng() < 0.07 ? 1 + Math.floor(rng() * 3)
        : 0;
      const pool = s === ST_TERM && notReady.length ? notReady : healthy;
      const nid = pool[Math.floor(rng() * pool.length)];
      node[i] = w.kind === 'DaemonSet' ? cn[k % cn.length] : nid;
      nodes[node[i]].pods++;
      const young = s !== ST_RUNNING ? 0.02 : 1;
      age[i] = Math.min(w.ageSec, 120 + Math.floor(rng() * rng() * 21 * 86400 * young));
      sfx[i] = w.kind === 'StatefulSet' ? k : Math.floor(rng() * 14348907);
      ipl[i] = 2 + Math.floor(rng() * 250);
    }
  }
  function stCountsReady(w: Workload) {
    w.ready++;
  }

  // --- helm / flux / services ----------------------------------------------
  const charts: [string, string, string, string, string][] = [
    ['ingress-nginx', 'ingress-nginx', 'ingress-nginx', '4.11.3', '1.11.3'],
    ['cert-manager', 'cert-manager', 'cert-manager', 'v1.16.2', 'v1.16.2'],
    ['kube-prometheus-stack', 'monitoring', 'kube-prometheus-stack', '66.2.1', 'v0.78.2'],
    ['grafana', 'monitoring', 'grafana', '8.6.4', '11.3.1'],
    ['loki', 'logging', 'loki', '6.22.0', '3.3.0'],
    ['fluent-bit', 'logging', 'fluent-bit', '0.47.10', '3.2.2'],
    ['external-dns', 'kube-system', 'external-dns', '1.15.0', '0.15.0'],
    ['kyverno', 'kyverno', 'kyverno', '3.3.4', 'v1.13.4'],
    ['trivy-operator', 'trivy-system', 'trivy-operator', '0.24.1', '0.22.0'],
    ['metrics-server', 'kube-system', 'metrics-server', '3.12.2', '0.7.2'],
    ['reloader', 'kube-system', 'reloader', '1.2.0', 'v1.2.0'],
    ['keda', 'keda', 'keda', '2.16.0', '2.16.0'],
    ['redis', 'shop', 'redis', '20.3.0', '7.4.1'],
    ['postgresql', 'payments', 'postgresql', '16.2.3', '17.2.0'],
    ['cluster-autoscaler', 'kube-system', 'cluster-autoscaler', '9.43.2', '1.31.0'],
    ['aws-ebs-csi-driver', 'kube-system', 'aws-ebs-csi-driver', '2.37.0', '1.37.0'],
  ];
  const helm: HelmRec[] = [];
  for (let c = 0; c < 3; c++) {
    charts.forEach(([name, ns, chart, v, av], k) => {
      if (c === 2 && k % 2 === 1) return;
      let status: HelmRec['status'] = 'deployed';
      let msg = `Upgrade complete`;
      if (c === 1 && name === 'keda') {
        status = 'failed';
        msg = 'Helm upgrade failed: timed out waiting for the condition';
      }
      if (c === 0 && name === 'grafana') status = 'pending-upgrade';
      helm.push({ cl: c, ns, name, chart, version: v, appVersion: av, status, revision: 3 + Math.floor(rng() * 38), ageSec: Math.floor(rng() * rng() * 60 * 86400) + 3600, msg });
    });
  }
  const ksNames: [string, string, string][] = [
    ['flux-system', 'flux-system', './clusters'],
    ['infrastructure', 'flux-system', './infrastructure'],
    ['apps', 'flux-system', './apps'],
    ['apps-payments', 'payments', './apps/payments'],
    ['apps-shop', 'shop', './apps/shop'],
    ['apps-search', 'search', './apps/search'],
    ['apps-data', 'data', './apps/data'],
    ['apps-edge', 'edge', './apps/edge'],
    ['monitoring', 'monitoring', './infrastructure/monitoring'],
    ['policies', 'kyverno', './infrastructure/policies'],
  ];
  const ks: KsRec[] = [];
  for (let c = 0; c < 3; c++) {
    for (const [name, ns, p] of ksNames) {
      let status: KsRec['status'] = 'Ready';
      let msg = 'Applied revision';
      if (c === 1 && name === 'apps-search') {
        status = 'Not ready';
        msg = 'kustomize build failed: accumulating resources: search/shard-12.yaml: no such file';
      }
      if (c === 2 && name === 'apps-edge') status = 'Reconciling';
      const sha = Math.floor(rng() * 0xfffffff).toString(16).padStart(7, '0');
      ks.push({ cl: c, ns, name, path: p.replace('clusters', `clusters/${CLUSTERS[c].id}`), source: 'GitRepository/flux-system', revision: `main@sha1:${sha}`, status, ageSec: Math.floor(rng() * 5400) + 30, msg });
    }
  }
  const svc: SvcRec[] = [];
  for (const w of workloads) {
    if (w.kind === 'CronJob') continue;
    const octet = CLUSTERS[w.cl].octet;
    const lb = w.name === 'ingress-nginx-controller' || w.name === 'edge-gateway';
    const headless = w.kind === 'StatefulSet';
    const port = [80, 443, 8080, 9090, 5432, 6379, 9200, 8443][Math.floor(rng() * 8)];
    svc.push({
      cl: w.cl, ns: w.ns, name: w.name + (headless ? '-headless' : ''),
      type: lb ? 'LoadBalancer' : 'ClusterIP',
      clusterIp: headless ? 'None' : `10.${octet + 100}.${Math.floor(rng() * 250)}.${Math.floor(rng() * 250)}`,
      external: lb ? (w.cl === 2 && w.name === 'edge-gateway' ? '<pending>' : `a${Math.floor(rng() * 1e9).toString(16)}.elb.${CLUSTERS[w.cl].region}.amazonaws.com`) : '',
      ports: `${port}/TCP`, ageSec: w.ageSec, wl: w.id,
    });
  }

  const fleet: Fleet = {
    podCount: total, wl, cl, st, rdy, rst, node, age, sfx, ipl, workloads, deployments, nodes, helm, ks, svc,
    problems: [], stCounts,
  };
  fleet.problems = buildProblems(fleet);
  cached = fleet;
  return fleet;
}

function terminating(f: Fleet, nodeId: number): number {
  let n = 0;
  for (let i = 0; i < f.podCount; i++) if (f.node[i] === nodeId && f.st[i] === ST_TERM) n++;
  return n;
}

function find(f: Fleet, cluster: string, name: string): Workload {
  const ci = CLUSTERS.findIndex((c) => c.id === cluster);
  return f.workloads.find((w) => w.cl === ci && w.name === name)!;
}

function buildProblems(f: Fleet): Problem[] {
  const out: Problem[] = [];
  const cnt = (w: Workload, s: number) => f.stCounts[w.id * 9 + s];
  const add = (p: Omit<Problem, 'id'>) => out.push({ ...p, id: out.length + 1 });

  const rec = find(f, 'prod-eu', 'ledger-reconciler');
  add({ sev: 1, family: 'Workloads', cl: rec.cl, ns: rec.ns, kind: 'Deployment', object: rec.name, cls: 'pod.crashloop',
    message: `${cnt(rec, ST_CRASH)} of ${rec.replicas} pods in CrashLoopBackOff: back-off 5m0s restarting failed container=reconciler`, ageSec: 3 * 3600 + 410, ref: { view: 'deploy', idx: rec.dep } });
  const wh = find(f, 'staging', 'webhook-dispatcher');
  add({ sev: 2, family: 'Workloads', cl: wh.cl, ns: wh.ns, kind: 'Deployment', object: wh.name, cls: 'pod.crashloop',
    message: `${cnt(wh, ST_CRASH)} of ${wh.replicas} pods in CrashLoopBackOff: exit code 1, failed to dial queue.staging.svc:5672`, ageSec: 52 * 60, ref: { view: 'deploy', idx: wh.dep } });
  const img = find(f, 'prod-us', 'image-resizer');
  add({ sev: 2, family: 'Workloads', cl: img.cl, ns: img.ns, kind: 'Deployment', object: img.name, cls: 'pod.oomkilled',
    message: `${cnt(img, ST_OOM)} pods OOMKilled in the last hour; memory limit 512Mi, peak 511Mi`, ageSec: 41 * 60, ref: { view: 'deploy', idx: img.dep } });
  const rcm = find(f, 'prod-us', 'recommendations');
  add({ sev: 1, family: 'Workloads', cl: rcm.cl, ns: rcm.ns, kind: 'Deployment', object: rcm.name, cls: 'pod.image-pull',
    message: `${cnt(rcm, ST_IMGPULL)} of ${rcm.replicas} pods in ImagePullBackOff: manifest for ghcr.io/acme-shop/recommendations:1.9.3 not found`, ageSec: 2 * 3600 + 15 * 60, ref: { view: 'deploy', idx: rcm.dep } });
  const set = find(f, 'prod-us', 'settlement-batch');
  add({ sev: 1, family: 'Workloads', cl: set.cl, ns: set.ns, kind: 'Job', object: `settlement-batch-${set.rs}`, cls: 'job.failed',
    message: 'Job has reached the specified backoff limit (BackoffLimitExceeded)', ageSec: 9 * 3600, ref: undefined });
  const cat = find(f, 'prod-us', 'catalog');
  add({ sev: 2, family: 'Workloads', cl: cat.cl, ns: cat.ns, kind: 'Deployment', object: cat.name, cls: 'rollout.stuck',
    message: `Rollout stalled for 18m: ${cnt(cat, ST_CREATING)} of ${cat.replicas} new pods not ready (ProgressDeadlineExceeded)`, ageSec: 18 * 60, ref: { view: 'deploy', idx: cat.dep } });
  const nr = f.nodes.find((n) => n.status === 'NotReady' && n.cl === 1)!;
  add({ sev: 0, family: 'Nodes', cl: nr.cl, ns: '', kind: 'Node', object: nr.name, cls: 'node.not-ready',
    message: `Kubelet stopped posting node status; ${terminating(f, nr.id)} pods Terminating on this node`, ageSec: 27 * 60, ref: { view: 'nodes', idx: nr.id } });
  const nr2 = f.nodes.find((n) => n.status === 'NotReady' && n.cl === 2)!;
  add({ sev: 1, family: 'Nodes', cl: nr2.cl, ns: '', kind: 'Node', object: nr2.name, cls: 'node.not-ready',
    message: 'NodeStatusUnknown: kubelet has no recent heartbeat', ageSec: 6 * 60, ref: { view: 'nodes', idx: nr2.id } });
  const cord = f.nodes.find((n) => n.status === 'Ready,SchedulingDisabled')!;
  add({ sev: 3, family: 'Nodes', cl: cord.cl, ns: '', kind: 'Node', object: cord.name, cls: 'node.cordoned',
    message: 'Unschedulable for 3d; drain not in progress', ageSec: 3 * 86400, ref: { view: 'nodes', idx: cord.id } });
  const ksi = f.ks.findIndex((k) => k.status === 'Not ready');
  const kr = f.ks[ksi];
  add({ sev: 1, family: 'Flux', cl: kr.cl, ns: kr.ns, kind: 'Kustomization', object: kr.name, cls: 'flux.not-ready',
    message: `Ready=False: ${kr.msg}`, ageSec: 5 * 3600, ref: { view: 'ks', idx: ksi } });
  const hi = f.helm.findIndex((h) => h.status === 'failed');
  const hr = f.helm[hi];
  add({ sev: 1, family: 'Helm', cl: hr.cl, ns: hr.ns, kind: 'HelmRelease', object: hr.name, cls: 'helm.failed',
    message: hr.msg, ageSec: 14 * 3600, ref: { view: 'helm', idx: hi } });
  add({ sev: 2, family: 'Storage', cl: 0, ns: 'data', kind: 'PersistentVolumeClaim', object: 'data-clickhouse-3', cls: 'pvc.pending',
    message: 'Pending for 22m: waiting for first consumer; no persistent volumes available (storageclass gp3)', ageSec: 22 * 60 });
  add({ sev: 2, family: 'Certificates', cl: 1, ns: 'payments', kind: 'Certificate', object: 'payments-tls', cls: 'cert.expiring',
    message: 'Expires in 6 days (2026-11-23); renewal failing: ACME challenge returned 403', ageSec: 4 * 86400 });
  add({ sev: 2, family: 'Policy', cl: 2, ns: 'shop', kind: 'Pod', object: 'cart-7d9f4b6c8-x7k2q', cls: 'policy.fail',
    message: 'Kyverno require-resource-limits: container "cart" must set resources.limits.memory', ageSec: 2 * 86400 });
  add({ sev: 0, family: 'Security', cl: 0, ns: 'search', kind: 'Image', object: 'ghcr.io/acme-shop/search-api:1.30.2', cls: 'vuln.critical',
    message: 'CVE-2024-45337 golang.org/x/crypto 0.28.0 (fixed in 0.31.0); 2 critical, 9 high', ageSec: 11 * 3600 });
  out.sort((a, b) => a.sev - b.sev || a.ageSec - b.ageSec);
  out.forEach((p, i) => (p.id = i + 1));
  return out;
}

export function podName(f: Fleet, i: number): string {
  const w = f.workloads[f.wl[i]];
  if (w.kind === 'StatefulSet') return `${w.name}-${f.sfx[i]}`;
  if (w.kind === 'DaemonSet') return `${w.name}-${suffix5(f.sfx[i])}`;
  return `${w.name}-${w.rs}-${suffix5(f.sfx[i])}`;
}
export function podIp(f: Fleet, i: number): string {
  const n = f.nodes[f.node[i]];
  const k = f.nodes.filter((x) => x.cl === n.cl).indexOf(n);
  return `10.${CLUSTERS[n.cl].octet}.${k + 1}.${f.ipl[i]}`;
}

export function fmtAge(sec: number): string {
  if (sec < 60) return `${Math.max(0, Math.floor(sec))}s`;
  if (sec < 3600) return `${Math.floor(sec / 60)}m`;
  if (sec < 86400) {
    const h = Math.floor(sec / 3600);
    const m = Math.floor((sec % 3600) / 60);
    return m && h < 10 ? `${h}h${m}m` : `${h}h`;
  }
  const d = Math.floor(sec / 86400);
  const h = Math.floor((sec % 86400) / 3600);
  return d < 10 && h ? `${d}d${h}h` : `${d}d`;
}
export const fmtN = (n: number) => n.toLocaleString('en-US');
