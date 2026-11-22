/** Detail drawer: Overview, YAML (with edit + dry-run diff), Logs (streaming), Events. */
import { CLUSTERS } from './data';
import { esc, tag } from './views';
import { makeLogger, type LogLine, type Subject } from './subject';

export type Tab = 'overview' | 'yaml' | 'logs' | 'events';
const TABS: [Tab, string][] = [['overview', 'Overview'], ['yaml', 'YAML'], ['logs', 'Logs'], ['events', 'Events']];

function hlYaml(line: string): string {
  const m = /^(\s*)(- )?([A-Za-z0-9_.\/-]+)(:)(.*)$/.exec(line);
  if (!m) return `<span class="vd-yv">${esc(line)}</span>`;
  const v = m[5];
  const t = v.trim();
  const cls = /^"/.test(t) ? 'vd-ys' : /^-?\d/.test(t) ? 'vd-yn' : /^(true|false)$/.test(t) ? 'vd-yb' : 'vd-yv';
  return `${m[1]}${m[2] ?? ''}<span class="vd-yk">${esc(m[3])}</span>:<span class="${cls}">${esc(v)}</span>`;
}

export class Drawer {
  subject: Subject | null = null;
  tab: Tab = 'overview';
  mode: 'view' | 'edit' | 'review' = 'view';
  follow = true;
  private replicas = 0;
  private timer = 0;
  private logs: LogLine[] = [];
  private next: (() => LogLine) | null = null;
  private logEl: HTMLElement | null = null;

  onClose: () => void = () => {};
  onToast: (m: string) => void = () => {};
  onAck: (problemId: number) => void = () => {};

  constructor(readonly el: HTMLElement) {
    el.addEventListener('click', (e) => {
      const t = (e.target as HTMLElement).closest<HTMLElement>('[data-dr]');
      if (!t) return;
      const a = t.dataset.dr!;
      if (a === 'close') this.onClose();
      else if (a === 'tab') this.setTab(t.dataset.tab as Tab);
      else if (a === 'follow') {
        this.follow = !this.follow;
        this.renderPanel();
      } else if (a === 'edit') this.startEdit();
      else if (a === 'review') this.mode = 'review', this.renderPanel();
      else if (a === 'back') this.mode = 'edit', this.renderPanel();
      else if (a === 'cancel') this.mode = 'view', this.renderPanel();
      else if (a === 'apply') {
        this.mode = 'view';
        this.onToast('Demo: nothing was changed');
        this.renderPanel();
      } else if (a === 'ack' && this.subject?.problem) {
        this.onAck(this.subject.problem.id);
        this.onClose();
      }
    });
    el.addEventListener('input', (e) => {
      const t = e.target as HTMLInputElement;
      if (t.dataset.dr === 'replicas') {
        this.replicas = Math.max(0, Math.min(9999, parseInt(t.value || '0', 10) || 0));
        const b = el.querySelector<HTMLButtonElement>('[data-dr="review"]');
        if (b) b.disabled = this.replicas === this.subject!.replicas;
      }
    });
    el.addEventListener('keydown', (e) => {
      const t = (e.target as HTMLElement).closest('[role="tab"]');
      if (!t || (e.key !== 'ArrowRight' && e.key !== 'ArrowLeft')) return;
      const i = TABS.findIndex(([k]) => k === this.tab);
      const n = (i + (e.key === 'ArrowRight' ? 1 : TABS.length - 1)) % TABS.length;
      this.setTab(TABS[n][0]);
      el.querySelector<HTMLElement>(`[data-tab="${TABS[n][0]}"]`)?.focus();
      e.preventDefault();
    });
    el.querySelector('.vd-logs')?.addEventListener('scroll', () => {});
  }

  get isOpen() {
    return !this.el.hidden;
  }

  open(s: Subject, tab: Tab = 'overview', edit = false) {
    this.subject = s;
    this.tab = tab;
    this.mode = 'view';
    this.follow = true;
    this.logs = [];
    this.next = makeLogger(s);
    for (let i = 0; i < 36; i++) this.logs.push(this.next());
    this.el.hidden = false;
    const cl = CLUSTERS[s.cl];
    this.el.querySelector('.vd-dr-meta .vd-dr-ctx')!.innerHTML =
      `<span class="vd-tag"><span class="vd-dot" style="background:var(--dot-ok)"></span>${cl.id}</span><span class="vd-dim">${esc(s.kind)}${s.ns ? ` · ${esc(s.ns)}` : ''}</span>`;
    this.el.querySelector('.vd-dr-title')!.innerHTML = `<h2 class="vd-dr-h">${esc(s.name)}</h2>${tag(s.status, s.tone)}`;
    this.el.setAttribute('aria-label', `${s.kind} ${s.name} details`);
    this.renderTabs();
    if (edit) this.startEdit();
    else this.renderPanel();
  }

