import type { DrainOptions, DrainParams, DrainPod, DrainPreflightResult, JobFrame, JobProgress, JobsStartResult } from '@v8s/client';

import { shallowRef } from 'vue';

import type { WriteDecision } from '@/lib/mutations.ts';

import { newRequestId } from '@/lib/actions.ts';
import { getAgent } from '@/lib/agent.ts';
import { callMutating } from '@/lib/mutations.ts';

export type Tone = 'success' | 'warning' | 'danger' | 'info' | 'neutral';

/** Set by the node action to open the drain dialog; `DrainHost` renders it. `jobId` opens straight into the progress view. */
export const drainRequest = shallowRef<{ cluster: string; node: string; jobId?: string } | null>(null);

export const FINISHED_VISIBLE_MS = 3_600_000;
export const DEFAULT_DRAIN_TIMEOUT = 300;

export interface StartOpts {
    /** One requestId per intent: pass the same one when retrying so the agent de-duplicates. */
    requestId?: string;
    /** Share across attempts so an accepted write context is not asked for twice. */
    decision?: WriteDecision;
    /** The `write_confirm` retry replaces the requestId: keep the new one for a later retry of the same intent. */
    onRequestId?: (id: string) => void;
}

/** Starts an agent-owned job. Returns the job id and the requestId used (reuse it for a retry of the same intent). */
export async function startJob(
    cluster: string,
    type: string,
    params: object,
    opts: StartOpts = {}
): Promise<JobsStartResult & { requestId: string }> {
    let requestId = opts.requestId ?? newRequestId();
    const res = await callMutating<JobsStartResult>(
        cluster,
        'jobs.start',
        { requestId, type, params: params as Record<string, unknown> },
        {
            decision: opts.decision,
            onRequestId: id => {
                requestId = id;
                opts.onRequestId?.(id);
            }
        }
    );
    return { ...res, requestId };
}

export async function cancelJob(id: string): Promise<void> {
    await getAgent().call('jobs.cancel', { jobId: id });
}

export function jobTone(state: string): Tone {
    switch (state) {
        case 'running':
            return 'warning';
        case 'done':
            return 'success';
        case 'failed':
            return 'danger';
        default:
            return 'neutral';
    }
}

export const isActive = (job: Pick<JobFrame, 'state'>): boolean => job.state === 'running';

const TYPE_LABELS: Record<string, string> = {
    drain: 'Drain',
    'helm.rollback': 'Helm rollback',
    'helm.uninstall': 'Helm uninstall',
    'helm.retry': 'Helm retry'
};

export function jobLabel(job: Pick<JobFrame, 'type' | 'subject'>): string {
    const base = TYPE_LABELS[job.type] ?? job.type;
    return job.subject ? `${base} ${job.subject}` : base;
}

/** "42 s", "3 min 5 s", "1 h 2 min". Running jobs measure to `now`. */
export function jobDuration(job: Pick<JobFrame, 'startedAt' | 'endedAt'>, now = Date.now()): string {
    const s = Math.max(0, Math.round(((job.endedAt ?? now) - job.startedAt) / 1000));
    if (s < 60) return `${s} s`;
    const m = Math.floor(s / 60);
    if (m < 60) return `${m} min ${s % 60} s`;
    return `${Math.floor(m / 60)} h ${m % 60} min`;
}

export interface ItemRow {
    item: string;
    state: string;
    message: string;
    at: number;
}

export interface ItemRows {
    rows: ItemRow[];
    log: JobProgress[];
}

/** Latest state per `item`, in first-seen order; entries without an item form the log. */
export function itemRows(job: Pick<JobFrame, 'progress'>): ItemRows {
    const idx = new Map<string, number>();
    const rows: ItemRow[] = [];
    const log: JobProgress[] = [];
    for (const p of job.progress ?? []) {
        if (!p.item) {
            log.push(p);
            continue;
        }
        const i = idx.get(p.item);
        if (i === undefined) {
            idx.set(p.item, rows.length);
            rows.push({ item: p.item, state: p.state ?? '', message: p.message, at: p.at });
        } else {
            const r = rows[i];
            r.state = p.state ?? r.state;
            r.message = p.message;
            r.at = p.at;
        }
    }
    return { rows, log };
}

const DONE_STATES = new Set(['evicted', 'done', 'deleted']);

export function progressSummary(job: Pick<JobFrame, 'type' | 'progress'>): string {
    const { rows, log } = itemRows(job);
    if (!rows.length) return log.length ? log[log.length - 1].message : '';
    const done = rows.filter(r => DONE_STATES.has(r.state)).length;
    return job.type === 'drain' ? `${done} of ${rows.length} pods evicted` : `${done} of ${rows.length} items done`;
}

