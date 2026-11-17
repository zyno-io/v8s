import { describe, expect, it } from 'vitest';

import type { WorkerLike } from '../src/agent.ts';
import type { MainToWorker, Row, WorkerToMain } from '../src/types.ts';

import { connectAgent } from '../src/agent.ts';

const Q = { clusters: [], gvr: 'v1/pods', namespaces: [], text: '', filters: [], sort: [] };

function fakeWorker() {
    const sent: MainToWorker[] = [];
    const w: WorkerLike = {
        onmessage: null,
        postMessage: m => void sent.push(m),
        terminate: () => {}
    };
    const emit = (m: WorkerToMain) => w.onmessage?.({ data: m } as MessageEvent<WorkerToMain>);
    return { w, sent, emit };
}

async function open() {
    const f = fakeWorker();
    const p = connectAgent({ url: 'ws://x/ws', token: 't', workerFactory: () => f.w });
    f.emit({ t: 'status', status: 'open', agentVersion: '1', mode: 'fake' });
    return { agent: await p, ...f };
}

describe('connectAgent', () => {
    it('connects with the endpoint and resolves on open', async () => {
        const { sent, agent } = await open();
        expect(sent[0]).toEqual({ t: 'connect', url: 'ws://x/ws', token: 't' });
        expect(agent.agentInfo.value).toEqual({ version: '1', mode: 'fake' });
    });

    it('correlates request/response and rejects on error', async () => {
        const { agent, sent, emit } = await open();
        const ok = agent.listClusters();
        const req = sent.find(m => m.t === 'request') as Extract<MainToWorker, { t: 'request' }>;
        expect(req.method).toBe('clusters.list');
        emit({ t: 'response', rid: req.rid, result: { clusters: [], selected: ['a'] } });
        expect(await ok).toEqual({ clusters: [], selected: ['a'] });

        const bad = agent.listKinds();
        const req2 = sent.filter(m => m.t === 'request').at(-1) as Extract<MainToWorker, { t: 'request' }>;
        emit({ t: 'response', rid: req2.rid, error: { code: 'forbidden', message: 'no' } });
        await expect(bad).rejects.toThrow('no');
    });

    it('applies rows without a version bump, then commits ids/total/offset/removed', async () => {
        const { agent, emit } = await open();
        const h = agent.subscribeView(Q, { offset: 0, limit: 10 });
        const r1 = [1, 1, 'c', 'a'] as unknown as Row;
        const r2 = [2, 1, 'c', 'b'] as unknown as Row;
        emit({ t: 'rows', qid: h.qid, gen: 1, rows: [r1, r2] });
        expect(h.rows.size).toBe(2);
        expect(h.version.value).toBe(0);
        emit({ t: 'commit', qid: h.qid, gen: 1, total: 2, offset: 5, ids: new Uint32Array([1, 2]) });
        expect(h.version.value).toBe(1);
        expect([h.total.value, h.offset.value, [...h.ids]]).toEqual([2, 5, [1, 2]]);
        emit({ t: 'commit', qid: h.qid, gen: 1, total: 1, offset: 5, removed: [2] });
        expect(h.rows.has(2)).toBe(false);
        expect([...h.ids]).toEqual([1, 2]); // ids unchanged when the patch carries none
    });

    it('prunes main-thread rows not in a new ids list even without removed', async () => {
        const { agent, emit } = await open();
        const h = agent.subscribeView(Q, { offset: 0, limit: 10 });
        const row = (id: number) => [id, 1, 'c', 'n'] as unknown as Row;
        emit({ t: 'rows', qid: h.qid, gen: 1, rows: [row(1), row(2), row(3)] });
        emit({ t: 'commit', qid: h.qid, gen: 1, total: 3, offset: 0, ids: new Uint32Array([1, 2, 3]) });
        emit({ t: 'rows', qid: h.qid, gen: 1, rows: [row(4)] });
        emit({ t: 'commit', qid: h.qid, gen: 1, total: 2, offset: 0, ids: new Uint32Array([3, 4]) });
        expect([...h.rows.keys()].sort()).toEqual([3, 4]);
    });

    it('bumps gen on a query change only, and drops older-gen messages', async () => {
        const { agent, sent, emit } = await open();
        const h = agent.subscribeView(Q, { offset: 0, limit: 10 });
        h.update({ window: { offset: 10, limit: 10 } });
        expect(sent.at(-1)).toMatchObject({ t: 'update', gen: 1, window: { offset: 10 } });
        h.update({ query: { ...Q, text: 'x' } });
        expect(h.gen).toBe(2);
        expect(sent.at(-1)).toMatchObject({ t: 'update', gen: 2, query: { text: 'x' } });
        emit({ t: 'commit', qid: h.qid, gen: 1, total: 99, offset: 0, ids: new Uint32Array([7]) });
        expect(h.version.value).toBe(0);
        expect(h.total.value).toBe(0);
    });

    it('close() unsubscribes once and ignores later updates', async () => {
        const { agent, sent } = await open();
        const h = agent.subscribeView(Q, { offset: 0, limit: 10 });
        h.close();
        h.close();
        h.update({ window: { offset: 0, limit: 5 } });
        expect(sent.filter(m => m.t === 'unsubscribe')).toHaveLength(1);
        expect(sent.filter(m => m.t === 'update')).toHaveLength(0);
    });

    it('keeps rows and ids out of reactive state (markRaw handle)', async () => {
        const { agent } = await open();
        const h = agent.subscribeView(Q, { offset: 0, limit: 10 });
        expect((h as unknown as { __v_skip?: boolean }).__v_skip).toBe(true);
    });
});
