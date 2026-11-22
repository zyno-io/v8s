/**
 * View definitions (columns, accessors, chips), the filter/sort pipeline and
 * the row HTML renderers. Pure functions, shared by the server render in
 * Demo.astro and the client script, so the hydrated table is identical.
 */
import {
  CLUSTERS, FAMILIES, SEVERITY, STATUS_TEXT, fmtAge, fmtN, getFleet, podIp, podName,
  ST_CRASH, ST_DONE, ST_ERROR, ST_IMGPULL, ST_OOM, ST_PENDING, ST_CREATING, ST_RUNNING, ST_TERM,
  type Fleet, type Workload,
} from './data';
import { USE_KEYS, fmtMem, nodeAlloc, nodeUse, podUse, podValue, workloadRes, type UseKey } from './usage';

/** Time tick for the synthetic usage; the app advances it every few seconds so the numbers drift a little. */
export const clock = { tick: 0 };

export type ViewId = 'pods' | 'deploy' | 'nodes' | 'helm' | 'ks' | 'svc' | 'problems';
export type Tone = 'success' | 'warning' | 'danger' | 'neutral' | 'info';
export type ColKind = 'cluster' | 'name' | 'status' | 'text' | 'mono' | 'num' | 'age' | 'ready' | 'sev' | 'obj' | 'msg' | 'ack';

export interface Col { key: string; label: string; w: string; kind: ColKind; right?: boolean; sort?: boolean; head?: string; hint?: string }
export interface Chip { label: string; tone: Tone }
export interface Source {
  id: ViewId;
  title: string;
  noun: string;
  cols: Col[];
  minW: number;
  chips: Chip[];
  count: number;
  hasCb: boolean;
  cl(i: number): number;
  ns(i: number): string;
  name(i: number): string;
  bucket(i: number): number;
  tone(i: number): Tone;
  status(i: number): string;
  /** Sort value for a column. */
  val(i: number, key: string): string | number;
  /** Cell html for a column. */
  cell(i: number, c: Col): string;
  metric(i: number): string;
  age(i: number): number;
  /** Sort rank of a missing value: 0 = has a value, 1 = n/a, 2 = no data. Missing values always sort last. */
  miss?(i: number, key: string): 0 | 1 | 2;
  /** Pods only: show the IP column (true) or the default layout (false). Returns whether the layout changed. */
  setIpMode?(on: boolean): boolean;
}

