<script setup lang="ts">
import type { FluxGraphNode, FluxGraphResult } from '@v8s/client';

import { computed, nextTick, onBeforeUnmount, onMounted, ref, shallowRef, toRaw, watch } from 'vue';

import type { Layout, LayoutEdge, LayoutNode } from '@/lib/flux-layout.ts';

import { layoutGraph, StaleLayout } from '@/lib/flux-graph-client.ts';
import { toneClass } from '@/lib/tone.ts';

/**
 * One canvas for the whole Flux dependency graph. Layout comes from a worker; drawing is culled to the viewport and
 * coalesced into one requestAnimationFrame. The accessible alternative is the "List view" table.
 */
const props = defineProps<{ graph: FluxGraphResult; focusId?: string }>();
const emit = defineEmits<{ open: [node: FluxGraphNode] }>();

const MIN_SCALE = 0.08;
const MAX_SCALE = 3;
const CELL = 256;
const LIST_MAX = 500;
const LOD_TEXT = 0.4;
const FONT_NAME = 12;

const wrap = ref<HTMLDivElement | null>(null);
const canvas = ref<HTMLCanvasElement | null>(null);
const tip = ref<HTMLDivElement | null>(null);
const busy = ref(false);
const failure = ref('');
const listView = ref(false);
const hoverId = shallowRef<string | null>(null);
const focusNode = shallowRef<string | null>(null);
const announce = ref('');

// Non-reactive drawing state.
let layout: Layout | null = null;
let meta = new Map<string, FluxGraphNode>();
let byId = new Map<string, LayoutNode>();
let depsOut = new Map<string, string[]>(); // node -> nodes it waits for / reads (drawn to its left)
let depsIn = new Map<string, string[]>(); // node -> its dependants (drawn to its right)
let grid = new Map<number, LayoutNode[]>();
let edgeBoxes: { minX: number; maxX: number; minY: number; maxY: number }[] = [];
let labels = new Map<string, string>();
let view = { scale: 1, tx: 0, ty: 0 };
let cw = 0;
let ch = 0;
let dpr = 1;
let raf = 0;
let reqToken = 0;
let userMoved = false;

interface Palette {
    surface: string;
    border: string;
    bstrong: string;
    text: string;
    text2: string;
    text3: string;
    focus: string;
    accent: string;
    tones: string[];
    font: string;
}
let pal: Palette = {
    surface: '#fff',
    border: '#ddd',
    bstrong: '#bbb',
    text: '#111',
    text2: '#444',
    text3: '#666',
    focus: '#66f',
    accent: '#44f',
    tones: ['#555', '#080', '#a60', '#c30', '#25d', '#72d'],
    font: 'system-ui, sans-serif'
};

function readPalette() {
    const cs = getComputedStyle(document.documentElement);
    const v = (n: string, d: string) => cs.getPropertyValue(n).trim() || d;
    pal = {
        surface: v('--surface', pal.surface),
        border: v('--border', pal.border),
        bstrong: v('--bstrong', pal.bstrong),
        text: v('--text', pal.text),
        text2: v('--text2', pal.text2),
        text3: v('--text3', pal.text3),
        focus: v('--focus', pal.focus),
        accent: v('--accent', pal.accent),
        tones: [
            v('--neutral', pal.tones[0]!),
            v('--success', pal.tones[1]!),
            v('--warning', pal.tones[2]!),
            v('--danger', pal.tones[3]!),
            v('--info', pal.tones[4]!),
            v('--release', pal.tones[5]!)
        ],
        font: cs.fontFamily || pal.font
    };
    labels = new Map();
    schedule();
}

const TONE_WORDS = ['Unknown', 'Ready', 'Warning', 'Failing', 'Reconciling', 'Updating'];
function toneWord(n: FluxGraphNode): string {
    return n.suspended ? 'Suspended' : (TONE_WORDS[n.tone] ?? 'Unknown');
}
const toneColor = (n: FluxGraphNode) => (n.suspended ? pal.text3 : (pal.tones[n.tone] ?? pal.tones[0]!));

function schedule() {
    if (raf || !canvas.value) return;
    raf = requestAnimationFrame(() => {
        raf = 0;
        draw();
    });
}

