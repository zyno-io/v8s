/**
 * Synthetic CPU and memory for the demo's Pods and Nodes tables. Everything is a pure function of the pod (or node)
 * index and a small time tick, so the server render and the client agree and nothing is stored per pod: 50,000 pods
 * cost no extra memory. Requests and limits come from the pod's workload; usage varies a little with the tick.
 * Mirrors the real columns (docs/guide/resources.md "CPU and memory"): CPU in millicores, memory in MiB,
 * percentages of the request (%R), the limit (%L) and a node's allocatable; "n/a" when there is nothing to divide by,
 * "-" (shown as an em dash) when metrics-server reports nothing for the pod.
 */
import { ST_CRASH, ST_OOM, ST_RUNNING, ST_TERM, type Fleet } from './data';

const hash = (n: number, salt = 0): number => {
  let h = Math.imul(n ^ (salt * 0x9e3779b1), 0x85ebca6b);
  h ^= h >>> 13;
  h = Math.imul(h, 0xc2b2ae35);
  h ^= h >>> 16;
  return (h >>> 0) / 4294967296;
};

const C_REQ = [10, 25, 50, 100, 250, 500];
const M_REQ = [32, 64, 128, 256, 512, 1024];
const C_LIM = [0, 2, 2, 4]; // multiples of the request, 0 = no limit
const M_LIM = [0, 2, 1.25, 1.5];

export interface Res { cReq: number; cLim: number; mReq: number; mLim: number }
const resCache = new Map<number, Res>();
/** Requests and limits of a workload (0 = not set). Some workloads set none at all, as in real clusters. */
export function workloadRes(wlId: number): Res {
  let r = resCache.get(wlId);
  if (!r) {
    const none = hash(wlId, 1) < 0.1;
    const cReq = none ? 0 : C_REQ[Math.floor(hash(wlId, 2) * C_REQ.length)];
    const mReq = none ? 0 : M_REQ[Math.floor(hash(wlId, 3) * M_REQ.length)];
    const cm = C_LIM[Math.floor(hash(wlId, 4) * C_LIM.length)];
    const mm = M_LIM[Math.floor(hash(wlId, 5) * M_LIM.length)];
    r = { cReq, cLim: Math.round(cReq * cm), mReq, mLim: Math.round(mReq * mm) };
    resCache.set(wlId, r);
  }
  return r;
}

const WITH_METRICS = new Set([ST_RUNNING, ST_CRASH, ST_OOM, ST_TERM]);

export interface PodUse { cpu: number; mem: number; r: Res }
/** Usage of pod `i` at time tick `t`, or null when metrics-server would report nothing for it. */
export function podUse(f: Fleet, i: number, t: number): PodUse | null {
  if (!WITH_METRICS.has(f.st[i])) return null;
  const r = workloadRes(f.wl[i]);
  const u = hash(i, 7);
  const wob = 1 + 0.05 * Math.sin(t * 0.9 + hash(i, 8) * 6.28);
  const cBase = r.cReq || 50;
  const mBase = r.mReq || 128;
  const crashed = f.st[i] === ST_CRASH;
  let cpu = Math.max(1, Math.round(cBase * (crashed ? 0.05 : 0.08 + 0.85 * u * u) * wob));
  let mem = Math.max(4, Math.round(mBase * (0.3 + 0.95 * hash(i, 9) ** 2) * (1 + (wob - 1) / 2)));
  if (f.st[i] === ST_OOM) mem = r.mLim ? Math.round(r.mLim * (1 + 0.04 * hash(i, 10))) : mem;
  if (r.cLim && cpu > r.cLim) cpu = r.cLim;
  return { cpu, mem, r };
}

export type UseKey = 'cpu' | 'cpur' | 'cpul' | 'mem' | 'memr' | 'meml';
export const USE_KEYS: UseKey[] = ['cpu', 'cpur', 'cpul', 'mem', 'memr', 'meml'];

/** Value of one usage column: a number, or null (no data) / 'na' (nothing to divide by). */
export function podValue(f: Fleet, i: number, k: UseKey, t: number): number | 'na' | null {
  const p = podUse(f, i, t);
  if (!p) return null;
  const cpu = k.startsWith('cpu');
  const used = cpu ? p.cpu : p.mem;
  if (k === 'cpu' || k === 'mem') return used;
  const base = k.endsWith('r') ? (cpu ? p.r.cReq : p.r.mReq) : cpu ? p.r.cLim : p.r.mLim;
  return base ? Math.round((used / base) * 100) : 'na';
}

export const fmtMem = (mi: number) => (mi >= 1024 ? `${(mi / 1024).toFixed(1)}Gi` : `${mi}Mi`);

// ---- nodes ----------------------------------------------------------------------------------
// Allocatable by instance type, as EKS reports it: capacity less kube-reserved and the eviction threshold.
const ALLOC: Record<string, { cpu: number; mem: number }> = {
  'm6i.large': { cpu: 1930, mem: 6900 },
  'm6i.4xlarge': { cpu: 15890, mem: 61400 },
  'c6i.8xlarge': { cpu: 31850, mem: 61400 },
  'c7i.8xlarge': { cpu: 31850, mem: 61400 },
  'c6a.8xlarge': { cpu: 31850, mem: 61400 },
  'm6i.8xlarge': { cpu: 31850, mem: 124500 },
  'm7i.8xlarge': { cpu: 31850, mem: 124500 },
  'm6a.8xlarge': { cpu: 31850, mem: 124500 },
};
export function nodeAlloc(n: { type: string }): { cpu: number; mem: number } {
  return ALLOC[n.type];
}

/** One node's usage percentages (a little drift with the tick) and amounts, for the Nodes table and the Overview. */
export function nodeUse(n: { type: string; cpu: number; mem: number }, i: number, t: number) {
  const wob = 1 + 0.02 * Math.sin(t * 0.7 + i);
  const cp = Math.min(99, Math.round(n.cpu * wob));
  const mp = Math.min(99, Math.round(n.mem * wob));
  const al = nodeAlloc(n);
  return { cp, mp, cpu: Math.round((cp * al.cpu) / 100), mem: Math.round((mp * al.mem) / 100), al };
}
