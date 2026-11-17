<script setup lang="ts">
import type { HubbleServiceMap, ServiceMapEdge, ServiceMapNode } from '@v8s/client';

import { computed, onBeforeUnmount, onMounted, ref, shallowRef, toRaw, watch } from 'vue';

import type { LayoutNode, MapEdgeLayout, MapLayout } from '@/lib/servicemap-layout.ts';

import { layoutMap, StaleLayout } from '@/lib/servicemap-layout-client.ts';
import { classifyEdge, edgeLabelParts, nodeLabel } from '@/lib/servicemap.ts';

/**
 * Canvas drawing of the service map. Layout comes from a worker; drawing is one requestAnimationFrame per change. The accessible
 * alternative is the edge list rendered by ServiceMapView next to it.
 */
const props = defineProps<{ graph: Pick<HubbleServiceMap, 'nodes' | 'edges'>; selectedId: string | null }>();
const emit = defineEmits<{ select: [id: string | null] }>();

const MIN_SCALE = 0.1;
const MAX_SCALE = 3;
const LOD_TEXT = 0.45;
const LOD_LABEL = 0.6;

const wrap = ref<HTMLDivElement | null>(null);
const canvas = ref<HTMLCanvasElement | null>(null);
const busy = ref(false);
const failure = ref('');
const hoverId = shallowRef<string | null>(null);
const announce = ref('');

let layout: MapLayout | null = null;
let meta = new Map<string, ServiceMapNode>();
let edgeMeta: ServiceMapEdge[] = [];
let incident = new Map<string, number>(); // node -> worst class rank among its edges
let byId = new Map<string, LayoutNode>();
let view = { scale: 1, tx: 0, ty: 0 };
let cw = 0;
let ch = 0;
let dpr = 1;
let raf = 0;
let token = 0;
let userMoved = false;

interface Palette {
    surface: string;
    s2: string;
    bstrong: string;
    text: string;
    text2: string;
    text3: string;
    focus: string;
    accent: string;
    success: string;
    danger: string;
    warning: string;
    font: string;
}
let pal: Palette = {
    surface: '#fff',
    s2: '#f3f4f6',
    bstrong: '#bbb',
    text: '#111',
    text2: '#444',
    text3: '#666',
    focus: '#66f',
    accent: '#44f',
    success: '#080',
    danger: '#c30',
    warning: '#a60',
    font: 'system-ui, sans-serif'
};
function readPalette() {
    const cs = getComputedStyle(document.documentElement);
    const v = (n: string, d: string) => cs.getPropertyValue(n).trim() || d;
    pal = {
        surface: v('--surface', pal.surface),
        s2: v('--s2', pal.s2),
        bstrong: v('--bstrong', pal.bstrong),
        text: v('--text', pal.text),
        text2: v('--text2', pal.text2),
        text3: v('--text3', pal.text3),
        focus: v('--focus', pal.focus),
        accent: v('--accent', pal.accent),
        success: v('--success', pal.success),
        danger: v('--danger', pal.danger),
        warning: v('--warning', pal.warning),
        font: getComputedStyle(document.body).fontFamily || pal.font
    };
    schedule();
}

function schedule() {
    if (raf || !canvas.value) return;
    raf = requestAnimationFrame(() => {
        raf = 0;
        draw();
    });
}

function rrect(ctx: CanvasRenderingContext2D, x: number, y: number, w: number, h: number, r: number) {
    ctx.beginPath();
    if (typeof ctx.roundRect === 'function') ctx.roundRect(x, y, w, h, r);
    else ctx.rect(x, y, w, h);
}
function ellipsize(ctx: CanvasRenderingContext2D, text: string, max: number): string {
    if (ctx.measureText(text).width <= max) return text;
    let lo = 0;
    let hi = text.length;
    while (lo < hi) {
        const mid = (lo + hi + 1) >> 1;
        if (ctx.measureText(text.slice(0, mid) + '…').width <= max) lo = mid;
        else hi = mid - 1;
    }
    return text.slice(0, lo) + '…';
}