function ellipsize(ctx: CanvasRenderingContext2D, id: string, text: string, max: number): string {
    const hit = labels.get(id);
    if (hit !== undefined) return hit;
    let out = text;
    if (ctx.measureText(text).width > max) {
        let lo = 0;
        let hi = text.length;
        while (lo < hi) {
            const mid = (lo + hi + 1) >> 1;
            if (ctx.measureText(text.slice(0, mid) + '…').width <= max) lo = mid;
            else hi = mid - 1;
        }
        out = text.slice(0, lo) + '…';
    }
    labels.set(id, out);
    return out;
}

function rrect(ctx: CanvasRenderingContext2D, x: number, y: number, w: number, h: number, r: number) {
    ctx.beginPath();
    if (typeof ctx.roundRect === 'function') ctx.roundRect(x, y, w, h, r);
    else ctx.rect(x, y, w, h);
}

function draw() {
    const cv = canvas.value;
    const ctx = cv?.getContext('2d');
    if (!cv || !ctx) return;
    ctx.setTransform(1, 0, 0, 1, 0, 0);
    ctx.clearRect(0, 0, cv.width, cv.height);
    if (!layout || !cw || !ch) return;
    const { scale, tx, ty } = view;
    ctx.setTransform(dpr * scale, 0, 0, dpr * scale, dpr * tx, dpr * ty);
    const x0 = -tx / scale;
    const y0 = -ty / scale;
    const x1 = (cw - tx) / scale;
    const y1 = (ch - ty) / scale;
    const active = focusNode.value ?? hoverId.value;

    // Edges: one path per style; edges touching the focused/hovered node are drawn again on top in the accent color.
    ctx.lineJoin = 'round';
    const lw = Math.max(1, 1.25 / Math.max(scale, 0.5));
    const pass = (type: string, hot: boolean) => {
        const curves = new Path2D();
        const heads = new Path2D();
        let any = false;
        for (let i = 0; i < layout!.edges.length; i++) {
            const e = layout!.edges[i]!;
            if ((e.type === 'sourceRef') !== (type === 'sourceRef')) continue;
            const isHot = active !== null && (e.from === active || e.to === active);
            if (isHot !== hot) continue;
            const b = edgeBoxes[i]!;
            if (b.maxX < x0 || b.minX > x1 || b.maxY < y0 || b.minY > y1) continue;
            tracePath(curves, heads, e);
            any = true;
        }
        if (!any) return;
        ctx.strokeStyle = hot ? pal.accent : pal.text3;
        ctx.fillStyle = ctx.strokeStyle;
        ctx.lineWidth = hot ? lw * 1.6 : lw;
        ctx.setLineDash(type === 'sourceRef' ? [6, 4] : []);
        ctx.stroke(curves);
        ctx.setLineDash([]);
        ctx.fill(heads);
    };
    for (const hot of [false, true]) {
        pass('dependsOn', hot);
        pass('sourceRef', hot);
    }

    // Nodes.
    const text = scale >= LOD_TEXT;
    ctx.textBaseline = 'alphabetic';
    for (const n of layout.nodes) {
        if (n.x + n.w < x0 || n.x > x1 || n.y + n.h < y0 || n.y > y1) continue;
        const m = meta.get(n.id)!;
        const tc = toneColor(m);
        rrect(ctx, n.x, n.y, n.w, n.h, 6);
        ctx.fillStyle = pal.surface;
        ctx.fill();
        ctx.setLineDash(m.suspended ? [4, 3] : []);
        ctx.lineWidth = n.id === hoverId.value ? 1.8 : 1;
        ctx.strokeStyle = m.suspended || n.id === hoverId.value ? pal.text3 : pal.bstrong;
        ctx.stroke();
        ctx.setLineDash([]);
        ctx.fillStyle = tc;
        ctx.fillRect(n.x + 1, n.y + 5, 3, n.h - 10);
        if (text) {
            ctx.font = `600 9px ${pal.font}`;
            ctx.textAlign = 'left';
            ctx.fillStyle = pal.text3;
            ctx.fillText(m.kind.toUpperCase(), n.x + 11, n.y + 15);
            ctx.font = `600 10px ${pal.font}`;
            ctx.textAlign = 'right';
            ctx.fillStyle = tc;
            ctx.fillText(toneWord(m), n.x + n.w - 8, n.y + 15);
            ctx.font = `500 ${FONT_NAME}px ${pal.font}`;
            ctx.textAlign = 'left';
            ctx.fillStyle = pal.text;
            ctx.fillText(ellipsize(ctx, n.id, m.name, n.w - 20), n.x + 11, n.y + 32);
        }
        if (n.id === focusNode.value) {
            rrect(ctx, n.x - 3, n.y - 3, n.w + 6, n.h + 6, 8);
            ctx.lineWidth = 2;
            ctx.strokeStyle = pal.focus;
            ctx.stroke();
        }
    }
}

