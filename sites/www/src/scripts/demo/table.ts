/**
 * Row-pooled virtual list. Rows are absolutely positioned with a transform
 * inside a spacer of the full height, so scrolling 50k rows only ever touches
 * the handful of rows that enter or leave the viewport.
 */
export interface TableHooks {
  inner(i: number): string;
  rowClass(i: number): string;
  isHeader(i: number): boolean;
}

export class VTable {
  rowH = 32;
  headH = 32;
  count = 0;
  overscan = 5;
  focus = -1;
  private pool = new Map<number, HTMLElement>();
  private free: HTMLElement[] = [];
  private raf = 0;
  private card = false;

  constructor(
    readonly scroller: HTMLElement,
    readonly sizer: HTMLElement,
    private hooks: TableHooks,
  ) {
    scroller.addEventListener('scroll', () => this.schedule(), { passive: true });
    new ResizeObserver(() => this.schedule()).observe(scroller);
  }

  private schedule() {
    if (this.raf) return;
    this.raf = requestAnimationFrame(() => {
      this.raf = 0;
      this.update();
    });
  }

  /** Replace the data set. Keeps scroll position unless reset. */
  set(count: number, opts: { rowH: number; headH: number; card: boolean; reset?: boolean; hooks?: TableHooks }) {
    this.count = count;
    this.rowH = opts.rowH;
    this.headH = opts.headH;
    this.card = opts.card;
    if (opts.hooks) this.hooks = opts.hooks;
    this.sizer.style.height = `${count * this.rowH}px`;
    this.scroller.setAttribute('aria-rowcount', String(count + 1));
    if (opts.reset) this.scroller.scrollTop = 0;
    this.render(true);
  }

  get domRows(): number {
    return this.pool.size;
  }

  private build(i: number, el: HTMLElement) {
    const h = this.hooks.isHeader(i);
    el.className = `${this.card ? 'vd-pcard' : 'vd-tr vd-row'} ${this.hooks.rowClass(i)}${h ? ' vd-grprow' : ''}`;
    el.innerHTML = this.hooks.inner(i);
    el.style.transform = `translateY(${i * this.rowH}px)`;
    el.dataset.i = String(i);
    el.id = `vd-r-${i}`;
    el.setAttribute('aria-rowindex', String(i + 2));
    if (h) el.setAttribute('aria-label', 'group');
    else el.removeAttribute('aria-label');
  }

  render(force = false) {
    const top = this.scroller.scrollTop;
    const h = this.scroller.clientHeight || 400;
    const first = Math.max(0, Math.floor(top / this.rowH) - this.overscan);
    const last = Math.min(this.count - 1, Math.ceil((top + h - this.headH) / this.rowH) + this.overscan);
    for (const [i, el] of this.pool) {
      if (i < first || i > last || force) {
        this.pool.delete(i);
        el.remove();
        this.free.push(el);
      }
    }
    const frag = document.createDocumentFragment();
    for (let i = first; i <= last; i++) {
      if (this.pool.has(i)) continue;
      const el = this.free.pop() ?? document.createElement('div');
      el.setAttribute('role', 'row');
      this.build(i, el);
      this.pool.set(i, el);
      frag.appendChild(el);
    }
    this.sizer.appendChild(frag);
    this.onRender?.();
  }

  onRender?: () => void;

  update() {
    this.render(false);
  }

  /** Re-render rows currently in the pool (data of visible rows changed). */
  refresh() {
    for (const [i, el] of this.pool) this.build(i, el);
  }

  rowEl(i: number): HTMLElement | undefined {
    return this.pool.get(i);
  }

  setFocus(i: number, scroll = true) {
    const prev = this.pool.get(this.focus);
    prev?.classList.remove('vd-focus');
    this.focus = i;
    if (i < 0) return;
    if (scroll) this.reveal(i);
    this.pool.get(i)?.classList.add('vd-focus');
    this.scroller.setAttribute('aria-activedescendant', `vd-r-${i}`);
  }

  reveal(i: number) {
    const s = this.scroller;
    const y = i * this.rowH;
    const vis = s.clientHeight - this.headH;
    if (y < s.scrollTop) s.scrollTop = y;
    else if (y + this.rowH > s.scrollTop + vis) s.scrollTop = y + this.rowH - vis;
    this.render(false);
  }

  pageRows(): number {
    return Math.max(1, Math.floor((this.scroller.clientHeight - this.headH) / this.rowH) - 1);
  }
}
