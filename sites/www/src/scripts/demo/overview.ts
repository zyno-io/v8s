/**
 * The demo's Overview: one card per selected cluster, like the app's Overview page. Every number is derived from the
 * demo's synthetic data: pod states and problems from the fleet, node allocatable and usage from usage.ts (the same
 * values the Nodes table shows, drifting with the same tick), requests and limits summed over the cluster's pods once.
 * Chips and counts carry data-ov so the app can jump to the filtered table like the real page does.
 */
import { CLUSTERS, SEVERITY, ST_CRASH, ST_DONE, ST_ERROR, ST_IMGPULL, ST_OOM, ST_PENDING, ST_CREATING, ST_RUNNING, getFleet, fmtN } from './data';
import { clock, esc } from './views';
import { nodeUse, workloadRes } from './usage';

interface Totals {
  pods: number; running: number; failing: number; pending: number; completed: number;
  cpuReq: number; cpuLim: number; memReq: number; memLim: number; // millicores, MiB
}
let totals: Totals[] | null = null;
function podTotals(): Totals[] {
  if (totals) return totals;
  const f = getFleet();
  const t: Totals[] = CLUSTERS.map(() => ({ pods: 0, running: 0, failing: 0, pending: 0, completed: 0, cpuReq: 0, cpuLim: 0, memReq: 0, memLim: 0 }));
  for (let i = 0; i < f.podCount; i++) {
    const c = t[f.cl[i]];
    const st = f.st[i];
    c.pods++;
    if (st === ST_RUNNING) c.running++;
    else if (st === ST_CRASH || st === ST_IMGPULL || st === ST_OOM || st === ST_ERROR) c.failing++;
    else if (st === ST_PENDING || st === ST_CREATING) c.pending++;
    else if (st === ST_DONE) c.completed++;
    if (st === ST_DONE) continue; // finished pods hold no requests
    const r = workloadRes(f.wl[i]);
    c.cpuReq += r.cReq; c.cpuLim += r.cLim || r.cReq; c.memReq += r.mReq; c.memLim += r.mLim || r.mReq;
  }
  return (totals = t);
}

const tone = (p: number) => (p >= 100 ? 'danger' : p >= 90 ? 'warning' : '');
const pctText = (v: number) => (v < 10 ? v.toFixed(1) : String(Math.round(v)));
const chip = (label: string, t: string, ov?: string) =>
  ov ? `<button type="button" class="vd-tag vd-t-${t} vd-ovt" data-ov="${ov}">${label}</button>` : `<span class="vd-tag vd-t-${t}">${label}</span>`;
const link = (label: string, ov: string, cls = '') => `<button type="button" class="vd-ovl${cls ? ' ' + cls : ''}" data-ov="${ov}">${label}</button>`;

function band(label: string, used: number, req: number, lim: number, alloc: number, amount: string): string {
  const pct = (used / alloc) * 100;
  const rq = (req / alloc) * 100;
  const lm = (lim / alloc) * 100;
  const t = tone(Math.round(pct));
  return `<div class="vd-ovc"><div class="vd-lbl">${label}</div>
    <div class="vd-ovbig${t ? ' vd-ov-' + t : ''}">${Math.round(pct)}%<span class="vd-dim"> used</span></div>
    <div class="vd-ovbar" role="img" aria-label="${label} ${Math.round(pct)}% used"><i class="${t ? 'vd-ov-' + t : ''}" style="width:${Math.min(100, pct)}%"></i><b style="left:${Math.min(100, rq)}%"></b></div>
    <div class="vd-ovsub">requests ${pctText(rq)}% · limits ${pctText(lm)}%</div>
    <div class="vd-ovsub vd-dim">${amount}</div></div>`;
}