/** Adds one edge (bezier through the layout's mid control points) plus its arrowhead to the current path. */
function tracePath(ctx: Path2D, heads: Path2D, e: LayoutEdge) {
    const p = e.points;
    ctx.moveTo(p[0]![0], p[0]![1]);
    let ex: number;
    let ey: number;
    let dir: number;
    if (p.length === 4 && e.from !== e.to) {
        ctx.bezierCurveTo(p[1]![0], p[1]![1], p[2]![0], p[2]![1], p[3]![0], p[3]![1]);
        ex = p[3]![0];
        ey = p[3]![1];
        dir = p[3]![0] >= p[2]![0] ? 1 : -1;
    } else {
        for (let i = 1; i < p.length; i++) ctx.lineTo(p[i]![0], p[i]![1]);
        const l = p[p.length - 1]!;
        ex = l[0];
        ey = l[1];
        dir = l[0] >= p[p.length - 2]![0] ? 1 : -1;
    }
    // Arrow tip at the target end, pointing along the last segment.
    heads.moveTo(ex, ey);
    heads.lineTo(ex - dir * 7, ey - 3.5);
    heads.lineTo(ex - dir * 7, ey + 3.5);
    heads.closePath();
}

function setLayout(l: Layout) {
    layout = l;
    byId = new Map(l.nodes.map(n => [n.id, n]));
    depsOut = new Map();
    depsIn = new Map();
    edgeBoxes = l.edges.map(e => {
        let minX = Infinity;
        let maxX = -Infinity;
        let minY = Infinity;
        let maxY = -Infinity;
        for (const [x, y] of e.points) {
            if (x < minX) minX = x;
            if (x > maxX) maxX = x;
            if (y < minY) minY = y;
            if (y > maxY) maxY = y;
        }
        const a = depsOut.get(e.from);
        if (a) a.push(e.to);
        else depsOut.set(e.from, [e.to]);
        const b = depsIn.get(e.to);
        if (b) b.push(e.from);
        else depsIn.set(e.to, [e.from]);
        return { minX, maxX, minY, maxY };
    });
    grid = new Map();
    for (const n of l.nodes) {
        for (let cy = Math.floor(n.y / CELL); cy <= Math.floor((n.y + n.h) / CELL); cy++) {
            for (let cx = Math.floor(n.x / CELL); cx <= Math.floor((n.x + n.w) / CELL); cx++) {
                const k = cy * 100003 + cx;
                const c = grid.get(k);
                if (c) c.push(n);
                else grid.set(k, [n]);
            }
        }
    }
    labels = new Map();
}

function hit(wx: number, wy: number): LayoutNode | null {
    const c = grid.get(Math.floor(wy / CELL) * 100003 + Math.floor(wx / CELL));
    if (!c) return null;
    for (let i = c.length - 1; i >= 0; i--) {
        const n = c[i]!;
        if (wx >= n.x && wx <= n.x + n.w && wy >= n.y && wy <= n.y + n.h) return n;
    }
    return null;
}

function fit() {
    if (!layout || !cw || !ch || !layout.nodes.length) return;
    const pad = 24;
    const s = Math.min(1, (cw - pad * 2) / Math.max(1, layout.width), (ch - pad * 2) / Math.max(1, layout.height));
    const scale = Math.min(MAX_SCALE, Math.max(MIN_SCALE, s));
    view = { scale, tx: (cw - layout.width * scale) / 2, ty: Math.max(pad, (ch - layout.height * scale) / 2) };
    userMoved = false;
    schedule();
}

