/** Hydrates the demo: state, table wiring, command bar, cluster menu, drawer, phone layout. */
import { CLUSTERS, FAMILIES, GROUPS, fmtN, getFleet } from './data';
import { overviewHtml } from './overview';
import { complete, parse, type Action, type Item } from './commands';
import { Drawer, type Tab } from './drawer';
import { subjectFor, subjectForProblem } from './subject';
import { VTable } from './table';
import {
  cardInner, chipsHtml, getSource, gridCols, headHtml, headerRowInner, problemItems, rowInner, runPipeline, esc, clock, isIpQuery,
  type Filter, type Source, type ViewId,
} from './views';

declare global {
  interface Window { v8sDemo?: { run(cmd: string): void } }
}

const reduceMotion = () => matchMedia('(prefers-reduced-motion: reduce)').matches;
const PHONE_MAX = 760;

export function init() {
  const root = document.getElementById('demo');
  if (!root || root.dataset.ready) return;
  root.dataset.ready = '1';
  const $ = <T extends HTMLElement = HTMLElement>(s: string) => root.querySelector<T>(s)!;
  const fleet = getFleet();

  // ---- state ---------------------------------------------------------------
  let view: ViewId = 'pods';
  let phone = false;
  const mask = [true, true, true];
  let ns = '';
  let q = '';
  let bucket = -1;
  const sorts: Record<string, { key: string; dir: 1 | -1 }> = {};
  const sortOf = (v: ViewId) => (sorts[v] ??= { key: 'name', dir: 1 });
  let order: ArrayLike<number> = new Uint32Array(0);
  let counts: number[] = [];
  const selected = new Set<number>();
  const acked = new Set<number>();
  let showAck = false;
  let group = false;
  let ackedCount = 0;
  let interacted = false;
  let ovOn = false;
  let src: Source = getSource('pods');

  // ---- elements -------------------------------------------------------------
  const scroller = $('.vd-scroller');
  const sizer = $('.vd-sizer');
  const thRow = $('.vd-th');
  const ovEl = $('.vd-ov');
  const focusList = () => (ovOn ? ovEl : scroller).focus({ preventScroll: true });
  const filterIn = $<HTMLInputElement>('#vd-filter');
  const toast = $('.vd-toast');
  const drawer = new Drawer($('.vd-drawer'));

  const isHeader = (i: number) => src.id === 'problems' && (order[i] as number) < 0;
  const table = new VTable(scroller, sizer, {
    isHeader,
    rowClass: (i) => {
      const id = order[i] as number;
      return `${selected.has(id) ? 'vd-sel ' : ''}${i === table.focus ? 'vd-focus ' : ''}${src.id === 'problems' && id >= 0 && acked.has(fleet.problems[id].id) ? 'vd-acked' : ''}`;
    },
    inner: (i) => {
      const id = order[i] as number;
      if (isHeader(i)) {
        const fi = -id - 1;
        let n = 0;
        for (let j = i + 1; j < order.length && (order[j] as number) >= 0; j++) n++;
        return headerRowInner(fi, n);
      }
      return phone ? cardInner(src, id, selected.has(id)) : rowInner(src, id, selected.has(id));
    },
  });
  sizer.textContent = '';

  // ---- helpers ----------------------------------------------------------------
  const filter = (): Filter => ({ mask, ns, q, bucket });
  let toastTimer = 0;
  function showToast(msg: string) {
    toast.textContent = msg;
    toast.classList.add('vd-show');
    clearTimeout(toastTimer);
    toastTimer = window.setTimeout(() => toast.classList.remove('vd-show'), 2600);
  }
  drawer.onToast = showToast;

  function badge() {
    const n = fleet.problems.filter((p) => mask[p.cl] && !acked.has(p.id)).length;
    $('[data-v="pbadge"]').textContent = String(n);
    root!.querySelectorAll('[data-v="sidepb"]').forEach((e) => (e.textContent = String(n)));
    $('.vd-problems').setAttribute('aria-label', `${n} problems`);
  }
  function sideCounts() {
    const c = (id: ViewId) => {
      const s = getSource(id);
      let n = 0;
      for (let i = 0; i < s.count; i++) if (mask[s.cl(i)]) n++;
      return fmtN(n);
    };
    let pods = 0;
    for (let k = 0; k < 3; k++) if (mask[k]) pods += podPerCluster[k];
    root!.querySelectorAll<HTMLElement>('[data-count]').forEach((e) => {
      const v = e.dataset.count as ViewId;
      e.textContent = v === 'pods' ? fmtN(pods) : c(v);
    });
  }
  const podPerCluster = [0, 0, 0];
  for (let i = 0; i < fleet.podCount; i++) podPerCluster[fleet.cl[i]]++;

  function clusterLabel() {
    const ids = CLUSTERS.filter((_, k) => mask[k]);
    return ids.length === 1 ? ids[0].id : `${ids.length} clusters`;
  }

  function updateCount() {
    if (ovOn) return;
    const n = order.length - (src.id === 'problems' && group ? FAMILIES.length : 0);
    const shown = src.id === 'problems' ? Math.max(0, countItems()) : n;
    $('[data-v="count"]').innerHTML = `<b>${fmtN(shown)}</b> ${src.noun}<span class="vd-sep"> · </span><span class="vd-domc">${table.domRows} in DOM</span>`;
  }
  function countItems() {
    let n = 0;
    for (let i = 0; i < order.length; i++) if ((order[i] as number) >= 0) n++;
    return n;
  }
  table.onRender = () => updateCount();

  /** After a rebuild removed or hid the focused control, put focus back on its twin, else on the grid. */
  function focusSel(el: Element | null): string {
    const h = el as HTMLElement | null;
    if (!h || !h.dataset) return '';
    if (h.dataset.chip != null) return `[data-chip="${h.dataset.chip}"]`;
    if (h.dataset.x) return `[data-x="${h.dataset.x}"]`;
    return '';
  }
  function restoreFocus(sel: string) {
    const a = document.activeElement as HTMLElement | null;
    if (a && a !== document.body && root!.contains(a) && a.isConnected && !a.closest('[hidden]')) return;
    const el = sel ? root!.querySelector<HTMLElement>(sel) : null;
    (el && !el.closest('[hidden]') ? el : scroller).focus({ preventScroll: true });
  }

  function bulk() {
    const b = $('.vd-bulk');
    const n = selected.size;
    b.hidden = n === 0;
    $('[data-v="bulkn"]').textContent = `${fmtN(n)} selected`;
  }

  function extras() {
    const e = $('[data-v="extra"]');
    if (src.id !== 'problems') return void (e.innerHTML = '');
    e.innerHTML = `<button type="button" class="vd-btn vd-sm${group ? ' vd-pressed' : ''}" data-x="group" aria-pressed="${group}"><i class="fa-solid fa-layer-group" aria-hidden="true"></i>Group by family</button><button type="button" class="vd-btn vd-sm${showAck ? ' vd-pressed' : ''}" data-x="ack" aria-pressed="${showAck}">Show acknowledged (${ackedCount})</button>`;
  }

  function chrome() {
    const s = sortOf(view);
    $('.vd-title').textContent = ovOn ? 'Overview' : src.title;
    thRow.innerHTML = headHtml(src, s.key, s.dir);
    scroller.style.setProperty('--cols', gridCols(src));
    scroller.style.setProperty('--minw', `${src.minW}px`);
    scroller.setAttribute('aria-label', src.title);
    root!.dataset.view = ovOn ? 'overview' : view;
    ovEl.hidden = !ovOn;
    root!.querySelectorAll<HTMLElement>('[data-nav]').forEach((a) => {
      const on = ovOn ? a.dataset.nav === 'overview' : a.dataset.nav === view;
      a.classList.toggle('vd-active', on);
      if (on) a.setAttribute('aria-current', 'page');
      else a.removeAttribute('aria-current');
    });
    filterIn.placeholder = src.id === 'problems' ? 'Search by object or message' : (src.id === 'pods' ? 'Filter by name, namespace or IP' : 'Filter by name or namespace');
    const nsChip = $('.vd-nschip');
    nsChip.hidden = !ns;
    $('[data-v="nsname"]').textContent = ns;
  }

  /** Recompute the visible rows and repaint. */
  function compute(reset = false) {
    if (ovOn) {
      ovEl.innerHTML = overviewHtml(mask);
      const n = mask.filter(Boolean).length;
      $('[data-v="count"]').innerHTML = `<b>${n}</b> cluster${n === 1 ? '' : 's'}`;
      table.set(0, { rowH: phone ? 72 : 32, headH: phone ? 0 : 32, card: phone, reset: true });
      return;
    }
    if (src.setIpMode?.(isIpQuery(q))) chrome();
    if (src.id === 'problems') {
      const r = problemItems(filter(), acked, showAck, group);
      order = r.items;
      counts = r.counts;
      ackedCount = r.ackedCount;
      // Chip counts exclude the severity filter itself: recount without it.
      if (bucket >= 0) counts = problemItems({ ...filter(), bucket: -1 }, acked, showAck, group).counts;
    } else {
      const s = sortOf(view);
      const r = runPipeline(src, s.key, s.dir, filter());
      order = r.ids;
      counts = r.counts;
    }
    $('[data-v="chips"]').innerHTML = chipsHtml(src, counts, bucket);
    extras();
    table.set(order.length, { rowH: phone ? 72 : 32, headH: phone ? 0 : 32, card: phone, reset });
    if (table.focus >= order.length) table.focus = -1;
    if (table.focus >= 0 && isHeader(table.focus)) table.focus = -1;
    table.refresh();
    const empty = $('.vd-empty');
    empty.hidden = order.length > 0;
    scroller.classList.toggle('vd-hide', order.length === 0 && false);
    badge();
    bulk();
    updateCount();
  }

  function showOverview() {
    closeDrawer();
    ovOn = true;
    selected.clear();
    table.focus = -1;
    chrome();
    compute(true);
  }
  function setView(v: ViewId, opts: { keepFilters?: boolean } = {}) {
    ovOn = false;
    if (v !== view || !opts.keepFilters) {
      q = '';
      filterIn.value = '';
      bucket = -1;
    }
    view = v;
    src = getSource(v);
    selected.clear();
    table.focus = -1;
    chrome();
    compute(true);
  }

  // ---- table interaction -----------------------------------------------------------
  function firstRow(from: number, step: 1 | -1): number {
    for (let i = from; i >= 0 && i < order.length; i += step) if (!isHeader(i)) return i;
    return -1;
  }
  function move(delta: number) {
    if (!order.length) return;
    let i = table.focus < 0 ? (delta > 0 ? 0 : order.length - 1) : table.focus + delta;
    i = Math.max(0, Math.min(order.length - 1, i));
    if (isHeader(i)) i = firstRow(i, delta >= 0 ? 1 : -1);
    if (i < 0) return;
    table.setFocus(i);
    if (drawer.isOpen) openDrawer(i, drawer.tab === 'yaml' ? 'yaml' : drawer.tab, false, true);
  }
  function toggleSel(i: number) {
    if (!src.hasCb || isHeader(i)) return;
    const id = order[i] as number;
    if (!selected.delete(id)) selected.add(id);
    const el = table.rowEl(i);
    if (el) {
      el.classList.toggle('vd-sel', selected.has(id));
      const cb = el.querySelector<HTMLInputElement>('.vd-cb');
      if (cb) cb.checked = selected.has(id);
    }
    bulk();
  }
  let drawerRow = -1;
  function openDrawer(i: number, tab: Tab = 'overview', edit = false, keepFocus = false) {
    if (i < 0 || isHeader(i)) return;
    const id = order[i] as number;
    const s = src.id === 'problems' ? subjectForProblem(fleet.problems[id]) : subjectFor(src.id, id);
    drawerRow = i;
    table.setFocus(i, false);
    root!.classList.add('vd-dopen');
    drawer.open(s, tab, edit);
    if (!keepFocus) root!.querySelector<HTMLElement>('.vd-drawer .vd-tab.vd-on')?.focus({ preventScroll: true });
  }
  function closeDrawer() {
    if (!drawer.isOpen) return;
    drawer.close();
    root!.classList.remove('vd-dopen');
    focusList();
    if (drawerRow >= 0) table.setFocus(Math.min(drawerRow, order.length - 1), false);
  }
  drawer.onClose = closeDrawer;
  drawer.onAck = (id) => {
    acked.add(id);
    compute();
    showToast('Acknowledged');
  };
  /** The object the row keys act on: the open drawer's, else the focused row's. */
  function actionSubject() {
    if (drawer.isOpen && drawer.subject) return drawer.subject;
    const i = table.focus;
    if (i < 0 || isHeader(i)) return null;
    const id = order[i] as number;
    return src.id === 'problems' ? subjectForProblem(fleet.problems[id]) : subjectFor(src.id, id);
  }
  function jumpToOwner() {
    const s = actionSubject();
    if (!s) return showToast('Focus a row first');
    if (!s.owner) return showToast('No owner');
    // The demo models Deployments but no ReplicaSets, so a pod jumps straight to its Deployment.
    const o = subjectFor(s.owner.view, s.owner.idx);
    root!.classList.add('vd-dopen');
    drawer.open(o, 'overview', false);
    root!.querySelector<HTMLElement>('.vd-drawer .vd-tab.vd-on')?.focus({ preventScroll: true });
    showToast(`Owner: Deployment/${o.name}`);
  }
  function copyField(what: 'name' | 'namespace') {
    const s = actionSubject();
    if (!s) return showToast('Focus a row first');
    const text = what === 'name' ? s.name : s.ns;
    if (!text) return showToast('Not namespaced');
    const done = () => showToast(`Copied ${what}`);
    try {
      navigator.clipboard.writeText(text).then(done, () => showToast('Could not copy: the clipboard is unavailable'));
    } catch {
      showToast('Could not copy: the clipboard is unavailable');
    }
  }
  function editFocused() {
    if (drawer.isOpen && drawer.subject) {
      const s = drawer.subject;
      if (s.owner) {
        const st = getSource(s.owner.view);
        void st;
        const o = subjectFor(s.owner.view, s.owner.idx);
        drawer.open(o, 'yaml', true);
        showToast('Editing the owning Deployment');
        return;
      }
      drawer.startEdit();
      return;
    }
    const i = table.focus;
    if (i < 0 || isHeader(i)) return;
    openDrawer(i, 'yaml', false);
    editFocused();
  }

  sizer.addEventListener('click', (e) => {
    const t = e.target as HTMLElement;
    const row = t.closest<HTMLElement>('[data-i]');
    if (!row) return;
    interact();
    const i = +row.dataset.i!;
    const ack = t.closest<HTMLElement>('[data-ack]');
    if (ack) {
      acked.add(+ack.dataset.ack!);
      compute();
      restoreFocus('');
      return;
    }
    if (isHeader(i)) return;
    table.setFocus(i, false);
    if (t.closest('.vd-cb')) toggleSel(i);
    else if (phone || t.closest('[data-open]')) openDrawer(i);
  });
  thRow.addEventListener('click', (e) => {
    const t = e.target as HTMLElement;
    const b = t.closest<HTMLElement>('[data-sort]');
    if (b && src.id !== 'problems') {
      const s = sortOf(view);
      const k = b.dataset.sort!;
      if (s.key === k) s.dir = s.dir === 1 ? -1 : 1;
      else (s.key = k), (s.dir = 1);
      chrome();
      compute(true);
      thRow.querySelector<HTMLElement>(`[data-sort="${k}"]`)?.focus();
    } else if (t.closest('[data-all]')) {
      const all = t.closest('[data-all]') as HTMLInputElement;
      if (all.checked) for (let i = 0; i < order.length; i++) selected.add(order[i] as number);
      else selected.clear();
      table.refresh();
      bulk();
    }
  });
  $('.vd-bulk').addEventListener('click', (e) => {
    const b = (e.target as HTMLElement).closest<HTMLElement>('[data-bulk]');
    if (!b) return;
    if (b.dataset.bulk === 'clear') {
      selected.clear();
      table.refresh();
      bulk();
      thRow.querySelectorAll<HTMLInputElement>('[data-all]').forEach((c) => (c.checked = false));
      restoreFocus('');
    } else showToast('Demo: nothing was changed');
  });
  filterIn.addEventListener('input', () => {
    interact();
    q = filterIn.value;
    compute(true);
  });
  $('.vd-tools').addEventListener('click', (e) => {
    const t = e.target as HTMLElement;
    const sel = focusSel(t.closest('[data-chip],[data-x]'));
    const chip = t.closest<HTMLElement>('[data-chip]');
    if (chip) {
      const k = +chip.dataset.chip!;
      bucket = bucket === k ? -1 : k;
      compute(true);
    } else if (t.closest('[data-x="group"]')) {
      group = !group;
      compute(true);
    } else if (t.closest('[data-x="ack"]')) {
      showAck = !showAck;
      compute(true);
    } else if (t.closest('.vd-nschip')) {
      ns = '';
      chrome();
      compute(true);
    } else return;
    restoreFocus(sel);
  });
  $('.vd-empty').addEventListener('click', () => {
    q = '';
    filterIn.value = '';
    bucket = -1;
    ns = '';
    chrome();
    compute(true);
    restoreFocus('');
  });
  root.querySelectorAll<HTMLElement>('[data-nav]').forEach((a) =>
    a.addEventListener('click', () => {
      interact();
      closeDrawer();
      if (a.dataset.nav === 'overview') showOverview();
      else setView(a.dataset.nav as ViewId);
    }),
  );
  $('[data-act="filter"]').addEventListener('click', () => {
    if (ovOn) setView('pods');
    filterIn.focus();
  });

  // Overview jumps, like the app: a count or chip opens the table with that filter.
  ovEl.addEventListener('click', (e) => {
    const b = (e.target as HTMLElement).closest<HTMLElement>('[data-ov]');
    if (!b) return;
    interact();
    const [kind, cl, bk] = b.dataset.ov!.split(':');
    if (kind === 'nodes' || kind === 'ks' || kind === 'helm') {
      setView(kind);
      if (kind === 'ks' || kind === 'helm') bucket = cl === '' ? -1 : +cl;
    } else if (kind === 'pods') {
      setView('pods');
      bucket = bk === '' || bk === undefined ? -1 : +bk;
    } else if (kind === 'problems') {
      setView('problems');
      applyMask(CLUSTERS.map((_, k) => k === +cl));
      return void focusList();
    }
    compute(true);
    focusList();
  });

  // ---- cluster menu -----------------------------------------------------------------
  const menu = $('.vd-cmenu');
  const cbtn = $<HTMLButtonElement>('#vd-cluster-btn');
  const draft = [true, true, true];
  function renderMenu() {
    menu.querySelectorAll<HTMLInputElement>('[data-cl]').forEach((c) => (c.checked = draft[+c.dataset.cl!]));
    menu.querySelectorAll<HTMLElement>('[data-grp]').forEach((g) => {
      const idx = CLUSTERS.map((c, k) => (c.group === g.dataset.grp ? k : -1)).filter((k) => k >= 0);
      const on = idx.every((k) => draft[k]) && CLUSTERS.every((c, k) => draft[k] === (c.group === g.dataset.grp));
      g.classList.toggle('vd-on', on);
      g.setAttribute('aria-pressed', String(on));
    });
    const n = draft.filter(Boolean).length;
    $('[data-v="msel"]').textContent = `${n} selected`;
    $<HTMLButtonElement>('[data-m="apply"]').disabled = n === 0;
  }
  function openMenu() {
    cmd.close(false);
    draft.splice(0, 3, ...mask);
    renderMenu();
    menu.hidden = false;
    cbtn.setAttribute('aria-expanded', 'true');
    menu.querySelector<HTMLElement>('[data-cl]')?.focus({ preventScroll: true });
  }
  function closeMenu(refocus = true) {
    if (menu.hidden) return;
    menu.hidden = true;
    cbtn.setAttribute('aria-expanded', 'false');
    if (refocus) (phone ? scroller : cbtn).focus({ preventScroll: true });
  }
  function applyMask(next: boolean[]) {
    mask.splice(0, 3, ...next);
    $('[data-v="clabel"]').textContent = clusterLabel();
    sideCounts();
    compute(true);
  }
  cbtn.addEventListener('click', () => (menu.hidden ? openMenu() : closeMenu()));
  menu.addEventListener('click', (e) => {
    const t = e.target as HTMLElement;
    const g = t.closest<HTMLElement>('[data-grp]');
    if (g) {
      const shift = (e as MouseEvent).shiftKey;
      CLUSTERS.forEach((c, k) => {
        if (c.group === g.dataset.grp) draft[k] = true;
        else if (!shift) draft[k] = false;
      });
      renderMenu();
    } else if (t.closest('[data-cl]')) {
      const c = t.closest<HTMLInputElement>('[data-cl]')!;
      draft[+c.dataset.cl!] = c.checked;
      renderMenu();
    } else if (t.closest('[data-m="apply"]')) {
      applyMask([...draft]);
      closeMenu();
    } else if (t.closest('[data-m="cancel"]')) closeMenu();
  });
  document.addEventListener('pointerdown', (e) => {
    if (!menu.hidden && !menu.contains(e.target as Node) && !cbtn.contains(e.target as Node)) closeMenu(false);
  });

  // ---- command bar --------------------------------------------------------------------
  const cmdEl = $('.vd-cmd');
  const cmdIn = $<HTMLInputElement>('.vd-cmd-in');
  const cmdList = $('.vd-cmd-list');
  const cmdMsg = $('.vd-cmd-msg');
  let items: Item[] = [];
  let sel = 0;
  let cmdReturn: HTMLElement | null = null;
  const cmd = {
    get isOpen() { return !cmdEl.hidden; },
    open(focus = true) {
      if (!menu.hidden) closeMenu(false);
      if (cmdEl.hidden) cmdReturn = (document.activeElement as HTMLElement) ?? null;
      cmdEl.hidden = false;
      cmdEl.classList.remove('vd-auto');
      cmdIn.value = '';
      cmd.render();
      if (focus) cmdIn.focus({ preventScroll: true });
    },
    /** `ran`: a command executed (focus the grid). Otherwise it was cancelled: restore the previous focus. */
    close(refocus = true, ran = false) {
      if (cmdEl.hidden) return;
      cmdEl.hidden = true;
      cmdEl.classList.remove('vd-auto');
      cmdMsg.textContent = '';
      const prev = cmdReturn;
      cmdReturn = null;
      if (!refocus) return;
      if (ran) focusList();
      else if (prev && prev !== cmdIn && prev !== document.body && prev.isConnected) prev.focus({ preventScroll: true });
    },
    render() {
      const v = cmdIn.value;
      items = complete(v);
      sel = 0;
      cmdMsg.textContent = '';
      cmdIn.setAttribute('aria-expanded', String(items.length > 0));
      cmdList.innerHTML = items
        .slice(0, 7)
        .map(
          (it, k) =>
            `<li role="option" id="vd-co-${k}" class="vd-co${k === sel ? ' vd-on' : ''}" aria-selected="${k === sel}" data-k="${k}"><i class="fa-solid fa-${it.icon}" aria-hidden="true"></i><span class="vd-mono vd-co-l">${esc(it.insert)}</span><span class="vd-co-c">${esc(it.caption)}</span></li>`,
        )
        .join('');
      cmdIn.setAttribute('aria-activedescendant', items.length ? 'vd-co-0' : '');
    },
    select(k: number) {
      const n = Math.min(items.length, 7);
      if (!n) return;
      sel = (k + n) % n;
      cmdList.querySelectorAll('.vd-co').forEach((li, j) => {
        li.classList.toggle('vd-on', j === sel);
        li.setAttribute('aria-selected', String(j === sel));
      });
      cmdIn.setAttribute('aria-activedescendant', `vd-co-${sel}`);
    },
    run(text: string, top?: Item, refocus = true) {
      const r = parse(text, top);
      if (!r.ok) {
        cmdMsg.textContent = r.msg;
        return false;
      }
      cmd.close(refocus && r.action.t !== 'ctxOpen', r.action.t !== 'quit');
      apply(r.action);
      return true;
    },
  };
  cmdIn.addEventListener('input', () => {
    interact();
    cmd.render();
  });
  cmdIn.addEventListener('keydown', (e) => {
    if (e.key === 'ArrowDown') (e.preventDefault(), cmd.select(sel + 1));
    else if (e.key === 'ArrowUp') (e.preventDefault(), cmd.select(sel - 1));
    else if (e.key === 'Tab' && !e.shiftKey && items[sel] && cmdIn.value !== items[sel].insert + (items[sel].insert.includes(' ') ? '' : ' ')) {
      e.preventDefault();
      cmdIn.value = items[sel].insert + (items[sel].insert.includes(' ') ? '' : ' ');
      cmd.render();
    } else if (e.key === 'Enter') {
      e.preventDefault();
      const v = cmdIn.value;
      const lastEmpty = /\s$/.test(v) || !v.trim();
      cmd.run(v, lastEmpty ? undefined : items[sel]);
    }
  });
  cmdList.addEventListener('pointerdown', (e) => e.preventDefault());
  cmdList.addEventListener('click', (e) => {
    const li = (e.target as HTMLElement).closest<HTMLElement>('[data-k]');
    if (li) cmd.run(items[+li.dataset.k!].insert, items[+li.dataset.k!]);
  });
  cmdIn.addEventListener('blur', (e) => {
    if (cmdEl.hidden || cmdEl.classList.contains('vd-auto')) return;
    const rt = e.relatedTarget as Node | null;
    if (rt && cmdEl.contains(rt)) return;
    setTimeout(() => {
      if (document.activeElement !== cmdIn) cmd.close(false);
    }, 0);
  });

  function apply(a: Action) {
    closeDrawer();
    switch (a.t) {
      case 'quit':
        break;
      case 'ctxOpen':
        openMenu();
        break;
      case 'ctx': {
        const cur = [...mask];
        const set = (id: string, v: boolean) => (cur[CLUSTERS.findIndex((c) => c.id === id)] = v);
        if (a.mode === 'set') CLUSTERS.forEach((c) => set(c.id, a.ids.includes(c.id)));
        else a.ids.forEach((id) => set(id, a.mode === 'add'));
        if (!cur.some(Boolean)) return showToast('At least one cluster must stay selected');
        applyMask(cur);
        break;
      }
      case 'ns':
        ns = a.ns;
        chrome();
        compute(true);
        break;
      case 'view':
        ns = a.ns ?? '';
        setView(a.view);
        break;
      case 'overview':
        showOverview();
        break;
      case 'ip':
        ns = '';
        setView('pods');
        q = a.q;
        filterIn.value = a.q;
        compute(true);
        break;
    }
  }

  // ---- shortcuts overlay -----------------------------------------------------------------
  const keys = $('.vd-keys');
  const toggleKeys = (on = keys.hidden) => {
    keys.hidden = !on;
    if (on) keys.querySelector<HTMLElement>('button')?.focus({ preventScroll: true });
    else focusList();
  };
  keys.addEventListener('click', (e) => {
    if (e.target === keys || (e.target as HTMLElement).closest('[data-close]')) toggleKeys(false);
  });
  $('[data-act="keys"]').addEventListener('click', () => toggleKeys(true));

  // ---- keyboard ----------------------------------------------------------------------------
  const isEditable = (t: HTMLElement) => t.matches('input,textarea,select,[contenteditable="true"],[contenteditable=""]');
  root.addEventListener('keydown', (e) => {
    const t = e.target as HTMLElement;
    if (e.key === 'Escape') {
      const wasAuto = autoplayActive;
      interact();
      if (wasAuto) return void e.preventDefault();
      if (!keys.hidden) return void (toggleKeys(false), e.preventDefault());
      if (cmd.isOpen) return void (cmd.close(), e.preventDefault());
      if (!menu.hidden) return void (closeMenu(), e.preventDefault());
      if (drawer.isOpen) return void (closeDrawer(), e.preventDefault());
      // Esc in the field clears it, then leaves it. Esc from the list clears a non-empty filter and keeps the list focused.
      if (t === filterIn) {
        e.preventDefault();
        if (q) {
          q = '';
          filterIn.value = '';
          compute(true);
        } else focusList();
        return;
      }
      if (q && !ovOn) {
        q = '';
        filterIn.value = '';
        compute(true);
        focusList();
        const i = firstRow(0, 1);
        if (i >= 0) table.setFocus(i);
        return void e.preventDefault();
      }
      if (selected.size) {
        selected.clear();
        table.refresh();
        bulk();
      }
      return;
    }
    if (e.key === 'Enter' && t === filterIn && !ovOn) {
      // Enter moves from the field to the results: first row focused, ready for j/k, arrows and Enter.
      e.preventDefault();
      interact();
      const i = firstRow(0, 1);
      focusList();
      if (i >= 0) table.setFocus(i);
      return;
    }
    if (e.ctrlKey || e.metaKey || e.altKey || isEditable(t) || menu.contains(t) || cmdEl.contains(t)) return;
    interact();
    const onBtn = !!t.closest('button, a');
    const k = e.key;
    if (ovOn && k !== '/' && k !== '?') return;
    if (k === '/' && ovOn) setView('pods');
    if (k === '/') (e.preventDefault(), filterIn.focus());
    else if (k === '?') (e.preventDefault(), toggleKeys());
    else if (k === 'j' || (k === 'ArrowDown' && t === scroller)) (e.preventDefault(), move(1));
    else if (k === 'k' || (k === 'ArrowUp' && t === scroller)) (e.preventDefault(), move(-1));
    else if (k === 'Home' && t === scroller) (e.preventDefault(), table.setFocus(firstRow(0, 1)));
    else if (k === 'End' && t === scroller) (e.preventDefault(), table.setFocus(firstRow(order.length - 1, -1)));
    else if (k === 'Enter' && !onBtn) (e.preventDefault(), openDrawer(table.focus < 0 ? firstRow(0, 1) : table.focus));
    else if (k === 'J') (e.preventDefault(), jumpToOwner());
    else if (k === 'c' && !onBtn) (e.preventDefault(), copyField('name'));
    else if (k === 'n' && !onBtn) (e.preventDefault(), copyField('namespace'));
    else if (k === 'x') (e.preventDefault(), table.focus >= 0 && toggleSel(table.focus));
    else if (k === 'l' || k === 'y' || k === 'd' || k === 'e' || k === 's') {
      if (onBtn && !t.matches('[role="tab"]') && t !== scroller) return;
      e.preventDefault();
      if (k === 's') return showToast('Demo: no shell in synthetic clusters');
      const i = drawer.isOpen ? drawerRow : table.focus < 0 ? firstRow(0, 1) : table.focus;
      if (k === 'e') return editFocused();
      const tab: Tab = k === 'l' ? 'logs' : k === 'y' ? 'yaml' : 'overview';
      if (drawer.isOpen) drawer.setTab(tab);
      else openDrawer(i, tab);
    }
  });
  root.addEventListener('focusin', () => {
    if (!ovOn && table.focus < 0 && order.length) {
      const i = firstRow(0, 1);
      if (i >= 0) table.setFocus(i, false);
    }
  });
  root.addEventListener(
    'pointerdown',
    (e) => {
      if (cmdEl.classList.contains('vd-auto') && cmdEl.contains(e.target as Node)) {
        // Clicking the intro bar turns it into a normal, visitor-owned bar.
        interacted = true;
        abort = true;
        autoplayActive = false;
        cmdEl.classList.remove('vd-auto');
        cmdIn.focus({ preventScroll: true });
        return;
      }
      interact();
    },
    true,
  );
  root.addEventListener('wheel', interact, { passive: true, capture: true });
  root.addEventListener('touchstart', interact, { passive: true, capture: true });

  // ---- phone bottom nav ----------------------------------------------------------------------
  root.querySelectorAll<HTMLElement>('[data-bn]').forEach((b) =>
    b.addEventListener('click', () => {
      interact();
      const k = b.dataset.bn;
      closeDrawer();
      if (k === 'problems' || k === 'pods') setView(k);
      else if (k === 'search') cmd.open();
      else if (k === 'clusters') openMenu();
    }),
  );

  // ---- layout (container width, not viewport) --------------------------------------------------
  function layout() {
    const p = root!.clientWidth <= PHONE_MAX;
    const first = !root!.dataset.laid;
    root!.dataset.laid = '1';
    if (p === phone && table.rowH === (p ? 72 : 32)) return;
    phone = p;
    root!.dataset.phone = p ? '1' : '';
    if (first && p) {
      ns = '';
      setView('problems');
    } else compute(false);
  }
  new ResizeObserver(layout).observe(root);

  // ---- public run + global hooks ---------------------------------------------------------------------
  function bringIntoView() {
    root!.scrollIntoView({ behavior: reduceMotion() ? 'auto' : 'smooth', block: 'center' });
  }
  function run(text: string) {
    interact();
    cmd.close(false);
    closeMenu(false);
    closeDrawer();
    const r = parse(text.replace(/^:/, ''), complete(text.replace(/^:/, ''))[0]);
    if (r.ok) apply(r.action);
    if (r.ok && r.action.t !== 'ctxOpen') focusList();
  }
  window.v8sDemo = { run };
  document.addEventListener('click', (e) => {
    const el = (e.target as HTMLElement).closest<HTMLElement>('[data-demo-cmd]');
    if (!el) return;
    e.preventDefault();
    bringIntoView();
    const text = el.dataset.demoCmd!.replace(/^:/, '').trim();
    if (text) run(text);
    else {
      interact();
      cmd.open();
    }
  });
  document.addEventListener('keydown', (e) => {
    if (e.key === 'Escape' && autoplayActive) return void interact();
    if (e.key !== ':' || e.ctrlKey || e.metaKey || e.altKey) return;
    const t = e.target as HTMLElement;
    if (isEditable(t) || t.isContentEditable) return;
    e.preventDefault();
    interact();
    bringIntoView();
    cmd.open();
  });

  // ---- one-time affordance ------------------------------------------------------------------------------
  let autoplayActive = false;
  let autoplayDone = false;
  let abort = false;
  function interact() {
    if (interacted) return;
    interacted = true;
    abort = true;
    if (autoplayActive) {
      autoplayActive = false;
      cmd.close(false);
    }
  }
  const sleep = (ms: number) => new Promise<void>((r) => setTimeout(r, ms));
  async function autoplay() {
    if (autoplayDone || interacted || phone) return;
    autoplayDone = true;
    autoplayActive = true;
    cmd.open(false);
    cmdEl.classList.add('vd-auto');
    if (reduceMotion()) {
      // No typing under reduced motion: show the typed command and its completions as a still,
      // and leave it open until the visitor does anything (interact() closes it).
      cmdIn.value = 'po pay';
      cmd.render();
      return;
    }
    for (const ch of 'po pay') {
      await sleep(60);
      if (abort) return;
      cmdIn.value += ch;
      cmd.render();
    }
    await sleep(900);
    if (abort) return;
    autoplayActive = false;
    const top = items[sel];
    cmd.run(cmdIn.value, top, false);
  }
  if ('IntersectionObserver' in window) {
    const io = new IntersectionObserver(
      (es) => {
        if (es.some((x) => x.isIntersecting)) {
          io.disconnect();
          setTimeout(() => void autoplay(), 500);
        }
      },
      { threshold: 0.6 },
    );
    io.observe(root);
  }

  // ---- usage drift: CPU and memory move a little every few seconds while the demo is on screen -------------------
  let onScreen = false;
  new IntersectionObserver((es) => (onScreen = es.some((x) => x.isIntersecting))).observe(root);
  if (!reduceMotion()) {
    setInterval(() => {
      if (!onScreen || document.hidden || (!ovOn && view !== 'pods' && view !== 'nodes')) return;
      clock.tick++;
      if (ovOn) {
        if (!ovEl.contains(document.activeElement) || document.activeElement === ovEl) compute(false);
      } else table.refresh();
    }, 4000);
  }

  // ---- go ----------------------------------------------------------------------------------------------------
  void GROUPS;
  sideCounts();
  chrome();
  layout();
  if (!root.dataset.laid) layout();
  compute(false);
  root.dataset.hydrated = '1';
}