export const esc = (s: string) => s.replace(/[&<>"]/g, (c) => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;' })[c]!);
export const tag = (text: string, tone: Tone = 'neutral') => `<span class="vd-tag vd-t-${tone}">${esc(text)}</span>`;
const clusterTag = (cl: number) =>
  `<span class="vd-tag"><span class="vd-dot" style="background:var(--dot-ok)"></span>${CLUSTERS[cl].id}</span>`;
// Header tooltips for the percentage columns: what the colours mean (the app's usageHint).
const BARS = 'green below 70 %, amber from 70 %, red from 90 %';
const HINT_LIM = `Bar: share of the limit, ${BARS}.`;
const HINT_NODE = `Bar: share of the node's allocatable, ${BARS}.`;
const HINT_REQ_MEM = `With a limit: no bar; amber above 100 % of the request, red above 200 % (pods over their memory request are evicted first when the node runs short). Without a limit: a bar, ${BARS}.`;
const HINT_REQ_CPU = `With a limit: no bar (running over a CPU request is normal). Without a limit: a bar, ${BARS}.`;
const useCols = (cl: string, ck: string, ml: string, mk: string, cw: string, mw: string): Col[] => [
  col(ck, `${cl} <span class="vd-u">m</span>`, cw, 'num', { right: true }),
  col(`${ck}r`, '%R', '34px', 'num', { right: true, hint: HINT_REQ_CPU }),
  col(`${ck}l`, '%L', '34px', 'num', { right: true, hint: HINT_LIM }),
  col(mk, `${ml} <span class="vd-u">Mi</span>`, mw, 'num', { right: true }),
  col(`${mk}r`, '%R', '34px', 'num', { right: true, hint: HINT_REQ_MEM }),
  col(`${mk}l`, '%L', '34px', 'num', { right: true, hint: HINT_LIM }),
];
/**
 * One percentage cell of the usage block: a background bar filling the row from the bottom by its value (capped at a full row),
 * green below 70 %, amber from 70 %, red from 90 %. Pure CSS (`--p`); no per-row objects. `bar` false = no bar (a request next to
 * a limit); then `mem` tints a memory request amber above 100 % and red above 200 %.
 */
const pctCell = (v: number, bar: boolean, tint = false): string => {
  if (bar) return `<span class="vd-b ${v >= 90 ? 'vd-hot' : v >= 70 ? 'vd-warn' : 'vd-ok'}" style="--p:${Math.min(v, 100)}%">${v}</span>`;
  return `<span class="vd-b${tint ? (v > 200 ? ' vd-bd' : v > 100 ? ' vd-bw' : '') : ''}">${v}</span>`;
};
/** One cell of the usage block: right-aligned unit-less integer, '—' for no data, 'n/a' when there is nothing to divide by. */
const useCell = (v: number | 'na' | null, bar: boolean, tint = false): string => {
  if (v === null) return '<span class="vd-na">—</span>';
  if (v === 'na') return '<span class="vd-na">n/a</span>';
  return bar || tint ? pctCell(v, bar, tint) : String(v);
};
const nameCell = (n: string) => `<span class="vd-name" data-open>${esc(n)}</span>`;

const clusterCol: Col = { key: 'cluster', label: 'Cluster', w: '70px', kind: 'cluster', sort: true };
const col = (key: string, label: string, w: string, kind: ColKind, extra: Partial<Col> = {}): Col => ({ key, label, w, kind, sort: true, ...extra });

// ---- Pods -----------------------------------------------------------------
let podNames: string[] | null = null;
const POD_BUCKET = [0, 1, 1, 2, 2, 2, -1, -1, 2];
const POD_TONE: Tone[] = ['success', 'warning', 'warning', 'danger', 'danger', 'danger', 'neutral', 'neutral', 'danger'];

export function getPodNames(f: Fleet): string[] {
  if (!podNames) {
    podNames = new Array(f.podCount);
    for (let i = 0; i < f.podCount; i++) podNames[i] = podName(f, i);
  }
  return podNames;
}
const shortNode = (n: string) => n.split('.')[0];

function podsSource(f: Fleet): Source {
  const names = () => getPodNames(f);
  const wl = (i: number): Workload => f.workloads[f.wl[i]];
  const cols: Col[] = [
    clusterCol,
    col('name', 'Name', 'minmax(104px,2fr)', 'name'),
    col('ns', 'Namespace', 'minmax(82px,0.9fr)', 'text'),
    col('ready', 'Ready', '44px', 'ready', { right: true }),
    col('status', 'Status', '96px', 'status'),
    col('restarts', 'Restarts', '50px', 'num', { right: true }),
    ...useCols('CPU', 'cpu', 'MEM', 'mem', '52px', '60px'),
    col('age', 'Age', '46px', 'age', { right: true }),
  ];
  const ipCol = col('ip', 'IP', '96px', 'mono');
  const s: Source = {
    id: 'pods', title: 'Pods', noun: 'pods', cols, minW: 880, hasCb: true, count: f.podCount,
    chips: [{ label: 'Running', tone: 'success' }, { label: 'Pending', tone: 'warning' }, { label: 'Failing', tone: 'danger' }],
    cl: (i) => f.cl[i],
    ns: (i) => wl(i).ns,
    name: (i) => names()[i],
    bucket: (i) => POD_BUCKET[f.st[i]],
    tone: (i) => POD_TONE[f.st[i]],
    status: (i) => STATUS_TEXT[f.st[i]],
    age: (i) => f.age[i],
    metric: (i) => {
      const u = podUse(f, i, clock.tick);
      return u ? `cpu ${u.cpu}m · mem ${fmtMem(u.mem)}` : `${f.rst[i]} restarts`;
    },
    miss(i, k) {
      const v = podValue(f, i, k as UseKey, 0);
      return v === null ? 2 : v === 'na' ? 1 : 0;
    },
    val(i, k) {
      if (USE_KEYS.includes(k as UseKey)) {
        const v = podValue(f, i, k as UseKey, 0);
        return typeof v === 'number' ? v : 0;
      }
      switch (k) {
        case 'cluster': return CLUSTERS[f.cl[i]].id;
        case 'name': return names()[i];
        case 'ns': return wl(i).ns;
        case 'ready': return f.rdy[i] / wl(i).containers;
        case 'status': return STATUS_TEXT[f.st[i]];
        case 'restarts': return f.rst[i];
        case 'node': return f.nodes[f.node[i]].name;
        case 'ip': return podIp(f, i);
        default: return f.age[i];
      }
    },
    cell(i, c) {
      if (USE_KEYS.includes(c.key as UseKey)) {
        const k = c.key as UseKey;
        const v = podValue(f, i, k, clock.tick);
        const pct = k.endsWith('l') || k.endsWith('r');
        // %L always has a bar; %R only when the pod has no limit for that resource, else (memory only) a tint.
        const noLim = pct && k.endsWith('r') && !(k.startsWith('cpu') ? workloadRes(f.wl[i]).cLim : workloadRes(f.wl[i]).mLim);
        const cell = useCell(v, k.endsWith('l') || noLim, k.startsWith('mem') && k.endsWith('r') && !noLim);
        if (k !== 'cpu' && k !== 'mem') return cell;
        const p = podUse(f, i, clock.tick);
        if (!p) return cell;
        const cpu = k === 'cpu';
        const num = (x: number) => (x ? (cpu ? `${x}m` : fmtMem(x)) : 'not set');
        const tip = `used ${num(cpu ? p.cpu : p.mem)}, requested ${num(cpu ? p.r.cReq : p.r.mReq)}, limit ${num(cpu ? p.r.cLim : p.r.mLim)}`;
        return `<span title="${tip}">${cell}</span>`;
      }
      switch (c.kind === 'name' ? 'name' : c.key) {
        case 'cluster': return clusterTag(f.cl[i]);
        case 'name': return nameCell(names()[i]);
        case 'ns': return wl(i).ns;
        case 'ready': {
          const n = wl(i).containers;
          const bad = f.rdy[i] < n && f.st[i] !== ST_DONE;
          return `<span${bad ? ' class="vd-bad"' : ''}>${f.rdy[i]}/${n}</span>`;
        }
        case 'status': return tag(STATUS_TEXT[f.st[i]], POD_TONE[f.st[i]]);
        case 'restarts': return `<span${f.rst[i] > 4 ? ' class="vd-bad"' : ''}>${f.rst[i]}</span>`;
        case 'node': return esc(shortNode(f.nodes[f.node[i]].name));
        case 'ip': return podIp(f, i);
        default: return fmtAge(f.age[i]);
      }
    },
  };
  /** The IP column (in place of Namespace) appears only while the filter is an IP query, so the usage block keeps its room. */
  s.setIpMode = (on) => {
    const has = s.cols.includes(ipCol);
    if (on === has) return false;
    s.cols = on ? cols.map((c) => (c.key === 'ns' ? ipCol : c)) : cols;
    return true;
  };
  return s;
}

// ---- Deployments ------------------------------------------------------------
const depBucket = (w: Workload) => (w.ready >= w.replicas * 0.75 ? 0 : w.ready >= w.replicas * 0.5 ? 1 : 2);
const DEP_STATUS = ['Available', 'Progressing', 'Degraded'];
const DEP_TONE: Tone[] = ['success', 'warning', 'danger'];

function deploySource(f: Fleet): Source {
  const w = (i: number) => f.workloads[f.deployments[i]];
  const cols: Col[] = [
    clusterCol,
    col('name', 'Name', 'minmax(160px,2fr)', 'name'),
    col('ns', 'Namespace', 'minmax(90px,1fr)', 'text'),
    col('ready', 'Ready', '60px', 'ready', { right: true }),
    col('uptodate', 'Up-to-date', '76px', 'num', { right: true }),
    col('avail', 'Available', '70px', 'num', { right: true }),
    col('status', 'Status', '104px', 'status'),
    col('image', 'Image', 'minmax(140px,2fr)', 'mono'),
    col('age', 'Age', '48px', 'age', { right: true }),
  ];
  return {
    id: 'deploy', title: 'Deployments', noun: 'deployments', cols, minW: 860, hasCb: true, count: f.deployments.length,
    chips: [{ label: 'Available', tone: 'success' }, { label: 'Progressing', tone: 'warning' }, { label: 'Degraded', tone: 'danger' }],
    cl: (i) => w(i).cl, ns: (i) => w(i).ns, name: (i) => w(i).name,
    bucket: (i) => depBucket(w(i)), tone: (i) => DEP_TONE[depBucket(w(i))], status: (i) => DEP_STATUS[depBucket(w(i))],
    age: (i) => w(i).ageSec, metric: (i) => `${w(i).ready}/${w(i).replicas} ready`,
    val(i, k) {
      const x = w(i);
      switch (k) {
        case 'cluster': return CLUSTERS[x.cl].id;
        case 'name': return x.name;
        case 'ns': return x.ns;
        case 'ready': return x.ready / x.replicas;
        case 'uptodate': return x.replicas;
        case 'avail': return x.ready;
        case 'status': return depBucket(x);
        case 'image': return x.image;
        default: return x.ageSec;
      }
    },
    cell(i, c) {
      const x = w(i);
      switch (c.key) {
        case 'cluster': return clusterTag(x.cl);
        case 'name': return nameCell(x.name);
        case 'ns': return x.ns;
        case 'ready': return `<span${x.ready < x.replicas ? ' class="vd-bad"' : ''}>${x.ready}/${x.replicas}</span>`;
        case 'uptodate': return String(x.replicas);
        case 'avail': return String(x.ready);
        case 'status': return tag(DEP_STATUS[depBucket(x)], DEP_TONE[depBucket(x)]);
        case 'image': return esc(x.image);
        default: return fmtAge(x.ageSec);
      }
    },
  };
}

// ---- Nodes -------------------------------------------------------------------
function nodesSource(f: Fleet): Source {
  const n = (i: number) => f.nodes[i];
  const b = (i: number) => (n(i).status === 'Ready' ? 0 : n(i).status === 'NotReady' ? 2 : 1);
  const label = (i: number) => (n(i).status === 'Ready,SchedulingDisabled' ? 'Ready, cordoned' : n(i).status);
  const T: Tone[] = ['success', 'warning', 'danger'];
  const cols: Col[] = [
    clusterCol,
    col('name', 'Name', 'minmax(110px,2fr)', 'name'),
    col('status', 'Status', '104px', 'status'),
    col('role', 'Roles', '76px', 'text'),
    col('pods', 'Pods', '44px', 'num', { right: true }),
    col('cpu', 'CPU <span class="vd-u">m</span>', '52px', 'num', { right: true }),
    col('cpup', '%', '36px', 'num', { right: true, hint: HINT_NODE }),
    col('cpua', 'alloc', '52px', 'num', { right: true }),
    col('mem', 'MEM <span class="vd-u">Mi</span>', '62px', 'num', { right: true }),
    col('memp', '%', '36px', 'num', { right: true, hint: HINT_NODE }),
    col('mema', 'alloc', '56px', 'num', { right: true }),
    col('age', 'Age', '40px', 'age', { right: true }),
  ];
  const al = (i: number) => nodeAlloc(n(i));
  const nodeVal = (i: number, k: string): number | null => {
    const x = n(i);
    if (x.status === 'NotReady') return k.endsWith('a') ? (k === 'cpua' ? al(i).cpu : al(i).mem) : null;
    const u = nodeUse(x, i, clock.tick);
    switch (k) {
      case 'cpu': return u.cpu;
      case 'cpup': return u.cp;
      case 'cpua': return u.al.cpu;
      case 'mem': return u.mem;
      case 'memp': return u.mp;
      default: return al(i).mem;
    }
  };
  const pct = (v: number, st: string) => (st === 'NotReady' ? '—' : `${v}%`);
  return {
    id: 'nodes', title: 'Nodes', noun: 'nodes', cols, minW: 880, hasCb: true, count: f.nodes.length,
    chips: [{ label: 'Ready', tone: 'success' }, { label: 'Cordoned', tone: 'warning' }, { label: 'NotReady', tone: 'danger' }],
    cl: (i) => n(i).cl, ns: () => '', name: (i) => n(i).name, bucket: b, tone: (i) => T[b(i)], status: label,
    age: (i) => n(i).ageSec, metric: (i) => `cpu ${pct(nodeVal(i, 'cpup') ?? 0, n(i).status)}`,
    val(i, k) {
      const x = n(i);
      switch (k) {
        case 'cluster': return CLUSTERS[x.cl].id;
        case 'name': return x.name;
        case 'status': return b(i);
        case 'role': return x.role;
        case 'type': return x.type;
        case 'version': return x.version;
        case 'pods': return x.pods;
        case 'cpu': case 'cpup': case 'cpua': case 'mem': case 'memp': case 'mema': return nodeVal(i, k) ?? 0;
        case 'ip': return x.ip;
        default: return x.ageSec;
      }
    },
    cell(i, c) {
      const x = n(i);
      switch (c.key) {
        case 'cluster': return clusterTag(x.cl);
        case 'name': return nameCell(x.name);
        case 'status': return tag(label(i), T[b(i)]);
        case 'role': return x.role;
        case 'type': return x.type;
        case 'version': return x.version;
        case 'pods': return String(x.pods);
        case 'cpu': case 'cpup': case 'cpua': case 'mem': case 'memp': case 'mema': {
          const v = nodeVal(i, c.key);
          return v === null ? '<span class="vd-na">—</span>' : c.key === 'cpup' || c.key === 'memp' ? pctCell(v, true) : String(v);
        }
        case 'ip': return x.ip;
        default: return fmtAge(x.ageSec);
      }
    },
  };
}

// ---- Helm ----------------------------------------------------------------------
function helmSource(f: Fleet): Source {
  const h = (i: number) => f.helm[i];
  const b = (i: number) => (h(i).status === 'deployed' ? 0 : h(i).status === 'failed' ? 2 : 1);
  const T: Tone[] = ['success', 'warning', 'danger'];
  const cols: Col[] = [
    clusterCol,
    col('name', 'Release', 'minmax(150px,1.6fr)', 'name'),
    col('ns', 'Namespace', 'minmax(90px,1fr)', 'text'),
    col('chart', 'Chart', 'minmax(140px,1.4fr)', 'mono'),
    col('version', 'Chart version', '92px', 'mono'),
    col('app', 'App version', '84px', 'mono'),
    col('rev', 'Rev', '44px', 'num', { right: true }),
    col('status', 'Status', '128px', 'status'),
    col('age', 'Updated', '60px', 'age', { right: true }),
  ];
  return {
    id: 'helm', title: 'Helm releases', noun: 'releases', cols, minW: 860, hasCb: true, count: f.helm.length,
    chips: [{ label: 'Deployed', tone: 'success' }, { label: 'Pending', tone: 'warning' }, { label: 'Failed', tone: 'danger' }],
    cl: (i) => h(i).cl, ns: (i) => h(i).ns, name: (i) => h(i).name, bucket: b, tone: (i) => T[b(i)], status: (i) => h(i).status,
    age: (i) => h(i).ageSec, metric: (i) => `rev ${h(i).revision}`,
    val(i, k) {
      const x = h(i);
      switch (k) {
        case 'cluster': return CLUSTERS[x.cl].id;
        case 'name': return x.name;
        case 'ns': return x.ns;
        case 'chart': return x.chart;
        case 'version': return x.version;
        case 'app': return x.appVersion;
        case 'rev': return x.revision;
        case 'status': return b(i);
        default: return x.ageSec;
      }
    },
    cell(i, c) {
      const x = h(i);
      switch (c.key) {
        case 'cluster': return clusterTag(x.cl);
        case 'name': return nameCell(x.name);
        case 'ns': return x.ns;
        case 'chart': return x.chart;
        case 'version': return x.version;
        case 'app': return x.appVersion;
        case 'rev': return String(x.revision);
        case 'status': return tag(x.status, T[b(i)]);
        default: return fmtAge(x.ageSec);
      }
    },
  };
}

// ---- Kustomizations -------------------------------------------------------------
function ksSource(f: Fleet): Source {
  const k = (i: number) => f.ks[i];
  const b = (i: number) => (k(i).status === 'Ready' ? 0 : k(i).status === 'Reconciling' ? 1 : 2);
  const T: Tone[] = ['success', 'warning', 'danger'];
  const cols: Col[] = [
    clusterCol,
    col('name', 'Name', 'minmax(130px,1.4fr)', 'name'),
    col('ns', 'Namespace', 'minmax(90px,1fr)', 'text'),
    col('path', 'Path', 'minmax(150px,1.6fr)', 'mono'),
    col('rev', 'Revision', '130px', 'mono'),
    col('status', 'Ready', '104px', 'status'),
    col('age', 'Last applied', '84px', 'age', { right: true }),
  ];
  return {
    id: 'ks', title: 'Kustomizations', noun: 'kustomizations', cols, minW: 860, hasCb: true, count: f.ks.length,
    chips: [{ label: 'Ready', tone: 'success' }, { label: 'Reconciling', tone: 'warning' }, { label: 'Not ready', tone: 'danger' }],
    cl: (i) => k(i).cl, ns: (i) => k(i).ns, name: (i) => k(i).name, bucket: b, tone: (i) => T[b(i)], status: (i) => k(i).status,
    age: (i) => k(i).ageSec, metric: (i) => k(i).revision.slice(0, 17),
    val(i, key) {
      const x = k(i);
      switch (key) {
        case 'cluster': return CLUSTERS[x.cl].id;
        case 'name': return x.name;
        case 'ns': return x.ns;
        case 'path': return x.path;
        case 'rev': return x.revision;
        case 'status': return b(i);
        default: return x.ageSec;
      }
    },
    cell(i, c) {
      const x = k(i);
      switch (c.key) {
        case 'cluster': return clusterTag(x.cl);
        case 'name': return nameCell(x.name);
        case 'ns': return x.ns;
        case 'path': return esc(x.path);
        case 'rev': return esc(x.revision);
        case 'status': return tag(x.status, T[b(i)]);
        default: return fmtAge(x.ageSec);
      }
    },
  };
}

// ---- Services --------------------------------------------------------------------
function svcSource(f: Fleet): Source {
  const s = (i: number) => f.svc[i];
  const cols: Col[] = [
    clusterCol,
    col('name', 'Name', 'minmax(150px,1.8fr)', 'name'),
    col('ns', 'Namespace', 'minmax(90px,1fr)', 'text'),
    col('type', 'Type', '100px', 'text'),
    col('ip', 'Cluster IP', '112px', 'mono'),
    col('ext', 'External IP', 'minmax(110px,1.4fr)', 'mono'),
    col('ports', 'Ports', '64px', 'mono'),
    col('age', 'Age', '48px', 'age', { right: true }),
  ];
  return {
    id: 'svc', title: 'Services', noun: 'services', cols, minW: 860, hasCb: true, count: f.svc.length, chips: [],
    cl: (i) => s(i).cl, ns: (i) => s(i).ns, name: (i) => s(i).name, bucket: () => -1,
    tone: (i) => (s(i).external === '<pending>' ? 'warning' : 'neutral'), status: (i) => s(i).type,
    age: (i) => s(i).ageSec, metric: (i) => s(i).type,
    val(i, k) {
      const x = s(i);
      switch (k) {
        case 'cluster': return CLUSTERS[x.cl].id;
        case 'name': return x.name;
        case 'ns': return x.ns;
        case 'type': return x.type;
        case 'ip': return x.clusterIp;
        case 'ext': return x.external;
        case 'ports': return x.ports;
        default: return x.ageSec;
      }
    },
    cell(i, c) {
      const x = s(i);
      switch (c.key) {
        case 'cluster': return clusterTag(x.cl);
        case 'name': return nameCell(x.name);
        case 'ns': return x.ns;
        case 'type': return x.type;
        case 'ip': return x.clusterIp;
        case 'ext': return x.external ? (x.external === '<pending>' ? tag('pending', 'warning') : esc(x.external)) : '—';
        case 'ports': return x.ports;
        default: return fmtAge(x.ageSec);
      }
    },
  };
}

// ---- Problems --------------------------------------------------------------------
const SEV_TONE: Tone[] = ['danger', 'danger', 'warning', 'info'];
function problemsSource(f: Fleet): Source {
  const p = (i: number) => f.problems[i];
  const cols: Col[] = [
    { key: 'sev', label: 'Severity', w: '76px', kind: 'sev' },
    { key: 'cluster', label: 'Cluster', w: '70px', kind: 'cluster' },
    { key: 'cls', label: 'Class', w: '116px', kind: 'mono' },
    { key: 'object', label: 'Object', w: 'minmax(220px,2fr)', kind: 'obj' },
    { key: 'msg', label: 'What’s wrong', w: 'minmax(180px,2.2fr)', kind: 'msg' },
    { key: 'age', label: 'Since', w: '48px', kind: 'age', right: true },
    { key: 'ack', label: '', w: '64px', kind: 'ack', right: true },
  ];
  return {
    id: 'problems', title: 'Problems', noun: 'problems', cols, minW: 860, hasCb: false, count: f.problems.length,
    chips: [{ label: 'Critical', tone: 'danger' }, { label: 'Error', tone: 'danger' }, { label: 'Warning', tone: 'warning' }, { label: 'Info', tone: 'info' }],
    cl: (i) => p(i).cl, ns: (i) => p(i).ns, name: (i) => p(i).object, bucket: (i) => p(i).sev,
    tone: (i) => SEV_TONE[p(i).sev], status: (i) => SEVERITY[p(i).sev], age: (i) => p(i).ageSec,
    metric: (i) => p(i).cls,
    val: (i) => p(i).ageSec,
    cell(i, c) {
      const x = p(i);
      switch (c.key) {
        case 'sev': return tag(SEVERITY[x.sev], SEV_TONE[x.sev]);
        case 'cluster': return clusterTag(x.cl);
        case 'cls': return x.cls;
        case 'object': return `${nameCell(x.object)} <span class="vd-kind">${x.kind}${x.ns ? ` · ${x.ns}` : ''}</span>`;
        case 'msg': return esc(x.message);
        case 'age': return fmtAge(x.ageSec);
        default: return `<button type="button" class="vd-btn vd-sm" data-ack="${x.id}" aria-label="Acknowledge ${esc(x.object)}"><i class="fa-solid fa-check" aria-hidden="true"></i>Ack</button>`;
      }
    },
  };
}

const sources = new Map<ViewId, Source>();
export function getSource(id: ViewId): Source {
  let s = sources.get(id);
  if (!s) {
    const f = getFleet();
    s = { pods: podsSource, deploy: deploySource, nodes: nodesSource, helm: helmSource, ks: ksSource, svc: svcSource, problems: problemsSource }[id](f);
    sources.set(id, s);
  }
  return s;
}

// ---- pipeline ---------------------------------------------------------------------
export interface Filter { mask: boolean[]; ns: string; q: string; bucket: number }
export interface Result { ids: Uint32Array; counts: number[]; matched: number }

const orderCache = new Map<string, Uint32Array>();
/** Number of rows with a value at the front of a cached order (the rest are n/a, then no data). */
const presentCache = new Map<string, number>();
function sortedAll(src: Source, key: string): Uint32Array {
  const ck = `${src.id}:${key}`;
  let o = orderCache.get(ck);
  if (o) return o;
  const n = src.count;
  const vals = new Array<string | number>(n);
  for (let i = 0; i < n; i++) vals[i] = src.val(i, key);
  const idx = new Uint32Array(n);
  const numeric = typeof vals[0] === 'number';
  const sortIdx = (a: Uint32Array) => {
    if (numeric) {
      const v = Float64Array.from(vals as number[]);
      a.sort((x, y) => v[x] - v[y]);
    } else {
      const v = vals as string[];
      a.sort((x, y) => (v[x] < v[y] ? -1 : v[x] > v[y] ? 1 : 0));
    }
  };
  if (src.miss && src.miss(0, key) !== undefined && src.id === 'pods' && /^(cpu|mem)[rl]?$/.test(key)) {
    const ranks = new Uint8Array(n);
    const cnt = [0, 0, 0];
    for (let i = 0; i < n; i++) cnt[(ranks[i] = src.miss(i, key))]++;
    const present = new Uint32Array(cnt[0]);
    let p = 0, na = cnt[0], dash = cnt[0] + cnt[1];
    for (let i = 0; i < n; i++) {
      if (ranks[i] === 0) present[p++] = i;
      else if (ranks[i] === 1) idx[na++] = i;
      else idx[dash++] = i;
    }
    sortIdx(present);
    idx.set(present, 0);
    presentCache.set(ck, cnt[0]);
  } else {
    for (let i = 0; i < n; i++) idx[i] = i;
    sortIdx(idx);
    presentCache.set(ck, n);
  }
  orderCache.set(ck, idx);
  return (o = idx);
}

/** A query that reads as an IP address, prefix, CIDR or range: digits and dots, or hex and colons. */
export const isIpQuery = (q: string): boolean => /^(\d{1,3}\.[\d.]*|[0-9a-f]*:[0-9a-f:]+)(\/\d+|-[\d.]+)?$/i.test(q.trim());
/** Demo-side IP search: a prefix of a pod's address (the real search also takes CIDRs and ranges). */
const ipMatch = (ip: string, q: string): boolean => ip.startsWith(q.replace(/\/.*$/, ''));

export function runPipeline(src: Source, key: string, dir: 1 | -1, f: Filter): Result {
  const all = sortedAll(src, key);
  const np = presentCache.get(`${src.id}:${key}`) ?? all.length;
  const n = all.length;
  const out = new Uint32Array(n);
  const counts = new Array(src.chips.length).fill(0);
  const q = f.q.trim().toLowerCase();
  const ipq = src.id === 'pods' && isIpQuery(q);
  const fleet = ipq ? getFleet() : null;
  let k = 0;
  for (let j = 0; j < n; j++) {
    const i = j >= np || dir === 1 ? all[j] : all[np - 1 - j];
    if (!f.mask[src.cl(i)]) continue;
    if (f.ns && src.ns(i) !== f.ns && src.id !== 'nodes') continue;
    if (q && (ipq ? !ipMatch(podIp(fleet!, i), q) : !src.name(i).includes(q) && !src.ns(i).includes(q))) continue;
    const b = src.bucket(i);
    if (b >= 0) counts[b]++;
    if (f.bucket >= 0 && b !== f.bucket) continue;
    out[k++] = i;
  }
  return { ids: out.subarray(0, k), counts, matched: k };
}

/** Problem list items: ids >= 0 are problems, negative are family headers (-(familyIndex+1)). */
export function problemItems(f: Filter, acked: Set<number>, showAck: boolean, group: boolean): { items: number[]; counts: number[]; ackedCount: number } {
  const src = getSource('problems');
  const fleet = getFleet();
  const counts = [0, 0, 0, 0];
  let ackedCount = 0;
  const q = f.q.trim().toLowerCase();
  const list: number[] = [];
  for (let i = 0; i < fleet.problems.length; i++) {
    const p = fleet.problems[i];
    if (!f.mask[p.cl]) continue;
    if (f.ns && p.ns !== f.ns) continue;
    if (q && !p.object.toLowerCase().includes(q) && !p.message.toLowerCase().includes(q)) continue;
    const isAck = acked.has(p.id);
    if (isAck) ackedCount++;
    if (isAck && !showAck) continue;
    counts[p.sev]++;
    if (f.bucket >= 0 && p.sev !== f.bucket) continue;
    list.push(i);
  }
  void src;
  if (!group) return { items: list, counts, ackedCount };
  const items: number[] = [];
  FAMILIES.forEach((fam, fi) => {
    const rows = list.filter((i) => fleet.problems[i].family === fam);
    if (rows.length) items.push(-(fi + 1), ...rows);
  });
  return { items, counts, ackedCount };
}

// ---- html -------------------------------------------------------------------------------
export function gridCols(src: Source): string {
  return (src.hasCb ? '16px ' : '') + src.cols.map((c) => c.w).join(' ');
}

export function headHtml(src: Source, sortKey: string, dir: 1 | -1): string {
  let h = src.hasCb
    ? `<span role="columnheader" class="vd-cbc"><input type="checkbox" class="vd-cb" data-all aria-label="Select all rows"></span>`
    : '';
  for (const c of src.cols) {
    const on = c.key === sortKey;
    const aria = c.sort ? ` aria-sort="${on ? (dir === 1 ? 'ascending' : 'descending') : 'none'}"` : '';
    const arrow = on ? `<i class="fa-solid fa-arrow-${dir === 1 ? 'up' : 'down'}" aria-hidden="true"></i>` : '';
    h += `<span role="columnheader" class="vd-c vd-h${c.right ? ' vd-r' : ''}"${aria}>${
      c.sort ? `<button type="button" class="vd-thb" data-sort="${c.key}"${c.hint ? ` title="${esc(`Sort by ${c.label.replace(/<[^>]*>/g, '').trim().toLowerCase()}\n${c.hint}`)}"` : ''}>${c.label}${arrow}</button>` : c.label
    }</span>`;
  }
  return h;
}

/** Inner html of a desktop row (cells only). */
export function rowInner(src: Source, i: number, sel: boolean): string {
  let h = src.hasCb
    ? `<span role="gridcell" class="vd-cbc"><input type="checkbox" class="vd-cb" tabindex="-1" aria-label="Select row"${sel ? ' checked' : ''}></span>`
    : '';
  for (const c of src.cols) {
    const cls = `vd-c${c.right ? ' vd-r' : ''}${c.kind === 'mono' ? ' vd-mono' : ''}${c.kind === 'msg' ? ' vd-msg' : ''}`;
    h += `<span role="gridcell" class="${cls}">${src.cell(i, c)}</span>`;
  }
  return h;
}

/** Inner html of a phone card. */
export function cardInner(src: Source, i: number, sel: boolean): string {
  const nm = esc(src.name(i));
  const ns = src.ns(i);
  const showAge = src.id !== 'ks';
  return (
    `<span class="vd-pc-main"><span class="vd-pc-l1"><span class="vd-name" data-open>${nm}</span>${tag(src.status(i), src.tone(i))}</span>` +
    `<span class="vd-pc-l2"><span class="vd-dot" style="background:var(--dot-ok)"></span>${CLUSTERS[src.cl(i)].id}${ns ? ` · ${ns}` : ''}${showAge ? ` · ${fmtAge(src.age(i))}` : ''}</span></span>` +
    `<span class="vd-pc-metric">${esc(src.metric(i))}</span>` + (sel ? '' : '')
  );
}

export function chipsHtml(src: Source, counts: number[], active: number): string {
  return src.chips
    .map(
      (c, k) =>
        `<button type="button" class="vd-chip${active === k ? ' vd-on' : ''}" data-chip="${k}" aria-pressed="${active === k}"><span class="vd-dot vd-d-${c.tone}"></span>${c.label} <b>${fmtN(counts[k] ?? 0)}</b></button>`,
    )
    .join('');
}

export function problemRowInner(src: Source, id: number, acked: boolean): string {
  const fleet = getFleet();
  const idx = fleet.problems.findIndex((p) => p.id === id);
  void acked;
  return rowInner(src, idx, false);
}

export function headerRowInner(fam: number, n: number): string {
  return `<span class="vd-grp" role="gridcell"><i class="fa-solid fa-chevron-down" aria-hidden="true"></i>${FAMILIES[fam]}<span class="vd-n">${n}</span></span>`;
}

export { ST_CRASH, ST_IMGPULL, ST_OOM, ST_PENDING, ST_CREATING, ST_RUNNING, ST_TERM, ST_ERROR };
