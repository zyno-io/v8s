import type { DrainPreflightResult, JobFrame } from '@v8s/client';

import { beforeEach, describe, expect, it, vi } from 'vitest';

const call = vi.fn();
const callMutating = vi.fn();
vi.mock('../src/lib/agent.ts', () => ({ getAgent: () => ({ call }) }));
vi.mock('../src/lib/mutations.ts', () => ({ callMutating: (...a: unknown[]) => callMutating(...a) }));

import {
    cancelJob,
    drainParams,
    groupJobs,
    isActive,
    itemRows,
    jobDuration,
    jobLabel,
    jobTone,
    preflightSummary,
    progressSummary,
    startJob
} from '../src/lib/jobs.ts';

const job = (o: Partial<JobFrame>): JobFrame => ({ jobId: 'j', type: 'drain', state: 'running', startedAt: 0, progress: [], ...o });
const pod = (name: string, extra = {}) => ({ namespace: 'ns', name, ...extra });
const pre = (o: Partial<DrainPreflightResult>): DrainPreflightResult => ({
    node: 'node-3',
    cordoned: false,
    usedContext: '',
    evict: [],
    blocked: [],
    unmanaged: [],
    emptyDir: [],
    daemonSets: [],
    mirror: [],
    finished: [],
    ...o
});

beforeEach(() => {
    call.mockReset();
    callMutating.mockReset();
});

describe('start/cancel', () => {
    it('sends one requestId and exposes it', async () => {
        callMutating.mockResolvedValue({ jobId: 'j1' });
        const a = await startJob('c1', 'drain', { x: 1 });
        expect(a.jobId).toBe('j1');
        expect(callMutating).toHaveBeenCalledWith('c1', 'jobs.start', { requestId: a.requestId, type: 'drain', params: { x: 1 } });
        await startJob('c1', 'drain', {}, { requestId: a.requestId });
        expect(callMutating.mock.calls[1][2].requestId).toBe(a.requestId);
    });
    it('cancels', async () => {
        await cancelJob('j1');
        expect(call).toHaveBeenCalledWith('jobs.cancel', { jobId: 'j1' });
    });
});

describe('labels', () => {
    it('tone, label, active, duration', () => {
        expect(['running', 'done', 'failed', 'cancelled'].map(jobTone)).toEqual(['warning', 'success', 'danger', 'neutral']);
        expect(jobLabel({ type: 'drain', subject: 'node-3' })).toBe('Drain node-3');
        expect(jobLabel({ type: 'helm.rollback', subject: 'podinfo' })).toBe('Helm rollback podinfo');
        expect(isActive(job({}))).toBe(true);
        expect(jobDuration({ startedAt: 0, endedAt: 42_000 })).toBe('42 s');
        expect(jobDuration({ startedAt: 0, endedAt: 185_000 })).toBe('3 min 5 s');
        expect(jobDuration({ startedAt: 0 }, 3_720_000)).toBe('1 h 2 min');
    });
});

describe('itemRows', () => {
    const j = job({
        progress: [
            { at: 1, message: 'cordoned' },
            { at: 2, message: 'waiting', item: 'a/x', state: 'waiting-pdb' },
            { at: 3, message: 'evicting', item: 'a/y', state: 'evicting' },
            { at: 4, message: 'evicted', item: 'a/x', state: 'evicted' }
        ]
    });
    it('keeps first-seen order with the latest state', () => {
        const { rows, log } = itemRows(j);
        expect(rows.map(r => [r.item, r.state])).toEqual([
            ['a/x', 'evicted'],
            ['a/y', 'evicting']
        ]);
        expect(log).toHaveLength(1);
    });
    it('summarises', () => {
        expect(progressSummary(j)).toBe('1 of 2 pods evicted');
        expect(progressSummary(job({ type: 'helm.rollback', progress: [{ at: 1, message: 'rolling back' }] }))).toBe('rolling back');
    });
});

describe('groupJobs', () => {
    it('running first, finished newest first, old hidden', () => {
        const now = 10_000_000;
        const out = groupJobs(
            [
                job({ jobId: 'old', state: 'done', startedAt: 0, endedAt: now - 4_000_000 }),
                job({ jobId: 'f1', state: 'done', startedAt: 1, endedAt: now - 2000 }),
                job({ jobId: 'f2', state: 'failed', startedAt: 1, endedAt: now - 1000 }),
                job({ jobId: 'r', startedAt: now - 5000 })
            ],
            now
        );
        expect(out.map(j => j.jobId)).toEqual(['r', 'f2', 'f1']);
    });
});

describe('drain', () => {
    it('params default timeout and omit empty grace', () => {
        expect(drainParams('c', 'n', { ignoreDaemonSets: true }).options).toEqual({
            ignoreDaemonSets: true,
            deleteEmptyDirData: false,
            force: false,
            timeoutSeconds: 300,
            disableEviction: false
        });
        expect(drainParams('c', 'n', { gracePeriodSeconds: 0, timeoutSeconds: 60 }).options).toMatchObject({
            gracePeriodSeconds: 0,
            timeoutSeconds: 60
        });
    });
    it('preflight blockers follow kubectl', () => {
        const r = pre({
            evict: [pod('a')],
            unmanaged: [pod('dbg')],
            emptyDir: [pod('e', { emptyDir: true })],
            blocked: [pod('b')],
            daemonSets: [pod('d')]
        });
        const off = preflightSummary(r, { ignoreDaemonSets: true });
        expect(off.blockers.map(b => b.option)).toEqual(['force', 'deleteEmptyDirData']);
        expect(off.warnings[0]).toContain('retry every 5 s until the timeout');
        expect(off.affected).toBe(2);
        const on = preflightSummary(r, { ignoreDaemonSets: true, force: true, deleteEmptyDirData: true });
        expect(on.blockers).toEqual([]);
        expect(on.affected).toBe(3);
        expect(preflightSummary(r, { ignoreDaemonSets: false, force: true, deleteEmptyDirData: true }).blockers.map(b => b.option)).toEqual([
            'ignoreDaemonSets'
        ]);
        expect(preflightSummary(r, { disableEviction: true }).warnings.join()).toContain('NOT honoured');
    });
});