export function overviewHtml(mask: boolean[]): string {
  const f = getFleet();
  const tot = podTotals();
  let html = '';
  CLUSTERS.forEach((c, k) => {
    if (!mask[k]) return;
    const pt = tot[k];
    let nodes = 0, ready = 0, version = '', cpuUsed = 0, memUsed = 0, cpuAlloc = 0, memAlloc = 0;
    f.nodes.forEach((n, i) => {
      if (n.cl !== k) return;
      nodes++;
      if (!version) version = n.version;
      const u = nodeUse(n, i, clock.tick);
      cpuAlloc += u.al.cpu; memAlloc += u.al.mem;
      if (n.status === 'NotReady') return;
      if (n.status === 'Ready') ready++;
      cpuUsed += u.cpu; memUsed += u.mem;
    });
    const sev = [0, 0, 0, 0];
    let policy = false, security = false;
    for (const p of f.problems) {
      if (p.cl !== k) continue;
      sev[p.sev]++;
      if (p.family === 'Policy') policy = true;
      if (p.family === 'Security') security = true;
    }
    const total = sev.reduce((a, b) => a + b, 0);
    const ks = f.ks.filter((x) => x.cl === k);
    const ksFail = ks.filter((x) => x.status === 'Not ready').length;
    const hm = f.helm.filter((x) => x.cl === k);
    const hmFail = hm.filter((x) => x.status === 'failed').length;
    const health = (label: string, n: number, fail: number, view: string) =>
      `<li>${link(label, `${view}:`)}<span class="vd-dim">${fmtN(n)}</span>${fail ? chip(`${fail} failing`, 'danger', `${view}:2`) : chip('healthy', 'success')}</li>`;
    const integ = ['Kyverno', policy ? 1 : 0, 'Trivy', security ? 1 : 0, 'Kubescape', security ? 1 : 0, 'Flux', 1, 'Helm', 1];
    const chips: string[] = [];
    for (let i = 0; i < integ.length; i += 2) if (integ[i + 1]) chips.push(`<span class="vd-tag">${integ[i]}</span>`);
    html += `<section class="vd-ovcard" aria-label="Cluster ${c.id}">
      <header class="vd-ovhd"><span class="vd-dot" style="background:var(--dot-ok)"></span><h3>${c.id}</h3><span class="vd-tag vd-t-success"><span class="vd-dot" style="background:var(--dot-ok)"></span>Connected</span><span class="vd-dim vd-mono">${version}</span></header>
      <div class="vd-ovgrid">
        <div class="vd-ovc"><div class="vd-lbl">Nodes</div><div class="vd-ovbig">${link(String(ready), `nodes:${k}`)}<span class="vd-dim"> / ${nodes} ready</span></div></div>
        <div class="vd-ovc"><div class="vd-lbl">Pods</div><div class="vd-ovbig">${link(fmtN(pt.pods), `pods:${k}:`)}</div>
          <div class="vd-ovchips">${chip(`${fmtN(pt.running)} running`, 'success', `pods:${k}:0`)}${pt.failing ? chip(`${fmtN(pt.failing)} failing`, 'danger', `pods:${k}:2`) : ''}${pt.pending ? chip(`${fmtN(pt.pending)} pending`, 'warning', `pods:${k}:1`) : ''}${pt.completed ? chip(`${fmtN(pt.completed)} completed`, 'info') : ''}</div></div>
        <div class="vd-ovc"><div class="vd-lbl">Problems</div><div class="vd-ovbig">${link(total ? fmtN(total) : 'None', `problems:${k}`, total ? '' : 'vd-ok')}</div>
          <div class="vd-ovchips">${sev.map((n, s) => (n ? chip(`${n} ${SEVERITY[s]}`, s === 2 ? 'warning' : s === 3 ? 'info' : 'danger') : '')).join('')}</div></div>
        <div class="vd-ovc"><div class="vd-lbl">GitOps health</div><ul class="vd-ovhl">${health('Flux Kustomizations', ks.length, ksFail, 'ks')}${health('Helm releases', hm.length, hmFail, 'helm')}</ul></div>
        <div class="vd-ovc"><div class="vd-lbl">Integrations</div><div class="vd-ovchips">${chips.join('')}</div></div>
      </div>
      <div class="vd-ovgrid vd-ovcap">
        ${band('CPU', cpuUsed, pt.cpuReq, pt.cpuLim, cpuAlloc, `${esc((cpuUsed / 1000).toFixed(0))} of ${esc((cpuAlloc / 1000).toFixed(0))} cores`)}
        ${band('Memory', memUsed, pt.memReq, pt.memLim, memAlloc, `${(memUsed / 1024).toFixed(0)} of ${(memAlloc / 1024).toFixed(0)} GiB`)}
      </div>
    </section>`;
  });
  return html;
}
