import { describe, expect, it } from 'vitest';

import type { MainToWorker, WorkerToMain } from '../src/types.ts';
import type { CoreIO } from '../src/worker/core.ts';

import {
    decodeFrame,
    encodeFrame,
    encodeIds,
    KindAck,
    KindClusterState,
    KindDict,
    KindHello,
    KindObject,
    KindPatch,
    KindRequest,
    KindResponse
} from '../src/codec.ts';
import { CHUNK_ROWS, DataCore, FLUSH_MS } from '../src/worker/core.ts';

const QUERY = { clusters: [], gvr: 'v1/pods', namespaces: [], text: '', filters: [], sort: [] };
const COLUMNS = [
    { key: 'cluster', type: 'dict' },
    { key: 'name', type: 'str' },
    { key: 'status', type: 'dict' },
    { key: 'restarts', type: 'int' }
];

function harness() {
    const sent: ReturnType<typeof decodeFrame>[] = [];
    const posted: { msg: WorkerToMain; transfer?: Transferable[] }[] = [];
    const timers = new Map<number, { fn: () => void; at: number }>();
    let t = 1000;
    let tid = 0;
    const io: CoreIO = {
        send: f => sent.push(decodeFrame(f)),
        post: (msg, transfer) => posted.push({ msg, transfer }),
        connect: () => {},
        closeSocket: () => {},
        connectStream: () => {},
        sendStream: () => {},
        closeStream: () => {},
        schedule: (fn, ms) => {
            timers.set(++tid, { fn, at: t + ms });
            return tid;
        },
        cancel: h => timers.delete(h as number),
        now: () => t
    };
    const core = new DataCore(io);
    const api = {
        core,
        sent,
        posted,
        timers,
        advance(ms: number) {
            t += ms;
            for (const [id, tm] of timers) {
                if (tm.at <= t) {
                    timers.delete(id);
                    tm.fn();
                }
            }
        },
        frame(kind: number, channel: number, seq: number, payload?: unknown) {
            core.handleFrame(encodeFrame(kind, channel, seq, payload));
        },
        main(m: MainToWorker) {
            core.handleMain(m);
        },
        of<T extends WorkerToMain['t']>(type: T) {
            return posted.map(p => p.msg).filter((m): m is Extract<WorkerToMain, { t: T }> => m.t === type);
        },
        /** Hello + subscribe + response with the test columns. Returns qid. */
        subscribed(qid = 1, gen = 1) {
            api.frame(KindHello, 0, 1, { version: 1, agentVersion: 'test', mode: 'fake' });
            api.main({ t: 'subscribe', qid, gen, query: QUERY, window: { offset: 0, limit: 100 } });
            const req = sent.find(s => s.kind === KindRequest)!;
            api.frame(KindResponse, req.channel, 2, { result: { columns: COLUMNS } });
            return qid;
        }
    };
    return api;
}

const patch = (over: Record<string, unknown> = {}) => ({ gen: 1, total: 0, offset: 0, ...over });

describe('DataCore handshake', () => {
    it('reports open on a matching Hello and flushes queued requests', () => {
        const h = harness();
        h.main({ t: 'request', rid: 7, method: 'clusters.list' });
        expect(h.sent).toHaveLength(0);
        h.frame(KindHello, 0, 1, { version: 1, agentVersion: '0.1', mode: 'fake' });
        expect(h.of('status').at(-1)).toMatchObject({ status: 'open', agentVersion: '0.1', mode: 'fake' });
        expect(h.sent[0]).toMatchObject({ kind: KindRequest, channel: 7, payload: { method: 'clusters.list' } });
    });

    it('errors on a version mismatch', () => {
        const h = harness();
        h.frame(KindHello, 0, 1, { version: 2, agentVersion: '9', mode: 'fake' });
        expect(h.of('status').at(-1)?.status).toBe('error');
    });

    it('routes responses by channel id and cluster states through', () => {
        const h = harness();
        h.frame(KindHello, 0, 1, { version: 1, agentVersion: '0.1', mode: 'fake' });
        h.main({ t: 'request', rid: 3, method: 'kinds.list' });
        h.frame(KindResponse, 3, 2, { result: { kinds: [] } });
        expect(h.of('response')[0]).toMatchObject({ rid: 3, result: { kinds: [] } });
        h.frame(KindClusterState, 0, 3, { states: [{ id: 'a', phase: 'ready', synced: 5 }] });
        expect(h.of('clusterStates')[0].states[0].id).toBe('a');
    });
});

