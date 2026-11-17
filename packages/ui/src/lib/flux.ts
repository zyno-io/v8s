import type { Cell } from '@v8s/client';

import { shallowRef } from 'vue';

export type FluxTone = 'success' | 'warning' | 'danger' | 'info' | 'release' | 'neutral';

export interface FluxKind {
    /** Short key used in test ids and routes. */
    id: string;
    kind: string;
    group: string;
    plural: string;
    /** A source-controller kind (no "reconcile with source", no sourceRef). */
    source: boolean;
    /** Preferred version, for building a gvr when discovery has not named one. */
    version: string;
}

export const FLUX_KINDS: readonly FluxKind[] = [
    { id: 'gitrepository', kind: 'GitRepository', group: 'source.toolkit.fluxcd.io', plural: 'gitrepositories', source: true, version: 'v1' },
    { id: 'ocirepository', kind: 'OCIRepository', group: 'source.toolkit.fluxcd.io', plural: 'ocirepositories', source: true, version: 'v1' },
    { id: 'helmrepository', kind: 'HelmRepository', group: 'source.toolkit.fluxcd.io', plural: 'helmrepositories', source: true, version: 'v1' },
    { id: 'helmchart', kind: 'HelmChart', group: 'source.toolkit.fluxcd.io', plural: 'helmcharts', source: true, version: 'v1' },
    { id: 'bucket', kind: 'Bucket', group: 'source.toolkit.fluxcd.io', plural: 'buckets', source: true, version: 'v1' },
    { id: 'kustomization', kind: 'Kustomization', group: 'kustomize.toolkit.fluxcd.io', plural: 'kustomizations', source: false, version: 'v1' },
    { id: 'helmrelease', kind: 'HelmRelease', group: 'helm.toolkit.fluxcd.io', plural: 'helmreleases', source: false, version: 'v2' }
];

/** Matches on group + plural only, so any served version works. `<group>/<version>/<plural>`. */
export function fluxKindOf(gvr: string): FluxKind | undefined {
    const i = gvr.indexOf('/');
    const j = gvr.lastIndexOf('/');
    if (i < 0 || j <= i) return undefined;
    const group = gvr.slice(0, i);
    const plural = gvr.slice(j + 1);
    return FLUX_KINDS.find(k => k.group === group && k.plural === plural);
}

export const isFluxGvr = (gvr: string): boolean => fluxKindOf(gvr) !== undefined;

export const isHelmReleaseGvr = (gvr: string): boolean => fluxKindOf(gvr)?.id === 'helmrelease';

// ---- row access ---------------------------------------------------------------------------------

/** Column order of every Flux view (agent/internal/flux/project.go), cluster first. */
export const FLUX_COLS = [
    'cluster',
    'name',
    'namespace',
    'ready',
    'suspended',
    'revision',
    'source',
    'interval',
    'lastReconcile',
    'message',
    'requestedAt',
    'lastHandled',
    'dependsOn',
    'statusTone',
    'createdAt'
] as const;
export type FluxCol = (typeof FLUX_COLS)[number];

const OFF = Object.fromEntries(FLUX_COLS.map((k, i) => [k, i])) as Record<FluxCol, number>;

/** Row index of the `cluster` column (rows are `[id, version, ...cells]`). */
export const FLUX_BASE = 2;

/** Base (row index of the first Flux column) given any known Flux column and its row index. */
export const baseOf = (key: string, idx: number): number => (key in OFF ? idx - OFF[key as FluxCol] : FLUX_BASE);

/** Base from a view schema: cluster is the first column, so its position is the base. Falls back to the default layout. */
export function baseFromColumns(columns: readonly { key: string }[] | null | undefined): number {
    const i = columns?.findIndex(c => c.key === 'ready') ?? -1;
    return i >= 0 ? i - OFF.ready + FLUX_BASE : FLUX_BASE;
}

type RowLike = readonly (Cell | number | string | undefined)[];

export const cellStr = (row: RowLike, base: number, col: FluxCol): string => {
    const v = row[base + OFF[col]];
    return v === undefined || v === null ? '' : String(v);
};
export const cellNum = (row: RowLike, base: number, col: FluxCol): number => {
    const v = Number(row[base + OFF[col]]);
    return Number.isFinite(v) ? v : 0;
};