function zoomAt(factor: number, sx: number, sy: number) {
    const scale = Math.min(MAX_SCALE, Math.max(MIN_SCALE, view.scale * factor));
    const k = scale / view.scale;
    view = { scale, tx: sx - (sx - view.tx) * k, ty: sy - (sy - view.ty) * k };
    userMoved = true;
    schedule();
}
const zoomBy = (f: number) => zoomAt(f, cw / 2, ch / 2);

function resize() {
    const w = wrap.value;
    const cv = canvas.value;
    if (!w || !cv) return;
    const r = w.getBoundingClientRect();
    const nw = Math.floor(r.width);
    const nh = Math.floor(r.height);
    const nd = window.devicePixelRatio || 1;
    if (nw === cw && nh === ch && nd === dpr) return;
    const first = cw === 0;
    cw = nw;
    ch = nh;
    dpr = nd;
    cv.width = Math.max(1, Math.round(cw * dpr));
    cv.height = Math.max(1, Math.round(ch * dpr));
    cv.style.width = cw + 'px';
    cv.style.height = ch + 'px';
    if (first || !userMoved) fit();
    else schedule();
}

// ---- layout ----
async function relayout() {
    const token = ++reqToken;
    const g = toRaw(props.graph);
    meta = new Map(g.nodes.map(n => [n.id, n]));
    failure.value = '';
    if (!g.nodes.length) {
        layout = null;
        byId = new Map();
        busy.value = false;
        schedule();
        return;
    }
    busy.value = true;
    try {
        const l = await layoutGraph(g);
        if (token !== reqToken) return;
        setLayout(l);
        if (focusNode.value && !byId.has(focusNode.value)) focusNode.value = null;
        applyFocusProp();
        fit();
    } catch (e) {
        if (e instanceof StaleLayout || token !== reqToken) return;
        failure.value = e instanceof Error ? e.message : String(e);
    } finally {
        if (token === reqToken) busy.value = false;
    }
}
watch(
    () => props.graph,
    () => void relayout()
);

function applyFocusProp() {
    const id = props.focusId;
    if (id && byId.has(id)) setFocus(id, true);
}
watch(() => props.focusId, applyFocusProp);

// ---- focus / keyboard ----
function describe(id: string): string {
    const m = meta.get(id);
    if (!m) return '';
    const o = depsOut.get(id)?.length ?? 0;
    const i = depsIn.get(id)?.length ?? 0;
    return `${m.kind} ${m.namespace ? m.namespace + '/' : ''}${m.name}, ${toneWord(m)}. Depends on ${o}, ${i} dependant${i === 1 ? '' : 's'}.`;
}

function setFocus(id: string | null, center = false) {
    focusNode.value = id;
    if (id) {
        announce.value = describe(id);
        const n = byId.get(id);
        if (n) reveal(n, center);
    }
    schedule();
}

function reveal(n: LayoutNode, center: boolean) {
    const s = view.scale;
    const sx = n.x * s + view.tx;
    const sy = n.y * s + view.ty;
    const inside = sx >= 0 && sy >= 0 && sx + n.w * s <= cw && sy + n.h * s <= ch;
    if (inside && !center) return;
    view = { scale: s, tx: cw / 2 - (n.x + n.w / 2) * s, ty: ch / 2 - (n.y + n.h / 2) * s };
    userMoved = true;
}

function nearestY(from: LayoutNode, ids: string[] | undefined): string | null {
    if (!ids?.length) return null;
    let best: string | null = null;
    let bd = Infinity;
    for (const id of ids) {
        const n = byId.get(id);
        if (!n) continue;
        const d = Math.abs(n.y - from.y);
        if (d < bd) {
            bd = d;
            best = id;
        }
    }
    return best;
}

function sibling(from: LayoutNode, dir: 1 | -1): string | null {
    let best: LayoutNode | null = null;
    for (const n of layout!.nodes) {
        if (n.layer !== from.layer || n === from) continue;
        if (dir > 0 ? n.y > from.y : n.y < from.y) {
            if (!best || (dir > 0 ? n.y < best.y : n.y > best.y)) best = n;
        }
    }
    return best?.id ?? null;
}