describe('DataCore patches', () => {
    it('resolves dictionary cells and applies ids/rows', () => {
        const h = harness();
        const qid = h.subscribed();
        h.frame(KindDict, 0, 3, { base: 1, strings: ['prod-us', 'Running', 'CrashLoopBackOff'] });
        h.frame(KindPatch, qid, 4, {
            gen: 1,
            total: 2,
            offset: 0,
            ids: encodeIds([10, 11]),
            rows: [
                [10, 1, 1, 'api-0', 2, 0],
                [11, 1, 1, 'api-1', 3, 4]
            ]
        });
        h.advance(FLUSH_MS);
        const rows = h.of('rows')[0];
        expect(rows.rows).toEqual([
            [10, 1, 'prod-us', 'api-0', 'Running', 0],
            [11, 1, 'prod-us', 'api-1', 'CrashLoopBackOff', 4]
        ]);
        const commit = h.of('commit')[0];
        expect([...commit.ids!]).toEqual([10, 11]);
        expect(commit).toMatchObject({ total: 2, offset: 0, gen: 1 });
    });

    it('treats dict id 0 as empty and unknown ids as empty', () => {
        const h = harness();
        const qid = h.subscribed();
        h.frame(KindPatch, qid, 3, patch({ total: 1, rows: [[1, 1, 0, 'x', 99, 0]] }));
        h.advance(FLUSH_MS);
        expect(h.of('rows')[0].rows[0]).toEqual([1, 1, '', 'x', '', 0]);
    });

    it('applies removed ids and updated versions', () => {
        const h = harness();
        const qid = h.subscribed();
        h.frame(KindDict, 0, 3, { base: 1, strings: ['c', 's'] });
        h.frame(
            KindPatch,
            qid,
            4,
            patch({
                total: 2,
                ids: encodeIds([1, 2]),
                rows: [
                    [1, 1, 1, 'a', 2, 0],
                    [2, 1, 1, 'b', 2, 0]
                ]
            })
        );
        h.advance(FLUSH_MS);
        expect(h.core.rowCount(qid)).toBe(2);
        h.frame(KindPatch, qid, 5, patch({ total: 1, ids: encodeIds([2]), rows: [[2, 2, 1, 'b', 2, 5]], removed: [1] }));
        h.advance(FLUSH_MS);
        expect(h.core.rowCount(qid)).toBe(1);
        const last = h.of('commit').at(-1)!;
        expect(last.removed).toEqual([1]);
        expect([...last.ids!]).toEqual([2]);
        expect(h.of('rows').at(-1)!.rows[0][1]).toBe(2);
    });

    it('prunes rows missing from a new ids list even without removed', () => {
        const h = harness();
        const qid = h.subscribed();
        h.frame(
            KindPatch,
            qid,
            4,
            patch({
                total: 3,
                ids: encodeIds([1, 2, 3]),
                rows: [
                    [1, 1, 0, 'a', 0, 0],
                    [2, 1, 0, 'b', 0, 0],
                    [3, 1, 0, 'c', 0, 0]
                ]
            })
        );
        h.advance(FLUSH_MS);
        expect(h.core.rowCount(qid)).toBe(3);
        h.frame(KindPatch, qid, 5, patch({ total: 3, ids: encodeIds([3, 4]), rows: [[4, 1, 0, 'd', 0, 0]] }));
        h.advance(FLUSH_MS);
        expect(h.core.rowCount(qid)).toBe(2);
    });

    it('omits ids when membership did not change', () => {
        const h = harness();
        const qid = h.subscribed();
        h.frame(KindPatch, qid, 3, patch({ total: 1, rows: [[1, 1, 0, 'a', 0, 0]] }));
        h.advance(FLUSH_MS);
        expect(h.of('commit')[0].ids).toBeUndefined();
        h.frame(KindPatch, qid, 4, patch({ total: 1, rows: [[1, 2, 0, 'a', 0, 1]] }));
        h.advance(FLUSH_MS);
        expect(h.of('commit').at(-1)!.ids).toBeUndefined();
    });

    it('acks every patch frame after applying, including dropped ones', () => {
        const h = harness();
        const qid = h.subscribed();
        h.main({ t: 'update', qid, gen: 2, query: QUERY });
        h.frame(KindPatch, qid, 40, patch({ gen: 1, total: 5 })); // stale
        h.frame(KindPatch, qid, 41, patch({ gen: 2, total: 6 }));
        h.frame(KindPatch, 999, 42, patch({ gen: 1 })); // unknown qid
        const acks = h.sent.filter(s => s.kind === KindAck);
        expect(acks.map(a => a.seq)).toEqual([40, 41, 42]);
        expect(acks.every(a => a.channel === 0)).toBe(true);
    });
});