  close() {
    this.el.hidden = true;
    this.stop();
    this.subject = null;
  }

  setTab(t: Tab) {
    this.tab = t;
    if (t !== 'yaml') this.mode = 'view';
    this.renderTabs();
    this.renderPanel();
  }

  startEdit() {
    const s = this.subject!;
    if (s.editLine < 0) {
      this.onToast('Demo: this type has no editable field here');
      return;
    }
    this.tab = 'yaml';
    this.mode = 'edit';
    this.replicas = s.replicas;
    this.renderTabs();
    this.renderPanel();
    this.el.querySelector<HTMLInputElement>('[data-dr="replicas"]')?.focus();
  }

  private renderTabs() {
    this.el.querySelector('.vd-tabs')!.innerHTML = TABS.map(
      ([k, l]) =>
        `<button type="button" role="tab" id="vd-tab-${k}" class="vd-tab${this.tab === k ? ' vd-on' : ''}" data-dr="tab" data-tab="${k}" aria-selected="${this.tab === k}" aria-controls="vd-panel" tabindex="${this.tab === k ? 0 : -1}">${l}</button>`,
    ).join('');
  }

  private stop() {
    if (this.timer) clearInterval(this.timer);
    this.timer = 0;
    this.logEl = null;
  }

  private renderPanel() {
    const s = this.subject;
    const p = this.el.querySelector<HTMLElement>('.vd-panel')!;
    if (!s) return;
    p.setAttribute('aria-labelledby', `vd-tab-${this.tab}`);
    this.stop();
    if (this.tab === 'overview') p.innerHTML = this.overview(s);
    else if (this.tab === 'yaml') p.innerHTML = this.yaml(s);
    else if (this.tab === 'events') p.innerHTML = this.events(s);
    else this.logsPanel(p);
  }

  private overview(s: Subject): string {
    let h = '';
    if (s.problem) {
      h += `<div class="vd-notice vd-t-${s.problem.sev === 3 ? 'info' : s.problem.sev === 2 ? 'warning' : 'danger'}" role="note"><div><b>${esc(s.problem.cls)}</b> · ${esc(s.problem.message)}</div><button type="button" class="vd-btn vd-sm" data-dr="ack"><i class="fa-solid fa-check" aria-hidden="true"></i>Acknowledge</button></div>`;
    }
    h += `<dl class="vd-facts"><dt>Status</dt><dd>${tag(s.status, s.tone)}</dd><dt>Cluster</dt><dd>${CLUSTERS[s.cl].id}</dd>`;
    if (s.ns) h += `<dt>Namespace</dt><dd>${esc(s.ns)}</dd>`;
    for (const [k, v] of s.facts) h += `<dt>${esc(k)}</dt><dd${/^[\d./:a-z-]+$/i.test(v) && v.length < 60 && /[\d.]/.test(v) ? ' class="vd-mono"' : ''}>${esc(v)}</dd>`;
    h += '</dl>';
    if (s.conditions.length) {
      h += `<h3 class="vd-lbl">Conditions</h3><table class="vd-ct"><thead><tr><th>Type</th><th>Status</th><th>Reason</th><th class="vd-r">Since</th></tr></thead><tbody>`;
      for (const c of s.conditions) h += `<tr><td>${esc(c.type)}</td><td>${tag(c.ok ? 'True' : 'False', c.ok ? 'success' : 'danger')}</td><td>${esc(c.reason) || '<span class="vd-dim">—</span>'}</td><td class="vd-r">${esc(c.since)}</td></tr>`;
      h += '</tbody></table>';
    }
    if (s.containers.length) {
      h += `<h3 class="vd-lbl">Containers <span class="vd-n">${s.containers.length}</span></h3><ul class="vd-cs">`;
      for (const c of s.containers)
        h += `<li><div class="vd-cs-top"><b>${esc(c.name)}</b>${tag(c.state, c.ready || c.state === 'Template' ? (c.state === 'Template' ? 'neutral' : 'success') : /Completed/.test(c.state) ? 'neutral' : 'danger')}${c.restarts ? `<span class="vd-dim">${c.restarts} restarts</span>` : ''}</div><div class="vd-mono vd-dim">${esc(c.image)}</div></li>`;
      h += '</ul>';
    }
    h += `<h3 class="vd-lbl">Labels <span class="vd-n">${s.labels.length}</span></h3><div class="vd-labels">${s.labels.map(([k, v]) => `<span class="vd-tag vd-mono">${esc(k)} = ${esc(v)}</span>`).join('')}</div>`;
    return h;
  }