function onKey(ev: KeyboardEvent) {
    if (!layout || !layout.nodes.length) return;
    const cur = focusNode.value ? byId.get(focusNode.value) : undefined;
    let next: string | null | undefined;
    switch (ev.key) {
        case 'ArrowLeft':
            next = cur ? nearestY(cur, depsOut.get(cur.id)) : layout.nodes[0]!.id;
            break;
        case 'ArrowRight':
            next = cur ? nearestY(cur, depsIn.get(cur.id)) : layout.nodes[0]!.id;
            break;
        case 'ArrowDown':
            next = cur ? sibling(cur, 1) : layout.nodes[0]!.id;
            break;
        case 'ArrowUp':
            next = cur ? sibling(cur, -1) : layout.nodes[0]!.id;
            break;
        case 'Enter':
        case ' ': {
            const m = cur && meta.get(cur.id);
            if (m) emit('open', m);
            ev.preventDefault();
            return;
        }
        case '+':
        case '=':
            zoomBy(1.25);
            ev.preventDefault();
            return;
        case '-':
            zoomBy(0.8);
            ev.preventDefault();
            return;
        case '0':
            fit();
            ev.preventDefault();
            return;
        default:
            return;
    }
    ev.preventDefault();
    if (next) setFocus(next);
}
function onFocus() {
    if (!focusNode.value && layout?.nodes.length) {
        // Start on the first failing node, otherwise the first node.
        const bad = layout.nodes.find(n => meta.get(n.id)?.tone === 3);
        setFocus((bad ?? layout.nodes[0]!).id);
    }
}

// ---- pointer ----
const pointers = new Map<number, { x: number; y: number }>();
let drag: { sx: number; sy: number; tx: number; ty: number; moved: boolean } | null = null;
let pinch: { dist: number; scale: number } | null = null;

function local(ev: { clientX: number; clientY: number }) {
    const r = canvas.value!.getBoundingClientRect();
    return { x: ev.clientX - r.left, y: ev.clientY - r.top };
}
function onDown(ev: PointerEvent) {
    const p = local(ev);
    pointers.set(ev.pointerId, p);
    canvas.value?.setPointerCapture(ev.pointerId);
    if (pointers.size === 1) drag = { sx: p.x, sy: p.y, tx: view.tx, ty: view.ty, moved: false };
    else if (pointers.size === 2) {
        const [a, b] = [...pointers.values()] as [{ x: number; y: number }, { x: number; y: number }];
        pinch = { dist: Math.hypot(a.x - b.x, a.y - b.y) || 1, scale: view.scale };
        if (drag) drag.moved = true;
    }
}
function onMove(ev: PointerEvent) {
    const p = local(ev);
    if (!pointers.has(ev.pointerId)) {
        setHover(hit((p.x - view.tx) / view.scale, (p.y - view.ty) / view.scale), ev.clientX, ev.clientY);
        return;
    }
    pointers.set(ev.pointerId, p);
    if (pinch && pointers.size >= 2) {
        const [a, b] = [...pointers.values()] as [{ x: number; y: number }, { x: number; y: number }];
        const d = Math.hypot(a.x - b.x, a.y - b.y) || 1;
        zoomAt((pinch.scale * (d / pinch.dist)) / view.scale, (a.x + b.x) / 2, (a.y + b.y) / 2);
    } else if (drag) {
        const dx = p.x - drag.sx;
        const dy = p.y - drag.sy;
        if (!drag.moved && Math.hypot(dx, dy) > 4) drag.moved = true;
        if (drag.moved) {
            view = { scale: view.scale, tx: drag.tx + dx, ty: drag.ty + dy };
            userMoved = true;
            setHover(null, 0, 0);
            schedule();
        }
    }
}
function onUp(ev: PointerEvent) {
    const p = local(ev);
    const wasClick = drag && !drag.moved && pointers.size === 1;
    pointers.delete(ev.pointerId);
    if (pointers.size < 2) pinch = null;
    if (pointers.size === 0) {
        if (wasClick) {
            const n = hit((p.x - view.tx) / view.scale, (p.y - view.ty) / view.scale);
            if (n) {
                setFocus(n.id);
                const m = meta.get(n.id);
                if (m) emit('open', m);
            }
        }
        drag = null;
    }
}
function onLeave() {
    setHover(null, 0, 0);
}
function onWheel(ev: WheelEvent) {
    ev.preventDefault();
    const p = local(ev);
    if (ev.ctrlKey || ev.metaKey) zoomAt(Math.exp(-ev.deltaY * 0.01), p.x, p.y);
    else {
        view = { scale: view.scale, tx: view.tx - ev.deltaX, ty: view.ty - ev.deltaY };
        userMoved = true;
        schedule();
    }
}