describe('DataCore generations', () => {
    it('drops patches older than the current gen and keeps newer ones', () => {
        const h = harness();
        const qid = h.subscribed();
        h.main({ t: 'update', qid, gen: 2, query: { ...QUERY, text: 'x' } });
        h.frame(KindPatch, qid, 10, patch({ gen: 1, total: 100, ids: encodeIds([1]), rows: [[1, 1, 0, 'old', 0, 0]] }));
        h.advance(FLUSH_MS);
        expect(h.core.rowCount(qid)).toBe(0);
        expect(h.of('commit')).toHaveLength(0);
        h.frame(KindPatch, qid, 11, patch({ gen: 2, total: 1, ids: encodeIds([2]), rows: [[2, 1, 0, 'new', 0, 0]] }));
        h.advance(FLUSH_MS);
        expect(h.of('commit')[0]).toMatchObject({ gen: 2, total: 1 });
    });

    it('discards pending old-gen ids when the query changes before the flush', () => {
        const h = harness();
        const qid = h.subscribed();
        h.frame(KindPatch, qid, 3, patch({ gen: 1, total: 5, ids: encodeIds([1, 2, 3]), rows: [[1, 1, 0, 'a', 0, 0]] }));
        h.main({ t: 'update', qid, gen: 2, query: { ...QUERY, text: 'q' } });
        h.advance(FLUSH_MS);
        expect(h.of('commit')[0].ids).toBeUndefined();
    });

    it('sends view.update with the new gen and query', () => {
        const h = harness();
        const qid = h.subscribed();
        h.main({ t: 'update', qid, gen: 2, query: { ...QUERY, text: 'q' }, window: { offset: 5, limit: 50 } });
        const req = h.sent.filter(s => s.kind === KindRequest).at(-1)!;
        expect(req.payload).toMatchObject({ method: 'view.update', params: { qid, gen: 2, query: { text: 'q' }, window: { offset: 5, limit: 50 } } });
    });
});

