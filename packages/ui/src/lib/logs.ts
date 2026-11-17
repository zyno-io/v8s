import type { LogOpenOptions, ObjectRef } from '@v8s/client';

// Pure helpers behind LogsPane / LogViewer (no Vue, no DOM) so they can be unit tested.

// ---- targets ----

const WORKLOAD_GVRS = new Set([
    'apps/v1/deployments',
    'apps/v1/statefulsets',
    'apps/v1/daemonsets',
    'apps/v1/replicasets',
    'batch/v1/jobs',
    'v1/services'
]);

/** `pod` for a single pod, `workload` for kinds whose pods the agent resolves from the selector, null when there are no logs. */
export function logTargetKind(gvr: string): 'pod' | 'workload' | null {
    if (gvr === 'v1/pods') return 'pod';
    return WORKLOAD_GVRS.has(gvr) ? 'workload' : null;
}

export const canShowLogs = (gvr: string): boolean => logTargetKind(gvr) !== null;

// ---- containers ----

export interface ContainerInfo {
    name: string;
    init: boolean;
    ephemeral?: boolean;
    restarts: number;
}

const DEFAULT_CONTAINER_ANNOTATION = 'kubectl.kubernetes.io/default-container';

type Rec = Record<string, unknown>;
const rec = (v: unknown): Rec | undefined => (v && typeof v === 'object' && !Array.isArray(v) ? (v as Rec) : undefined);
const arr = (v: unknown): Rec[] => (Array.isArray(v) ? v.map(rec).filter((x): x is Rec => !!x) : []);

/** Containers of a parsed pod: regular, then init, then ephemeral, each with its restart count. Empty for null. */
export function containersOf(parsed: Rec | null | undefined): ContainerInfo[] {
    if (!parsed) return [];
    const spec = rec(parsed.spec);
    const status = rec(parsed.status);
    const restarts = new Map<string, number>();
    for (const key of ['containerStatuses', 'initContainerStatuses', 'ephemeralContainerStatuses']) {
        for (const s of arr(status?.[key])) {
            if (typeof s.name === 'string') restarts.set(s.name, Number(s.restartCount) || 0);
        }
    }
    const out: ContainerInfo[] = [];
    const add = (list: unknown, init: boolean, ephemeral: boolean) => {
        for (const c of arr(list)) {
            if (typeof c.name !== 'string') continue;
            out.push({ name: c.name, init, ...(ephemeral ? { ephemeral } : {}), restarts: restarts.get(c.name) ?? 0 });
        }
    };
    add(spec?.containers, false, false);
    add(spec?.initContainers, true, false);
    add(spec?.ephemeralContainers, false, true);
    return out;
}

/** The annotated default container when it exists, else the first non-init container, else the first container, else ''. */
export function defaultContainer(parsed: Rec | null | undefined, list: readonly ContainerInfo[] = containersOf(parsed)): string {
    const ann = rec(rec(parsed?.metadata)?.annotations)?.[DEFAULT_CONTAINER_ANNOTATION];
    if (typeof ann === 'string' && list.some(c => c.name === ann)) return ann;
    return (list.find(c => !c.init && !c.ephemeral) ?? list.find(c => !c.init) ?? list[0])?.name ?? '';
}

/** A previous container instance exists only after a restart. */
export function hasPrevious(list: readonly ContainerInfo[], name: string): boolean {
    return (list.find(c => c.name === name)?.restarts ?? 0) > 0;
}

// ---- stream params ----

export const TAIL_OPTIONS = [1000, 5000, 10000] as const;
export const DEFAULT_TAIL = 1000;
export const CAPACITY_PHONE = 5000;
export const CAPACITY_DESKTOP = 50000;

export const MAX_BYTES_DESKTOP = 32 * 1024 * 1024;
export const MAX_BYTES_PHONE = 8 * 1024 * 1024;
export const logMaxBytes = (phone: boolean): number => (phone ? MAX_BYTES_PHONE : MAX_BYTES_DESKTOP);

export const logCapacity = (phone: boolean): number => (phone ? CAPACITY_PHONE : CAPACITY_DESKTOP);

export interface StreamOptions {
    container?: string;
    follow: boolean;
    tailLines: number;
    previous?: boolean;
    phone: boolean;
}