function setHover(n: LayoutNode | null, cx: number, cy: number) {
    const id = n?.id ?? null;
    if (id && tip.value) {
        const r = wrap.value!.getBoundingClientRect();
        tip.value.style.transform = `translate(${Math.min(cx - r.left + 12, Math.max(0, r.width - 240))}px, ${cy - r.top + 14}px)`;
    }
    if (id !== hoverId.value) {
        hoverId.value = id;
        if (canvas.value) canvas.value.style.cursor = id ? 'pointer' : 'grab';
        schedule();
    }
}
const hoverMeta = computed(() => (hoverId.value ? meta.get(hoverId.value) : undefined));

// ---- list view ----
const listRows = computed(() => {
    if (!listView.value) return { rows: [] as { node: FluxGraphNode; deps: string[] }[], total: 0 };
    const g = toRaw(props.graph);
    const names = new Map(g.nodes.map(n => [n.id, n]));
    const deps = new Map<string, string[]>();
    for (const e of g.edges) {
        const t = names.get(e.to);
        if (!names.has(e.from) || !t) continue;
        const s = `${t.kind}/${t.name} (${e.type === 'sourceRef' ? 'reads' : 'depends on'})`;
        const a = deps.get(e.from);
        if (a) a.push(s);
        else deps.set(e.from, [s]);
    }
    const rank = [0, 1, 4, 5, 3, 2];
    const sorted = [...g.nodes].sort((a, b) => (rank[b.tone] ?? 0) - (rank[a.tone] ?? 0));
    return { rows: sorted.slice(0, LIST_MAX).map(node => ({ node, deps: deps.get(node.id) ?? [] })), total: sorted.length };
});

// ---- lifecycle ----
let ro: ResizeObserver | null = null;
let mo: MutationObserver | null = null;
let mq: MediaQueryList | null = null;
onMounted(() => {
    readPalette();
    ro = new ResizeObserver(resize);
    if (wrap.value) ro.observe(wrap.value);
    mo = new MutationObserver(readPalette);
    mo.observe(document.documentElement, { attributes: true, attributeFilter: ['class', 'data-theme'] });
    mq = matchMedia('(prefers-color-scheme: dark)');
    mq.addEventListener('change', readPalette);
    canvas.value?.addEventListener('wheel', onWheel, { passive: false });
    resize();
    void relayout();
});
onBeforeUnmount(() => {
    reqToken++;
    ro?.disconnect();
    mo?.disconnect();
    mq?.removeEventListener('change', readPalette);
    canvas.value?.removeEventListener('wheel', onWheel);
    if (raf) cancelAnimationFrame(raf);
});
watch(listView, async () => {
    await nextTick();
    resize();
});
</script>