function tracePath(path: Path2D, heads: Path2D, e: MapEdgeLayout) {
    const p = e.points;
    path.moveTo(p[0]![0], p[0]![1]);
    let ex: number;
    let ey: number;
    let dir: number;
    if (p.length === 4 && e.from !== e.to) {
        path.bezierCurveTo(p[1]![0], p[1]![1], p[2]![0], p[2]![1], p[3]![0], p[3]![1]);
        ex = p[3]![0];
        ey = p[3]![1];
        dir = p[3]![0] >= p[2]![0] ? 1 : -1;
    } else {
        for (let i = 1; i < p.length; i++) path.lineTo(p[i]![0], p[i]![1]);
        const l = p[p.length - 1]!;
        ex = l[0];
        ey = l[1];
        dir = l[0] >= p[p.length - 2]![0] ? 1 : -1;
    }
    heads.moveTo(ex, ey);
    heads.lineTo(ex - dir * 8, ey - 4);
    heads.lineTo(ex - dir * 8, ey + 4);
    heads.closePath();
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
    const active = props.selectedId ?? hoverId.value;
    const lw = Math.max(1, 1.4 / Math.max(scale, 0.5));
    ctx.lineJoin = 'round';

    const edgeColor = (c: string, hot: boolean) => (c === 'ok' ? (hot ? pal.accent : pal.text3) : pal.danger);
    for (const hot of [false, true]) {
        for (let i = 0; i < layout.edges.length; i++) {
            const e = layout.edges[i]!;
            const isHot = active !== null && (e.from === active || e.to === active);
            if (isHot !== hot) continue;
            const cls = classifyEdge(edgeMeta[i]!);
            const path = new Path2D();
            const heads = new Path2D();
            tracePath(path, heads, e);
            ctx.globalAlpha = active !== null && !hot ? 0.3 : 1;
            ctx.strokeStyle = edgeColor(cls, hot);
            ctx.fillStyle = ctx.strokeStyle;
            ctx.lineWidth = hot ? lw * 1.7 : cls === 'ok' ? lw : lw * 1.3;
            ctx.setLineDash(cls === 'dropped' ? [7, 5] : []);
            ctx.stroke(path);
            ctx.setLineDash([]);
            ctx.fill(heads);
        }
    }
    ctx.globalAlpha = 1;

    // Edge labels (pill background so they read over crossing lines).
    if (scale >= LOD_LABEL) {
        ctx.font = `600 10px ${pal.font}`;
        ctx.textAlign = 'left';
        ctx.textBaseline = 'middle';
        for (let i = 0; i < layout.edges.length; i++) {
            const e = layout.edges[i]!;
            const isHot = active !== null && (e.from === active || e.to === active);
            if (active !== null && !isHot) continue;
            const parts = edgeLabelParts(edgeMeta[i]!);
            const sep = ' · ';
            const widths = parts.map(p => ctx.measureText(p.text).width);
            const total = widths.reduce((a, b) => a + b, 0) + ctx.measureText(sep).width * (parts.length - 1);
            const x0 = e.label[0] - total / 2;
            ctx.fillStyle = pal.surface;
            rrect(ctx, x0 - 4, e.label[1] - 8, total + 8, 16, 4);
            ctx.fill();
            let x = x0;
            parts.forEach((p, k) => {
                if (k > 0) {
                    ctx.fillStyle = pal.text3;
                    ctx.fillText(sep, x, e.label[1]);
                    x += ctx.measureText(sep).width;
                }
                ctx.fillStyle = p.danger ? pal.danger : pal.text2;
                ctx.fillText(p.text, x, e.label[1]);
                x += widths[k]!;
            });
        }
    }

    // Nodes.
    const text = scale >= LOD_TEXT;
    for (const n of layout.nodes) {
        const m = meta.get(n.id)!;
        const bad = (incident.get(n.id) ?? 0) > 0;
        rrect(ctx, n.x, n.y, n.w, n.h, 6);
        ctx.fillStyle = m.external ? pal.s2 : pal.surface;
        ctx.fill();
        ctx.setLineDash(m.external ? [4, 3] : []);
        ctx.lineWidth = n.id === hoverId.value ? 1.8 : 1;
        ctx.strokeStyle = n.id === hoverId.value ? pal.text3 : pal.bstrong;
        ctx.stroke();
        ctx.setLineDash([]);
        ctx.fillStyle = bad ? pal.danger : m.external ? pal.text3 : pal.success;
        ctx.fillRect(n.x + 1, n.y + 5, 3, n.h - 10);
        if (text) {
            ctx.textBaseline = 'alphabetic';
            ctx.textAlign = 'left';
            ctx.font = `600 9px ${pal.font}`;
            ctx.fillStyle = pal.text3;
            ctx.fillText(
                m.external ? (m.namespace ? `EXTERNAL · ${m.namespace}`.toUpperCase() : 'EXTERNAL') : (m.namespace ?? '').toUpperCase(),
                n.x + 11,
                n.y + 15
            );
            ctx.font = `500 12px ${pal.font}`;
            ctx.fillStyle = pal.text;
            ctx.fillText(ellipsize(ctx, nodeLabel(m), n.w - 20), n.x + 11, n.y + 32);
        }
        if (n.id === props.selectedId) {
            rrect(ctx, n.x - 3, n.y - 3, n.w + 6, n.h + 6, 8);
            ctx.lineWidth = 2;
            ctx.strokeStyle = pal.focus;
            ctx.stroke();
        }
    }
}

