/**
 * Command bar grammar: completion (prefix first, then fuzzy) and execution.
 * Pure; the app applies the returned action.
 */
import { CLUSTERS, GROUPS, NAMESPACES } from './data';
import type { ViewId } from './views';

export interface Item { label: string; insert: string; caption: string; icon: string }
export type Action =
  | { t: 'view'; view: ViewId; ns?: string }
  | { t: 'ctxOpen' }
  | { t: 'ctx'; mode: 'set' | 'add' | 'rm'; ids: string[] }
  | { t: 'ns'; ns: string }
  | { t: 'ip'; q: string }
  | { t: 'overview' }
  | { t: 'quit' };
export type Parsed = { ok: true; action: Action } | { ok: false; msg: string };

interface Cmd { names: string[]; view?: ViewId; caption: string; icon: string }
const CMDS: Cmd[] = [
  { names: ['po', 'pods', 'pod'], view: 'pods', caption: 'Workloads · Pods', icon: 'cube' },
  { names: ['deploy', 'deployments', 'deployment'], view: 'deploy', caption: 'Workloads · Deployments', icon: 'layer-group' },
  { names: ['no', 'nodes', 'node'], view: 'nodes', caption: 'Cluster · Nodes', icon: 'server' },
  { names: ['ks', 'kustomizations'], view: 'ks', caption: 'Flux · Kustomizations', icon: 'code-branch' },
  { names: ['hr', 'helmreleases', 'helm'], view: 'helm', caption: 'Helm · Releases', icon: 'ship' },
  { names: ['svc', 'services', 'service'], view: 'svc', caption: 'Network · Services', icon: 'network-wired' },
  { names: ['pb', 'problems'], view: 'problems', caption: 'Cluster · Problems', icon: 'triangle-exclamation' },
  { names: ['ctx', 'context', 'contexts'], caption: 'Choose clusters', icon: 'circle-nodes' },
  { names: ['overview', 'ov'], caption: 'Cluster · Overview', icon: 'gauge' },
  { names: ['ip'], caption: 'Search by IP address', icon: 'location-dot' },
  { names: ['ns', 'namespace'], caption: 'Set namespace filter', icon: 'folder' },
  { names: ['q', 'quit'], caption: 'Close the command bar', icon: 'xmark' },
];

/** Score: 0 = prefix, 1+ = fuzzy subsequence (lower is better), -1 = no match. */
export function score(q: string, s: string): number {
  if (!q) return 0;
  if (s.startsWith(q)) return 0;
  const i0 = s.indexOf(q);
  if (i0 >= 0) return 1 + i0;
  let j = 0;
  let gap = 0;
  for (let i = 0; i < s.length && j < q.length; i++) {
    if (s[i] === q[j]) j++;
    else if (j > 0) gap++;
  }
  return j === q.length ? 20 + gap : -1;
}

function rank<T>(q: string, list: T[], key: (t: T) => string): T[] {
  return list
    .map((t, n) => ({ t, s: score(q, key(t)), n }))
    .filter((x) => x.s >= 0)
    .sort((a, b) => a.s - b.s || a.n - b.n)
    .map((x) => x.t);
}

const nsItems = (cmd: string, q: string): Item[] =>
  rank(q, ['all', ...NAMESPACES], (n) => n).map((n) => ({
    label: n, insert: `${cmd} ${n}`, caption: n === 'all' ? 'all namespaces' : 'namespace', icon: 'folder',
  }));

function ctxItems(q: string): Item[] {
  const sign = q[0] === '+' || q[0] === '-' ? q[0] : '';
  const body = q.slice(sign ? 1 : 0);
  const cands = [...CLUSTERS.map((c) => ({ n: c.id, cap: `cluster · ${c.group}` })), ...GROUPS.map((g) => ({ n: g, cap: `group · ${CLUSTERS.filter((c) => c.group === g).length} cluster${g === 'production' ? 's' : ''}` }))];
  return rank(body, cands, (c) => c.n).map((c) => ({ label: sign + c.n, insert: `ctx ${sign}${c.n}`, caption: c.cap, icon: c.cap.startsWith('group') ? 'layer-group' : 'circle' }));
}