<template>
    <div ref="wrap" class="fg" data-testid="flux-graph">
        <canvas
            v-show="!listView"
            ref="canvas"
            class="cv"
            tabindex="0"
            role="application"
            aria-roledescription="dependency graph"
            aria-label="Flux dependency graph. Arrow keys follow dependencies, Enter opens the focused object, plus and minus zoom."
            @keydown="onKey"
            @focus="onFocus"
            @pointerdown="onDown"
            @pointermove="onMove"
            @pointerup="onUp"
            @pointercancel="onUp"
            @pointerleave="onLeave"
        ></canvas>
        <p class="sr-only" role="status" aria-live="polite">{{ announce }}</p>

        <div class="tools floating" role="toolbar" aria-label="Graph controls">
            <button class="btn sm ghost" type="button" aria-label="Zoom out" @click="zoomBy(0.8)">−</button>
            <button class="btn sm ghost" type="button" aria-label="Zoom in" @click="zoomBy(1.25)">+</button>
            <button class="btn sm ghost" type="button" @click="fit">Fit</button>
            <button class="btn sm" type="button" :aria-pressed="listView" @click="listView = !listView">
                {{ listView ? 'Graph view' : 'List view' }}
            </button>
        </div>

        <div v-if="busy && !listView" class="state muted" role="status">Laying out…</div>
        <div v-else-if="failure" class="state notice danger" role="alert">Layout failed: {{ failure }}</div>
        <div v-else-if="!graph.nodes.length" class="state muted">No Flux objects to draw.</div>

        <div v-show="hoverMeta && !listView" ref="tip" class="tip floating" role="presentation">
            <template v-if="hoverMeta">
                <div class="lbl">{{ hoverMeta.kind }}</div>
                <div class="nm">{{ hoverMeta.name }}</div>
                <div class="muted">{{ hoverMeta.namespace || 'cluster-scoped' }}</div>
                <span class="tag" :class="hoverMeta.suspended ? '' : toneClass(hoverMeta.tone)">{{
                    hoverMeta.suspended ? 'Suspended' : TONE_WORDS[hoverMeta.tone]
                }}</span>
            </template>
        </div>

        <div v-if="listView" class="list">
            <table>
                <caption class="sr-only">
                    Flux objects and what each depends on or reads
                </caption>
                <thead>
                    <tr>
                        <th scope="col">Kind</th>
                        <th scope="col">Namespace</th>
                        <th scope="col">Name</th>
                        <th scope="col">Status</th>
                        <th scope="col">Depends on / reads</th>
                    </tr>
                </thead>
                <tbody>
                    <tr v-for="r in listRows.rows" :key="r.node.id">
                        <td>{{ r.node.kind }}</td>
                        <td class="mono">{{ r.node.namespace || '—' }}</td>
                        <td>
                            <button class="btn sm ghost" type="button" @click="emit('open', r.node)">{{ r.node.name }}</button>
                        </td>
                        <td>
                            <span class="tag" :class="r.node.suspended ? '' : toneClass(r.node.tone)">{{
                                r.node.suspended ? 'Suspended' : TONE_WORDS[r.node.tone]
                            }}</span>
                        </td>
                        <td class="deps">{{ r.deps.join(', ') || '—' }}</td>
                    </tr>
                </tbody>
            </table>
            <p v-if="listRows.total > LIST_MAX" class="muted foot">Showing {{ LIST_MAX }} of {{ listRows.total }} objects, failing first.</p>
        </div>
    </div>
</template>

<style scoped>
.fg {
    position: relative;
    width: 100%;
    height: 100%;
    min-height: 320px;
    overflow: hidden;
    background: var(--s2);
    border: 1px solid var(--border);
    border-radius: 12px;
}
.cv {
    display: block;
    touch-action: none;
    cursor: grab;
    outline-offset: -2px;
}
.tools {
    position: absolute;
    top: 8px;
    right: 8px;
    z-index: 3;
    display: flex;
    gap: 2px;
    padding: 3px;
}
.state {
    position: absolute;
    left: 50%;
    top: 50%;
    transform: translate(-50%, -50%);
    font-size: 13px;
}
.tip {
    position: absolute;
    top: 0;
    left: 0;
    z-index: 2;
    max-width: 240px;
    padding: 8px 10px;
    pointer-events: none;
    font-size: 12px;
    display: flex;
    flex-direction: column;
    gap: 2px;
    align-items: flex-start;
}
.nm {
    font-weight: 600;
    overflow-wrap: anywhere;
}
.list {
    position: absolute;
    inset: 0;
    overflow: auto;
    background: var(--surface);
    padding-top: 44px;
}
.list table {
    border-collapse: collapse;
    width: 100%;
    font-size: 12.5px;
}
.list th,
.list td {
    padding: 4px 10px;
    text-align: left;
    border-bottom: 1px solid var(--border);
    vertical-align: top;
}
.list th {
    position: sticky;
    top: 0;
    background: var(--s2);
    font-size: 11px;
    color: var(--text3);
}
.deps {
    color: var(--text2);
    overflow-wrap: anywhere;
}
.foot {
    padding: 8px 10px;
    margin: 0;
}
</style>