function fit() {
    if (!layout || !cw || !ch || !layout.nodes.length) return;
    const pad = 28;
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
    const firstTime = cw === 0;
    cw = nw;
    ch = nh;
    dpr = nd;
    cv.width = Math.max(1, Math.round(cw * dpr));
    cv.height = Math.max(1, Math.round(ch * dpr));
    cv.style.width = cw + 'px';
    cv.style.height = ch + 'px';
    if (firstTime || !userMoved) fit();
    else schedule();
}

async function relayout() {
    const my = ++token;
    const g = toRaw(props.graph);
    meta = new Map(g.nodes.map(n => [n.id, n]));
    failure.value = '';
    if (!g.nodes.length) {
        layout = null;
        busy.value = false;
        schedule();
        return;
    }
    busy.value = true;
    try {
        const l = await layoutMap(g);
        if (my !== token) return;
        layout = l;
        byId = new Map(l.nodes.map(n => [n.id, n]));
        // Edge metadata in layout order (the layout drops edges with unknown endpoints).
        const em = new Map(g.edges.map(e => [`${e.from}\u0000${e.to}`, e]));
        edgeMeta = l.edges.map(e => em.get(`${e.from}\u0000${e.to}`)!);
        incident = new Map();
        for (const e of edgeMeta) {
            const rank = classifyEdge(e) === 'ok' ? 0 : 1;
            for (const id of [e.from, e.to]) incident.set(id, Math.max(incident.get(id) ?? 0, rank));
        }
        fit();
    } catch (e) {
        if (e instanceof StaleLayout || my !== token) return;
        failure.value = e instanceof Error ? e.message : String(e);
    } finally {
        if (my === token) busy.value = false;
    }
}
watch(
    () => props.graph,
    () => void relayout()
);
watch(
    () => props.selectedId,
    id => {
        const n = id ? byId.get(id) : undefined;
        if (n) {
            // Bring the selection into view when it is off screen (selected from the list).
            const s = view.scale;
            const sx = n.x * s + view.tx;
            const sy = n.y * s + view.ty;
            if (sx < 0 || sy < 0 || sx + n.w * s > cw || sy + n.h * s > ch) {
                view = { scale: s, tx: cw / 2 - (n.x + n.w / 2) * s, ty: ch / 2 - (n.y + n.h / 2) * s };
                userMoved = true;
            }
        }
        schedule();
    }
);