export function complete(text: string): Item[] {
  const raw = text.replace(/^:/, '');
  const parts = raw.split(/\s+/);
  const head = parts[0].toLowerCase();
  if (parts.length === 1) {
    const q = head;
    const out: Item[] = [];
    for (const c of CMDS) {
      const best = rank(q, c.names, (n) => n)[0];
      if (best !== undefined) out.push({ label: q ? best : c.names[0], insert: q ? best : c.names[0], caption: c.caption, icon: c.icon });
    }
    return out
      .map((it, n) => ({ it, s: score(q, it.label), n }))
      .sort((a, b) => a.s - b.s || a.n - b.n)
      .map((x) => x.it);
  }
  const cmd = CMDS.find((c) => c.names.includes(head)) ?? CMDS.find((c) => c.names.some((n) => n.startsWith(head)));
  if (!cmd) return [];
  const last = parts[parts.length - 1].toLowerCase();
  const prefix = parts.slice(0, -1).join(' ');
  if (cmd.names[0] === 'ctx') {
    return ctxItems(last).map((it) => ({ ...it, insert: `${prefix} ${it.label}` }));
  }
  if (cmd.names[0] === 'ns' || cmd.view) {
    if (parts.length > 2) return [];
    return nsItems(cmd.names[0], last);
  }
  return [];
}

function resolveCluster(tok: string): string[] | null {
  const t = tok.toLowerCase();
  const g = GROUPS.find((x) => x === t) ?? GROUPS.find((x) => x.startsWith(t));
  const c = CLUSTERS.find((x) => x.id === t) ?? CLUSTERS.find((x) => x.id.startsWith(t));
  if (CLUSTERS.some((x) => x.id === t)) return [t];
  if (g && GROUPS.includes(t as never)) return CLUSTERS.filter((x) => x.group === g).map((x) => x.id);
  if (c) return [c.id];
  if (g) return CLUSTERS.filter((x) => x.group === g).map((x) => x.id);
  return null;
}
function resolveNs(tok: string): string | null {
  const t = tok.toLowerCase();
  if (t === 'all' || t === '*') return '';
  return NAMESPACES.find((n) => n === t) ?? NAMESPACES.find((n) => n.startsWith(t)) ?? null;
}

/** `picked` is the highlighted completion the user chose with arrows/Tab, if any. */
export function parse(text: string, top?: Item): Parsed {
  let raw = text.replace(/^:/, '').trim();
  if (!raw) return { ok: false, msg: 'Type a command, for example po, deploy or ctx.' };
  const trailingSpace = /\s$/.test(text);
  if (top && !trailingSpace) raw = top.insert;
  const parts = raw.split(/\s+/);
  const head = parts[0].toLowerCase();
  const cmd = CMDS.find((c) => c.names.includes(head)) ?? CMDS.find((c) => c.names.some((n) => n.startsWith(head)));
  if (!cmd) return { ok: false, msg: `Unknown command: ${parts[0]}` };
  const args = parts.slice(1);
  const id = cmd.names[0];
  if (id === 'q') return { ok: true, action: { t: 'quit' } };
  if (id === 'ctx') {
    if (!args.length) return { ok: true, action: { t: 'ctxOpen' } };
    const mode = args.every((a) => a[0] === '+') ? 'add' : args.every((a) => a[0] === '-') ? 'rm' : 'set';
    const ids: string[] = [];
    for (const a of args) {
      const r = resolveCluster(mode === 'set' ? a : a.slice(1));
      if (!r) return { ok: false, msg: `Unknown cluster or group: ${a.replace(/^[+-]/, '')}` };
      ids.push(...r);
    }
    return { ok: true, action: { t: 'ctx', mode, ids } };
  }
  if (id === 'overview') return { ok: true, action: { t: 'overview' } };
  if (id === 'ip') {
    return args.length ? { ok: true, action: { t: 'ip', q: args[0] } } : { ok: false, msg: 'Usage: ip <address>, for example ip 10.20.' };
  }
  if (id === 'ns') {
    if (!args.length) return { ok: false, msg: 'Usage: ns <namespace> or ns all' };
    const n = resolveNs(args[0]);
    return n === null ? { ok: false, msg: `Unknown namespace: ${args[0]}` } : { ok: true, action: { t: 'ns', ns: n } };
  }
  if (cmd.view) {
    if (!args.length) return { ok: true, action: { t: 'view', view: cmd.view } };
    const n = resolveNs(args[0]);
    return n === null ? { ok: false, msg: `Unknown namespace: ${args[0]}` } : { ok: true, action: { t: 'view', view: cmd.view, ns: n } };
  }
  return { ok: false, msg: `Unknown command: ${parts[0]}` };
}