describe('DataCore posting', () => {
    it('coalesces patches into one flush per interval', () => {
        const h = harness();
        const qid = h.subscribed();
        for (let i = 1; i <= 5; i++) h.frame(KindPatch, qid, 10 + i, patch({ total: i, rows: [[i, 1, 0, 'r' + i, 0, 0]] }));
        expect(h.of('commit')).toHaveLength(0);
        h.advance(FLUSH_MS);
        expect(h.of('commit')).toHaveLength(1);
        expect(h.of('rows').flatMap(r => r.rows)).toHaveLength(5);
        // A patch right after a flush waits out the remainder of the interval.
        h.advance(4);
        h.frame(KindPatch, qid, 20, patch({ total: 6 }));
        h.advance(FLUSH_MS - 5);
        expect(h.of('commit')).toHaveLength(1);
        h.advance(1);
        expect(h.of('commit')).toHaveLength(2);
    });

    it('chunks rows to at most 500 per message and transfers the ids buffer', () => {
        const h = harness();
        const qid = h.subscribed();
        const n = CHUNK_ROWS * 2 + 100;
        const ids = Array.from({ length: n }, (_, i) => i + 1);
        h.frame(KindPatch, qid, 3, patch({ total: n, ids: encodeIds(ids), rows: ids.map(id => [id, 1, 0, 'n' + id, 0, 0]) }));
        h.advance(FLUSH_MS);
        const sizes = h.of('rows').map(r => r.rows.length);
        expect(sizes).toEqual([500, 500, 100]);
        const idx = h.posted.findIndex(p => p.msg.t === 'commit');
        expect(h.posted.slice(idx - 3, idx).every(p => p.msg.t === 'rows')).toBe(true); // commit closes the flush
        expect(idx).toBe(h.posted.length - 1);
        const commit = h.posted[idx];
        expect(commit.transfer).toHaveLength(1);
        expect((commit.msg as Extract<WorkerToMain, { t: 'commit' }>).ids).toBeInstanceOf(Uint32Array);
    });

    it('holds patches that arrive before the subscribe response, then applies them', () => {
        const h = harness();
        h.frame(KindHello, 0, 1, { version: 1, agentVersion: 't', mode: 'fake' });
        h.main({ t: 'subscribe', qid: 1, gen: 1, query: QUERY, window: { offset: 0, limit: 10 } });
        const req = h.sent.find(s => s.kind === KindRequest)!;
        h.frame(KindDict, 0, 2, { base: 1, strings: ['c'] });
        h.frame(KindPatch, 1, 3, patch({ total: 1, ids: encodeIds([1]), rows: [[1, 1, 1, 'a', 0, 0]] }));
        h.advance(FLUSH_MS);
        expect(h.of('rows')).toHaveLength(0);
        h.frame(KindResponse, req.channel, 4, { result: { columns: COLUMNS } });
        h.advance(FLUSH_MS);
        expect(h.of('rows')[0].rows[0]).toEqual([1, 1, 'c', 'a', '', 0]);
    });

    it('posts empty columns with the error when the subscribe fails', () => {
        const h = harness();
        h.frame(KindHello, 0, 1, { version: 1, agentVersion: 't', mode: 'fake' });
        h.main({ t: 'subscribe', qid: 1, gen: 1, query: QUERY, window: { offset: 0, limit: 10 } });
        const req = h.sent.find(s => s.kind === KindRequest)!;
        h.frame(KindResponse, req.channel, 2, { error: { code: 'forbidden', message: 'no permission' } });
        expect(h.of('columns')).toEqual([{ t: 'columns', qid: 1, columns: [], error: { code: 'forbidden', message: 'no permission' } }]);
    });

    it('fails outstanding requests when the socket closes', () => {
        const h = harness();
        h.frame(KindHello, 0, 1, { version: 1, agentVersion: 't', mode: 'fake' });
        h.main({ t: 'request', rid: 1, method: 'clusters.list' });
        h.core.handleClosed('bye');
        expect(h.of('response')[0].error?.code).toBe('unavailable');
        expect(h.of('status').at(-1)?.status).toBe('closed');
    });
});

describe('DataCore objects and errors', () => {
    it('forwards KindObject frames by oid without acking them', () => {
        const h = harness();
        h.frame(KindHello, 0, 1, { version: 1, agentVersion: 't', mode: 'fake' });
        h.frame(KindObject, 9, 2, { yaml: 'a: 1\n', uid: 'u', resourceVersion: '5', managedBy: [], owners: [] });
        h.frame(KindObject, 9, 3, { deleted: true, yaml: '', uid: 'u', resourceVersion: '6', managedBy: [], owners: [] });
        const frames = h.of('object');
        expect(frames).toHaveLength(2);
        expect(frames[0]).toMatchObject({ oid: 9, frame: { resourceVersion: '5', yaml: 'a: 1\n' } });
        expect(frames[1].frame.deleted).toBe(true);
        expect(h.sent.filter(f => f.kind === KindAck)).toHaveLength(0);
    });

    it('passes a conflict error with the live object through', () => {
        const h = harness();
        h.frame(KindHello, 0, 1, { version: 1, agentVersion: 't', mode: 'fake' });
        h.main({ t: 'request', rid: 4, method: 'object.apply', params: {} });
        h.frame(KindResponse, 4, 2, {
            error: { code: 'conflict', message: 'changed', live: { yaml: 'x', uid: 'u', resourceVersion: '9', managedBy: [], owners: [] } }
        });
        expect(h.of('response')[0].error).toMatchObject({ code: 'conflict', live: { resourceVersion: '9' } });
    });
});