/** Params for `agent.openStream('logs', ...)`. Timestamps are always requested; the UI hides them with the toggle. */
export function streamParams(target: ObjectRef, o: StreamOptions): LogOpenOptions {
    const kind = logTargetKind(target.gvr);
    const ref: ObjectRef = { cluster: target.cluster, gvr: target.gvr, namespace: target.namespace, name: target.name };
    const base: LogOpenOptions = {
        ref,
        timestamps: true,
        follow: o.follow && !o.previous,
        tailLines: o.tailLines,
        capacity: logCapacity(o.phone),
        maxBytes: logMaxBytes(o.phone)
    };
    if (kind === 'pod') {
        if (o.container) base.container = o.container;
        if (o.previous) base.previous = true;
        base.multi = false;
    } else {
        base.multi = true;
    }
    return base;
}

/** Params for the full-log download: same target, no tail limit, no follow. */
export function downloadParams(
    target: ObjectRef,
    o: Pick<StreamOptions, 'container' | 'previous'>
): Omit<LogOpenOptions, 'capacity' | 'maxBytes' | 'multi'> {
    const p = streamParams(target, { ...o, follow: false, tailLines: 0, phone: false });
    const { capacity: _c, maxBytes: _b, multi: _m, tailLines: _t, ...rest } = p;
    return rest;
}

export function downloadFilename(target: ObjectRef, container?: string): string {
    const safe = (s: string) => s.replace(/[^A-Za-z0-9._-]+/g, '_');
    return `${safe(target.name ?? 'log')}${container && logTargetKind(target.gvr) === 'pod' ? '-' + safe(container) : ''}.log`;
}

// ---- row layout ----

export const ROW_H = 18;
/**
 * Wrap mode is a fixed 2-line row: the virtualizer uses a constant size (no measurement), so a longer line is clamped to two lines
 * and the rest is cut off. Download or turn wrap off to see the full line.
 */
export const ROW_H_WRAP = 36;
export const rowHeight = (wrap: boolean): number => (wrap ? ROW_H_WRAP : ROW_H);
export const MARKER_H = 26;
export const OVERSCAN = 12;
/** Extra lines on each side of the visible range requested from the worker. */
export const VIEW_PAD = 60;
export const BOTTOM_TOLERANCE = 4;

// ---- follow state machine ----

export function isAtBottom(scrollTop: number, clientHeight: number, scrollHeight: number, tolerance = BOTTOM_TOLERANCE): boolean {
    return scrollHeight - (scrollTop + clientHeight) <= tolerance;
}

/** User scrolled up away from the bottom while following: pause. Only an upward move counts, content growth does not. */
export function shouldPauseFollow(prevTop: number, top: number, clientHeight: number, scrollHeight: number): boolean {
    return top < prevTop && !isAtBottom(top, clientHeight, scrollHeight);
}

/** scrollTop change that keeps the user's view still when `first` advances while following is paused. */
export function dropCompensation(prevFirst: number, first: number, rowH: number): number {
    return first > prevFirst ? -((first - prevFirst) * rowH) : 0;
}

/**
 * Rows to paint while following: the last screenful plus overscan, computed from the list size alone. The virtualizer's own scroll
 * offset trails the real scrollTop (it only learns of our pin-to-bottom writes from the async scroll event), so under a fast stream
 * its window can sit hundreds of rows behind the data the worker holds and the viewport paints blank.
 */
export function followItems(
    count: number,
    viewH: number,
    rowH: number,
    paddingStart: number,
    overscan = OVERSCAN
): { index: number; start: number }[] {
    const n = Math.min(count, Math.ceil(viewH / rowH) + 1 + overscan);
    const out: { index: number; start: number }[] = [];
    for (let i = count - n; i < count; i++) out.push({ index: i, start: paddingStart + i * rowH });
    return out;
}

/** Lines that arrived since follow paused. */
export const newLinesSince = (pausedAtTotal: number, total: number): number => Math.max(0, total - pausedAtTotal);

export function formatNewLines(n: number): string {
    return `${n.toLocaleString('en-US')} new ${n === 1 ? 'line' : 'lines'}`;
}

/** Scroll position that centres row `idx` (index in the rendered list). */
export function centerScrollTop(idx: number, rowH: number, clientHeight: number, paddingStart: number): number {
    return Math.max(0, paddingStart + idx * rowH + rowH / 2 - clientHeight / 2);
}

/** Range for `handle.setView`: absolute first line (clamped at 0) and a count including the pad on both sides. */
export function viewRange(firstIdx: number, lastIdx: number, first: number, pad = VIEW_PAD): { first: number; count: number } {
    const lo = Math.max(0, first + firstIdx - pad);
    const visible = Math.max(0, lastIdx - firstIdx + 1);
    return { first: lo, count: visible + 2 * pad };
}