export interface FluxCells {
    cluster: string;
    name: string;
    namespace: string;
    ready: string;
    suspended: boolean;
    revision: string;
    source: string;
    interval: string;
    lastReconcile: number;
    message: string;
    requestedAt: string;
    lastHandled: string;
    dependsOn: string;
    tone: number;
    createdAt: number;
}

export function readCells(row: RowLike, base = FLUX_BASE): FluxCells {
    return {
        cluster: cellStr(row, base, 'cluster'),
        name: cellStr(row, base, 'name'),
        namespace: cellStr(row, base, 'namespace'),
        ready: cellStr(row, base, 'ready'),
        suspended: cellStr(row, base, 'suspended') === 'true',
        revision: cellStr(row, base, 'revision'),
        source: cellStr(row, base, 'source'),
        interval: cellStr(row, base, 'interval'),
        lastReconcile: cellNum(row, base, 'lastReconcile'),
        message: cellStr(row, base, 'message'),
        requestedAt: cellStr(row, base, 'requestedAt'),
        lastHandled: cellStr(row, base, 'lastHandled'),
        dependsOn: cellStr(row, base, 'dependsOn'),
        tone: cellNum(row, base, 'statusTone'),
        createdAt: cellNum(row, base, 'createdAt')
    };
}

// ---- tone / ready tag ---------------------------------------------------------------------------

const TONES: FluxTone[] = ['neutral', 'success', 'warning', 'danger', 'info', 'release'];
export const toneName = (t: number): FluxTone => TONES[t] ?? 'neutral';

export interface ReadyTag {
    text: string;
    tone: FluxTone;
}

/**
 * The Ready tag. Suspended is neutral. Otherwise the text keeps the source word and reason (`Ready`,
 * `Not ready · ArtifactFailed`, `Unknown`) and the tone is the agent's computed status tone (so Stalled and progressing
 * are right), falling back to the text when the tone is absent.
 */
export function readyTag(row: RowLike, base = FLUX_BASE): ReadyTag {
    return readyTagOf(readCells(row, base));
}

export function readyTagOf(c: Pick<FluxCells, 'ready' | 'suspended' | 'tone'>): ReadyTag {
    if (c.suspended) return { text: 'Suspended', tone: 'neutral' };
    const raw = c.ready.trim();
    let text: string;
    let derived: FluxTone;
    if (raw === 'True') {
        text = 'Ready';
        derived = 'success';
    } else if (raw === 'False' || raw.startsWith('False')) {
        const reason = raw.replace(/^False\s*·?\s*/, '');
        text = reason ? `Not ready · ${reason}` : 'Not ready';
        derived = 'danger';
    } else {
        text = 'Unknown';
        derived = 'warning';
    }
    // Tone 0 (neutral) on a non-suspended object carries no meaning: use the text-derived tone.
    return { text, tone: c.tone > 0 ? toneName(c.tone) : derived };
}

// ---- revision / message -------------------------------------------------------------------------

const SHA = /([0-9a-f]{7})[0-9a-f]{5,}/g;

/** Git `main@sha1:abcdef0123…` -> `main@abcdef0`; chart versions and short strings are unchanged. */
export function shortRevision(rev: string): string {
    if (!rev) return '';
    return rev.replace(/sha(?:1|256|512):/, '').replace(SHA, '$1');
}

export function truncateMessage(msg: string, max = 120): string {
    const one = msg.replace(/\s+/g, ' ').trim();
    return one.length <= max ? one : one.slice(0, max - 1).trimEnd() + '…';
}

// ---- intervals ----------------------------------------------------------------------------------

const UNIT_MS: Record<string, number> = { ms: 1, s: 1000, m: 60_000, h: 3_600_000 };

/** Go duration (`10m`, `1h30m`, `30s`) to ms; 0 when unparseable. */
export function parseInterval(s: string): number {
    if (!s || !/^(\d+(\.\d+)?(ms|s|m|h))+$/.test(s)) return 0;
    let total = 0;
    for (const m of s.matchAll(/(\d+(?:\.\d+)?)(ms|s|m|h)/g)) total += parseFloat(m[1]) * UNIT_MS[m[2]];
    return Math.round(total);
}

/** Not reconciled within 3x its interval. Unknown interval or last-reconcile time is never stale. */
export function isStale(interval: string, lastReconcileMs: number, nowMs: number, factor = 3): boolean {
    const iv = parseInterval(interval);
    return iv > 0 && lastReconcileMs > 0 && nowMs - lastReconcileMs > iv * factor;
}