/** Running first (newest first), then finished newest-first; finished jobs older than 1 h are hidden. */
export function groupJobs(jobs: readonly JobFrame[], now = Date.now()): JobFrame[] {
    const running = jobs.filter(isActive).sort((a, b) => b.startedAt - a.startedAt);
    const finished = jobs
        .filter(j => !isActive(j) && now - (j.endedAt ?? j.startedAt) <= FINISHED_VISIBLE_MS)
        .sort((a, b) => (b.endedAt ?? b.startedAt) - (a.endedAt ?? a.startedAt));
    return [...running, ...finished];
}

export function drainParams(cluster: string, node: string, options: DrainOptions): DrainParams {
    const o: DrainOptions = {
        ignoreDaemonSets: options.ignoreDaemonSets ?? true,
        deleteEmptyDirData: !!options.deleteEmptyDirData,
        force: !!options.force,
        timeoutSeconds: options.timeoutSeconds && options.timeoutSeconds > 0 ? options.timeoutSeconds : DEFAULT_DRAIN_TIMEOUT,
        disableEviction: !!options.disableEviction
    };
    if (options.gracePeriodSeconds != null && Number.isFinite(options.gracePeriodSeconds) && options.gracePeriodSeconds >= 0) {
        o.gracePeriodSeconds = Math.floor(options.gracePeriodSeconds);
    }
    if (options.podSelector) o.podSelector = options.podSelector;
    return { cluster, node, options: o };
}

export interface Blocker {
    option: 'force' | 'deleteEmptyDirData' | 'ignoreDaemonSets';
    message: string;
    count: number;
}

export interface PreflightSummary {
    counts: { evict: number; blocked: number; unmanaged: number; emptyDir: number; daemonSets: number; mirror: number; finished: number };
    /** Pods the drain acts on (evict + blocked + unmanaged when forced). */
    affected: number;
    /** kubectl fails before evicting anything for these; Start must stay disabled until the option is on. */
    blockers: Blocker[];
    warnings: string[];
}

export function preflightSummary(r: DrainPreflightResult, options: DrainOptions): PreflightSummary {
    const n = (l?: DrainPod[]) => l?.length ?? 0;
    const counts = {
        evict: n(r.evict),
        blocked: n(r.blocked),
        unmanaged: n(r.unmanaged),
        emptyDir: n(r.emptyDir),
        daemonSets: n(r.daemonSets),
        mirror: n(r.mirror),
        finished: n(r.finished)
    };
    const blockers: Blocker[] = [];
    const warnings: string[] = [];
    if (counts.unmanaged && !options.force) {
        blockers.push({
            option: 'force',
            count: counts.unmanaged,
            message: `${counts.unmanaged} pod${counts.unmanaged === 1 ? ' has' : 's have'} no controller and would be lost for good. Turn on "Force" to drain them, or the drain fails before evicting anything.`
        });
    }
    if (counts.emptyDir && !options.deleteEmptyDirData) {
        blockers.push({
            option: 'deleteEmptyDirData',
            count: counts.emptyDir,
            message: `${counts.emptyDir} pod${counts.emptyDir === 1 ? ' uses' : 's use'} emptyDir volumes whose data would be lost. Turn on "Delete emptyDir data", or the drain fails before evicting anything.`
        });
    }
    if (counts.daemonSets && options.ignoreDaemonSets === false) {
        blockers.push({
            option: 'ignoreDaemonSets',
            count: counts.daemonSets,
            message: `${counts.daemonSets} DaemonSet pod${counts.daemonSets === 1 ? '' : 's'} cannot be evicted. Turn on "Ignore DaemonSets", or the drain fails before evicting anything.`
        });
    }
    if (counts.blocked) {
        warnings.push(
            options.disableEviction
                ? `${counts.blocked} pod${counts.blocked === 1 ? ' is' : 's are'} covered by a PodDisruptionBudget that allows no disruptions; with eviction disabled they are deleted anyway.`
                : `${counts.blocked} pod${counts.blocked === 1 ? ' is' : 's are'} covered by a PodDisruptionBudget that allows no disruptions: the drain will wait and retry every 5 s until the timeout.`
        );
    }
    if (options.disableEviction) warnings.push('Eviction is disabled: pods are deleted and PodDisruptionBudgets are NOT honoured.');
    const affected = counts.evict + counts.blocked + (options.force ? counts.unmanaged : 0);
    return { counts, affected, blockers, warnings };
}

export function itemTone(state: string): Tone {
    switch (state) {
        case 'evicted':
        case 'done':
        case 'deleted':
            return 'success';
        case 'waiting-pdb':
        case 'evicting':
            return 'warning';
        case 'error':
            return 'danger';
        default:
            return 'neutral';
    }
}

export function itemLabel(state: string): string {
    return state === 'waiting-pdb' ? 'waiting on PDB' : state || 'pending';
}

export const CANCEL_CONFIRM = 'Cancel stops further evictions and leaves the node cordoned.';