// ---- timestamps ----

/** `2024-05-01T12:00:00.123456789Z` -> `2024-05-01 12:00:00.123`. */
export function shortTimestamp(ts: string): string {
    const m = /^(\d{4}-\d\d-\d\d)T(\d\d:\d\d:\d\d)(?:\.(\d+))?/.exec(ts);
    if (!m) return ts;
    return `${m[1]} ${m[2]}${m[3] ? '.' + m[3].slice(0, 3).padEnd(3, '0') : ''}`;
}

// ---- colour and runs ----

export const SOURCE_PALETTE_SIZE = 8;

/** Stable palette slot for a `<pod>/<container>` label (FNV-1a over the pod part so containers of one pod share hue). */
export function colorForSource(name: string): number {
    const pod = name.split('/')[0] ?? name;
    let h = 0x811c9dc5;
    for (let i = 0; i < pod.length; i++) {
        h ^= pod.charCodeAt(i);
        h = Math.imul(h, 0x01000193);
    }
    return (h >>> 0) % SOURCE_PALETTE_SIZE;
}

export const sourceVar = (name: string): string => `var(--log-src-${colorForSource(name)})`;

const RGB_FLAG = 0x1000000;
const BOLD = 1;
const DIM = 2;
const ITALIC = 4;
const UNDERLINE = 8;

/** CSS variable for ANSI palette index 0..15. */
export const paletteVar = (i: number): string => `--ansi-${i}`;

/** CSS colour for an encoded span colour: var() for the palette, #rrggbb for rgb, '' for none. */
export function cssColor(c: number): string {
    if (c < 0) return '';
    if (c & RGB_FLAG) return '#' + (c & 0xffffff).toString(16).padStart(6, '0');
    return c < 16 ? `var(${paletteVar(c)})` : '';
}

export interface Run {
    text: string;
    fg: number;
    bg: number;
    flags: number;
    hl: boolean;
}

/** Splits `text` at span and highlight boundaries. A line without spans or hits is a single run. */
export function splitRuns(text: string, spans?: readonly number[], hl?: readonly number[]): Run[] {
    if (!(spans && spans.length) && !(hl && hl.length)) return [{ text, fg: -1, bg: -1, flags: 0, hl: false }];
    const cuts = new Set<number>([0, text.length]);
    for (let i = 0; spans && i + 4 < spans.length; i += 5) {
        cuts.add(clamp(spans[i]!, text.length));
        cuts.add(clamp(spans[i + 1]!, text.length));
    }
    for (let i = 0; hl && i + 1 < hl.length; i += 2) {
        cuts.add(clamp(hl[i]!, text.length));
        cuts.add(clamp(hl[i + 1]!, text.length));
    }
    const points = [...cuts].sort((a, b) => a - b);
    const out: Run[] = [];
    let si = 0;
    let hi = 0;
    for (let p = 0; p + 1 < points.length; p++) {
        const a = points[p]!;
        const b = points[p + 1]!;
        let fg = -1;
        let bg = -1;
        let flags = 0;
        if (spans) {
            while (si < spans.length && spans[si + 1]! <= a) si += 5;
            if (si < spans.length && spans[si]! <= a) {
                fg = spans[si + 2]!;
                bg = spans[si + 3]!;
                flags = spans[si + 4]!;
            }
        }
        let on = false;
        if (hl) {
            while (hi < hl.length && hl[hi + 1]! <= a) hi += 2;
            on = hi < hl.length && hl[hi]! <= a;
        }
        const prev = out[out.length - 1];
        if (prev && prev.fg === fg && prev.bg === bg && prev.flags === flags && prev.hl === on) prev.text += text.slice(a, b);
        else out.push({ text: text.slice(a, b), fg, bg, flags, hl: on });
    }
    return out;
}

const clamp = (n: number, max: number) => Math.min(Math.max(n, 0), max);

/** Inline style for a run: colours (palette var or rgb), weight, dim, italic, underline. */
export function runStyle(r: Run): string {
    let s = '';
    const fg = cssColor(r.fg);
    const bg = cssColor(r.bg);
    if (fg) s += `color:${fg};`;
    if (bg) s += `background:${bg};`;
    if (r.flags & BOLD) s += 'font-weight:700;';
    if (r.flags & DIM) s += 'opacity:.7;';
    if (r.flags & ITALIC) s += 'font-style:italic;';
    if (r.flags & UNDERLINE) s += 'text-decoration:underline;';
    return s;
}