  private yaml(s: Subject): string {
    const canEdit = s.editLine >= 0 || !!s.owner;
    let bar: string;
    if (this.mode === 'view')
      bar = `<span class="vd-dim">Read-only manifest</span><button type="button" class="vd-btn vd-sm" data-dr="edit"${canEdit ? '' : ' title="Not editable in the demo"'}><i class="fa-solid fa-pen" aria-hidden="true"></i>Edit <kbd class="vd-kbd">e</kbd></button>`;
    else if (this.mode === 'edit')
      bar = `<span class="vd-dim">Editing <span class="vd-mono">spec.replicas</span></span><span class="vd-grow"></span><button type="button" class="vd-btn vd-sm" data-dr="cancel">Cancel</button><button type="button" class="vd-btn vd-sm vd-primary" data-dr="review"${this.replicas === s.replicas ? ' disabled' : ''}>Review changes</button>`;
    else
      bar = `${tag('Server-side dry run passed', 'success')}<span class="vd-grow"></span><button type="button" class="vd-btn vd-sm" data-dr="back">Back</button><button type="button" class="vd-btn vd-sm vd-primary" data-dr="apply">Apply</button>`;
    if (this.mode === 'review') {
      return `<div class="vd-ybar">${bar}</div><div class="vd-diff vd-mono" role="group" aria-label="Diff"><div class="vd-dl vd-ctx">  spec:</div><div class="vd-dl vd-del"><span class="vd-dm">-</span>  replicas: ${s.replicas}</div><div class="vd-dl vd-add"><span class="vd-dm">+</span>  replicas: ${this.replicas}</div></div><p class="vd-dim vd-note">Nothing is sent to a cluster in this demo.</p>`;
    }
    const lines = s.yaml
      .map((l, i) => {
        const body =
          this.mode === 'edit' && i === s.editLine
            ? `  <span class="vd-yk">replicas</span>: <input class="vd-ein" data-dr="replicas" type="number" min="0" max="9999" value="${this.replicas}" aria-label="spec.replicas">`
            : hlYaml(l);
        return `<div class="vd-yl${this.mode === 'edit' && i === s.editLine ? ' vd-edit' : ''}"><span class="vd-ln">${i + 1}</span><span class="vd-ly">${body}</span></div>`;
      })
      .join('');
    return `<div class="vd-ybar">${bar}</div><div class="vd-yaml vd-mono">${lines}</div>`;
  }

  private events(s: Subject): string {
    return `<table class="vd-ct vd-ev"><thead><tr><th>Type</th><th>Reason</th><th>Age</th><th>Message</th></tr></thead><tbody>${s.events
      .map((e) => `<tr><td>${tag(e.type, e.type === 'Warning' ? 'warning' : 'neutral')}</td><td>${esc(e.reason)}</td><td>${esc(e.age)}</td><td class="vd-evm">${esc(e.msg)}</td></tr>`)
      .join('')}</tbody></table>`;
  }

  private lineHtml(l: LogLine): string {
    return `<div class="vd-ll vd-l-${l.level}"><span class="vd-lt">${l.ts.slice(11)}</span><span class="vd-lm">${esc(l.text)}</span></div>`;
  }

  private logsPanel(p: HTMLElement) {
    p.innerHTML = `<div class="vd-ybar"><span class="vd-live"><span class="vd-dot" style="background:var(--dot-ok)"></span>Streaming</span><span class="vd-dim vd-mono">${esc(this.subject!.containers[0]?.name ?? 'app')}</span><span class="vd-grow"></span><button type="button" class="vd-btn vd-sm${this.follow ? ' vd-pressed' : ''}" data-dr="follow" aria-pressed="${this.follow}"><i class="fa-solid fa-arrow-down-long" aria-hidden="true"></i>Follow</button></div><div class="vd-logs vd-mono" role="log" aria-label="Log stream" tabindex="0">${this.logs.map((l) => this.lineHtml(l)).join('')}</div>`;
    const el = (this.logEl = p.querySelector('.vd-logs')!);
    el.scrollTop = el.scrollHeight;
    el.addEventListener('scroll', () => {
      const atEnd = el.scrollHeight - el.scrollTop - el.clientHeight < 24;
      if (this.follow && !atEnd) {
        this.follow = false;
        p.querySelector('[data-dr="follow"]')?.classList.remove('vd-pressed');
        p.querySelector('[data-dr="follow"]')?.setAttribute('aria-pressed', 'false');
      }
    }, { passive: true });
    this.timer = window.setInterval(() => {
      if (!this.next || !this.logEl) return;
      const l = this.next();
      this.logs.push(l);
      if (this.logs.length > 200) this.logs.shift();
      this.logEl.insertAdjacentHTML('beforeend', this.lineHtml(l));
      while (this.logEl.childElementCount > 200) this.logEl.firstElementChild!.remove();
      if (this.follow) this.logEl.scrollTop = this.logEl.scrollHeight;
    }, 400);
  }
}
