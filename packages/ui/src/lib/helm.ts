import type { HelmRelease, HelmRevision, Row } from '@v8s/client';
import type { ShallowRef } from 'vue';

import { shallowRef } from 'vue';

import type { Tone } from './table-decor.ts';

/** Row indexes of the Helm release view (`cluster, name, namespace, revision, status, updated, managedBy, statusTone` after id and version). */
export const HELM_COL = { cluster: 2, name: 3, namespace: 4, revision: 5, status: 6, updated: 7, managedBy: 8, tone: 9 } as const;

export function helmStatusTone(status: string): Tone {
    switch (status) {
        case 'deployed':
            return 'success';
        case 'failed':
            return 'danger';
        case 'pending-install':
        case 'pending-upgrade':
            return 'warning';
        case 'pending-rollback':
            return 'release';
        case 'superseded':
            return 'info';
        default:
            // uninstalling, uninstalled, unknown and anything new
            return 'neutral';
    }
}

export const isPending = (status: string): boolean => status.startsWith('pending-');

/** Retry is offered for a failed release or one stuck in a pending-* state. */
export const canRetry = (status: string): boolean => status === 'failed' || isPending(status);

export const releaseKey = (cluster: string, ns: string, name: string, revision: number | string): string => `${cluster}|${ns}|${name}|${revision}`;

export function parseManagedBy(v: string | null | undefined): { namespace: string; name: string } | null {
    if (!v || !v.startsWith('flux:')) return null;
    const rest = v.slice(5);
    const i = rest.indexOf('/');
    if (i <= 0 || i === rest.length - 1) return null;
    return { namespace: rest.slice(0, i), name: rest.slice(i + 1) };
}

/** Revisions newest first, labelled for a select. */
export function latestRevisionOptions(history: readonly HelmRevision[] | undefined): { revision: number; label: string }[] {
    return [...(history ?? [])]
        .sort((a, b) => b.revision - a.revision)
        .map(h => ({ revision: h.revision, label: `${h.revision} · ${h.status}${h.chart ? ` · ${h.chart}` : ''}` }));
}

/** A rollback target must exist in the history and not be the current revision. */
export function canRollbackTo(history: readonly HelmRevision[] | undefined, current: number): (revision: number) => boolean;
export function canRollbackTo(history: readonly HelmRevision[] | undefined, current: number, revision: number): boolean;
export function canRollbackTo(history: readonly HelmRevision[] | undefined, current: number, revision?: number): boolean | ((r: number) => boolean) {
    const ok = (r: number) => r !== current && r > 0 && !!history?.some(h => h.revision === r);
    return revision === undefined ? ok : ok(revision);
}

export interface ReleaseDetail {
    chart: string;
    chartVersion: string;
    appVersion: string;
}

export interface MergedRow {
    cluster: string;
    name: string;
    namespace: string;
    revision: number;
    status: string;
    updated: number;
    managedBy: string;
    chart: string;
    chartVersion: string;
    appVersion: string;
}

/** Base cells from the view plus the lazily fetched details (empty strings while unknown). */
export function mergeRow(row: Row, details?: ReleaseDetail | null): MergedRow {
    const s = (i: number) => String(row[i] ?? '');
    return {
        cluster: s(HELM_COL.cluster),
        name: s(HELM_COL.name),
        namespace: s(HELM_COL.namespace),
        revision: Number(row[HELM_COL.revision] ?? 0),
        status: s(HELM_COL.status),
        updated: Number(row[HELM_COL.updated] ?? 0),
        managedBy: s(HELM_COL.managedBy),
        chart: details?.chart ?? '',
        chartVersion: details?.chartVersion ?? '',
        appVersion: details?.appVersion ?? ''
    };
}

export interface ReleaseRef {
    cluster: string;
    namespace: string;
    name: string;
    revision: number;
}

export type DetailState = ReleaseDetail | 'loading' | 'unknown';

export interface ReleaseDetailsOptions {
    concurrency?: number;
    capacity?: number;
    /** Schedules the batched `version` bump (default: setTimeout 50 ms). */
    schedule?: (fn: () => void) => void;
}

/**
 * Bounded lazy loader for the chart columns of the release table. Keyed by cluster|ns|name|revision, so a new revision refetches.
 * In-flight requests are de-duplicated, queued ones for rows that scrolled away are dropped, results sit in an LRU, and `version`
 * is bumped at most once per batch window.
 */
export class ReleaseDetails {
    readonly version: ShallowRef<number> = shallowRef(0);
    private readonly cache = new Map<string, ReleaseDetail | null>();
    private readonly inflight = new Set<string>();
    private queue: { key: string; ref: ReleaseRef }[] = [];
    private wanted = new Set<string>();
    private pendingBump = false;
    private readonly concurrency: number;
    private readonly capacity: number;
    private readonly schedule: (fn: () => void) => void;

    constructor(
        private readonly fetch: (ref: ReleaseRef) => Promise<ReleaseDetail>,
        opts: ReleaseDetailsOptions = {}
    ) {
        this.concurrency = opts.concurrency ?? 3;
        this.capacity = opts.capacity ?? 500;
        this.schedule = opts.schedule ?? (fn => void setTimeout(fn, 50));
    }

    state(ref: ReleaseRef): DetailState {
        const k = releaseKey(ref.cluster, ref.namespace, ref.name, ref.revision);
        if (this.cache.has(k)) {
            const v = this.cache.get(k)!;
            // refresh LRU position
            this.cache.delete(k);
            this.cache.set(k, v);
            return v ?? 'unknown';
        }
        // Never fetched (or still queued/in flight): shown as loading; a failed fetch is cached as unknown.
        return 'loading';
    }

    /** The rows now on screen: fetches the missing ones and drops queued fetches for everything else. */
    setVisible(refs: readonly ReleaseRef[]) {
        this.wanted = new Set(refs.map(r => releaseKey(r.cluster, r.namespace, r.name, r.revision)));
        this.queue = this.queue.filter(q => this.wanted.has(q.key));
        for (const r of refs) {
            const key = releaseKey(r.cluster, r.namespace, r.name, r.revision);
            if (this.cache.has(key) || this.inflight.has(key) || this.queue.some(q => q.key === key)) continue;
            this.queue.push({ key, ref: r });
        }
        this.pump();
    }

    /** Test hook and diagnostics. */
    get size() {
        return this.cache.size;
    }
    get active() {
        return this.inflight.size;
    }
    get queued() {
        return this.queue.length;
    }

    private pump() {
        while (this.inflight.size < this.concurrency && this.queue.length) {
            const { key, ref } = this.queue.shift()!;
            this.inflight.add(key);
            this.fetch(ref).then(
                d => this.finish(key, d),
                () => this.finish(key, null)
            );
        }
    }

    private finish(key: string, d: ReleaseDetail | null) {
        this.inflight.delete(key);
        this.cache.set(key, d ? { chart: d.chart, chartVersion: d.chartVersion, appVersion: d.appVersion ?? '' } : null);
        while (this.cache.size > this.capacity) this.cache.delete(this.cache.keys().next().value as string);
        this.bump();
        this.pump();
    }

    private bump() {
        if (this.pendingBump) return;
        this.pendingBump = true;
        this.schedule(() => {
            this.pendingBump = false;
            this.version.value++;
        });
    }
}

export const detailOf = (r: HelmRelease): ReleaseDetail => ({ chart: r.chart, chartVersion: r.chartVersion, appVersion: r.appVersion ?? '' });