// ---- keyboard ----
function onKey(ev: KeyboardEvent) {
    if (!layout?.nodes.length) return;
    const order = [...layout.nodes].sort((a, b) => a.x - b.x || a.y - b.y);
    const idx = order.findIndex(n => n.id === props.selectedId);
    let next: LayoutNode | undefined;
    switch (ev.key) {
        case 'ArrowDown':
        case 'ArrowRight':
            next = order[idx < 0 ? 0 : Math.min(order.length - 1, idx + 1)];
            break;
        case 'ArrowUp':
        case 'ArrowLeft':
            next = order[idx < 0 ? 0 : Math.max(0, idx - 1)];
            break;
        case 'Escape':
            emit('select', null);
            return;
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
    if (next) {
        emit('select', next.id);
        const m = meta.get(next.id);
        announce.value = m ? `${nodeLabel(m)}${m.namespace ? ' in ' + m.namespace : ''}${m.external ? ', external' : ''}` : '';
    }
}

// ---- pointer ----
const pointers = new Map<number, { x: number; y: number }>();
let drag: { sx: number; sy: number; tx: number; ty: number; moved: boolean } | null = null;
let pinch: { dist: number; scale: number } | null = null;
const local = (ev: { clientX: number; clientY: number }) => {
    const r = canvas.value!.getBoundingClientRect();
    return { x: ev.clientX - r.left, y: ev.clientY - r.top };
};
function hit(wx: number, wy: number): LayoutNode | null {
    if (!layout) return null;
    for (let i = layout.nodes.length - 1; i >= 0; i--) {
        const n = layout.nodes[i]!;
        if (wx >= n.x && wx <= n.x + n.w && wy >= n.y && wy <= n.y + n.h) return n;
    }
    return null;
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
        setHover(hit((p.x - view.tx) / view.scale, (p.y - view.ty) / view.scale)?.id ?? null);
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
        if (wasClick) emit('select', hit((p.x - view.tx) / view.scale, (p.y - view.ty) / view.scale)?.id ?? null);
        drag = null;
    }
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
function setHover(id: string | null) {
    if (id === hoverId.value) return;
    hoverId.value = id;
    if (canvas.value) canvas.value.style.cursor = id ? 'pointer' : 'grab';
    schedule();
}

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
    token++;
    ro?.disconnect();
    mo?.disconnect();
    mq?.removeEventListener('change', readPalette);
    canvas.value?.removeEventListener('wheel', onWheel);
    if (raf) cancelAnimationFrame(raf);
});
const empty = computed(() => !props.graph.nodes.length);
</script>

<template>
    <div ref="wrap" class="sg" data-testid="servicemap-graph">
        <canvas
            ref="canvas"
            class="cv"
            tabindex="0"
            role="application"
            aria-roledescription="service map"
            aria-label="Service map. Arrow keys move between workloads, plus and minus zoom, zero fits. The list of edges below the map has the same information."
            @keydown="onKey"
            @pointerdown="onDown"
            @pointermove="onMove"
            @pointerup="onUp"
            @pointercancel="onUp"
            @pointerleave="setHover(null)"
        ></canvas>
        <p class="sr-only" role="status" aria-live="polite">{{ announce }}</p>
        <div class="tools floating" role="toolbar" aria-label="Map controls">
            <button class="btn sm ghost" type="button" aria-label="Zoom out" @click="zoomBy(0.8)">−</button>
            <button class="btn sm ghost" type="button" aria-label="Zoom in" @click="zoomBy(1.25)">+</button>
            <button class="btn sm ghost" type="button" @click="fit">Fit</button>
        </div>
        <div v-if="busy" class="state muted" role="status">Laying out…</div>
        <div v-else-if="failure" class="state notice danger" role="alert">Layout failed: {{ failure }}</div>
        <div v-else-if="empty" class="state muted">No traffic in this window.</div>
    </div>
</template>

<style scoped>
.sg {
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
</style>