// ---- reconcile state ----------------------------------------------------------------------------

export type ReconcileState = 'idle' | 'reconciling' | 'resolved';

export const RECONCILE_EXPIRY_MS = 5 * 60_000;

/**
 * `reconciling`: a pending token is known and `lastHandled` has not reached it, or (no pending token) the object carries a
 * `requestedAt` that is newer than anything handled and still recent. `resolved`: the pending token was handled.
 */
export function reconcileState(row: RowLike, pendingToken?: string, base = FLUX_BASE, nowMs: number = Date.now()): ReconcileState {
    const requestedAt = cellStr(row, base, 'requestedAt');
    const lastHandled = cellStr(row, base, 'lastHandled');
    if (pendingToken) return lastHandled === pendingToken ? 'resolved' : 'reconciling';
    if (requestedAt && requestedAt !== lastHandled) {
        if (cellStr(row, base, 'suspended') === 'true') return 'idle'; // suspended objects never handle requests
        const t = Date.parse(requestedAt);
        if (Number.isFinite(t) && nowMs - t > RECONCILE_EXPIRY_MS) return 'idle';
        return 'reconciling';
    }
    return 'idle';
}

// ---- pending store ------------------------------------------------------------------------------

export interface PendingReconcile {
    token: string;
    since: number;
}

export const objectKey = (r: { cluster: string; gvr: string; namespace?: string; name?: string }): string =>
    `${r.cluster}|${r.gvr}|${r.namespace ?? ''}|${r.name ?? ''}`;

/** Plain module state (not reactive): `version` is the only reactive signal, bumped on every change. */
export const pendingReconciles = new Map<string, PendingReconcile>();
export const version = shallowRef(0);
const bump = () => version.value++;

export function trackReconcile(key: string, token: string | undefined, now: number = Date.now()): void {
    if (!token) return;
    pendingReconciles.set(key, { token, since: now });
    bump();
    const t = setTimeout(() => sweep(), RECONCILE_EXPIRY_MS + 50);
    (t as unknown as { unref?: () => void }).unref?.();
}

/** The live pending token of an object (expired entries are dropped lazily). */
export function pendingToken(key: string, now: number = Date.now()): string | undefined {
    const p = pendingReconciles.get(key);
    if (!p) return undefined;
    if (now - p.since > RECONCILE_EXPIRY_MS) return undefined;
    return p.token;
}

/** Clears the entry once the object reports it handled the token. Returns whether it cleared. */
export function settle(key: string, lastHandled: string): boolean {
    const p = pendingReconciles.get(key);
    if (!p || p.token !== lastHandled) return false;
    pendingReconciles.delete(key);
    bump();
    return true;
}

/** Drops entries older than the expiry; returns how many. */
export function sweep(now: number = Date.now()): number {
    let n = 0;
    for (const [k, p] of pendingReconciles)
        if (now - p.since > RECONCILE_EXPIRY_MS) {
            pendingReconciles.delete(k);
            n++;
        }
    if (n) bump();
    return n;
}

export function clearPending(): void {
    if (!pendingReconciles.size) return;
    pendingReconciles.clear();
    bump();
}

// ---- overview aggregation -----------------------------------------------------------------------

export interface KindCounts {
    total: number;
    ready: number;
    notready: number;
    suspended: number;
}

export interface OverviewRow extends KindCounts {
    /** Reconciling, unknown or otherwise not ready nor failed: total minus the other buckets (never negative). */
    other: number;
}

export interface Overview {
    rows: Record<string, OverviewRow>;
    totals: OverviewRow;
    /** Nothing failing. */
    healthy: boolean;
}

export function aggregateOverview(counts: Record<string, KindCounts | undefined>): Overview {
    const rows: Record<string, OverviewRow> = {};
    const totals: OverviewRow = { total: 0, ready: 0, notready: 0, suspended: 0, other: 0 };
    for (const [id, c] of Object.entries(counts)) {
        if (!c) continue;
        const other = Math.max(0, c.total - c.ready - c.notready - c.suspended);
        rows[id] = { ...c, other };
        totals.total += c.total;
        totals.ready += c.ready;
        totals.notready += c.notready;
        totals.suspended += c.suspended;
        totals.other += other;
    }
    return { rows, totals, healthy: totals.notready === 0 };
}
