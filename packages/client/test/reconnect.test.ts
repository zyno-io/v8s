import { describe, expect, it } from 'vitest';

import type { MainToWorker, WorkerToMain } from '../src/types.ts';
import type { CoreIO } from '../src/worker/core.ts';

import { decodeFrame, encodeFrame, encodeIds, KindDict, KindHello, KindPatch, KindRequest, KindResponse } from '../src/codec.ts';
import { backoffMs, DataCore, RECONNECT_ATTEMPT_MS, RECONNECT_MAX_MS, RESYNC_EMPTY_MS } from '../src/worker/core.ts';

const QUERY = { clusters: [], gvr: 'v1/pods', namespaces: [], text: '', filters: [], sort: [] };
const QUERY2 = { ...QUERY, text: 'api' };
const COLUMNS = [
    { key: 'cluster', type: 'dict' },
    { key: 'name', type: 'str' }
];
const HELLO = { version: 1, agentVersion: 't', mode: 'fake' };

function harness(opts: { token?: string; sessionEnded?: () => Promise<boolean> } = {}) {
    const sent: ReturnType<typeof decodeFrame>[] = [];
    const posted: WorkerToMain[] = [];
    const timers = new Map<number, { fn: () => void; at: number }>();
    const connects: { url: string; token: string }[] = [];
    let closeSocketCalls = 0;
    let t = 1000;
    let tid = 0;
    const io: CoreIO = {
        send: f => sent.push(decodeFrame(f)),
        post: msg => posted.push(msg),
        connect: (url, token) => connects.push({ url, token }),
        closeSocket: () => closeSocketCalls++,
        connectStream: () => {},
        sendStream: () => {},
        closeStream: () => {},
        schedule: (fn, ms) => {
            timers.set(++tid, { fn, at: t + ms });
            return tid;
        },
        cancel: h => timers.delete(h as number),
        now: () => t,
        random: () => 0, // jitter floor: 75 % of the base
        sessionEnded: opts.sessionEnded
    };
    const core = new DataCore(io);
    const api = {
        core,
        sent,
        posted,
        connects,
        timers,
        get closeSocketCalls() {
            return closeSocketCalls;
        },
        advance(ms: number) {
            t += ms;
            for (const [id, tm] of Array.from(timers)) {
                if (tm.at <= t && timers.delete(id)) tm.fn();
            }
        },
        frame(kind: number, channel: number, seq: number, payload?: unknown) {
            core.handleFrame(encodeFrame(kind, channel, seq, payload));
        },
        main(m: MainToWorker) {
            core.handleMain(m);
        },
        statuses: () => posted.filter((m): m is Extract<WorkerToMain, { t: 'status' }> => m.t === 'status').map(m => m.status),
        of<T extends WorkerToMain['t']>(type: T) {
            return posted.filter((m): m is Extract<WorkerToMain, { t: T }> => m.t === type);
        },
        requests: () => sent.filter(s => s.kind === KindRequest),
        /** connect + Hello. */
        open() {
            api.main({ t: 'connect', url: 'ws://h/ws', token: opts.token ?? 'tok' });
            api.frame(KindHello, 0, 1, HELLO);
        },
        /** subscribe + response with columns. */
        subscribe(qid: number, gen = 1) {
            api.main({ t: 'subscribe', qid, gen, query: QUERY, window: { offset: 0, limit: 50 } });
            const req = api.requests().at(-1)!;
            api.frame(KindResponse, req.channel, 2, { result: { columns: COLUMNS } });
        },
        /** Drops the socket and waits out the first retry; the new socket then says Hello. */
        dropAndReconnect() {
            api.core.handleClosed('gone');
            api.advance(RECONNECT_MAX_MS);
            api.frame(KindHello, 0, 1, HELLO);
        }
    };
    return api;
}

describe('backoffMs', () => {
    it('doubles from 0.5 s to a 15 s cap, jittered down by at most a quarter', () => {
        expect([0, 1, 2, 3, 4, 5, 6, 20].map(a => backoffMs(a, 1))).toEqual([500, 1000, 2000, 4000, 8000, 15000, 15000, 15000]);
        expect(backoffMs(0, 0)).toBe(375);
        expect(backoffMs(3, 0.5)).toBeGreaterThanOrEqual(3000);
        expect(backoffMs(3, 0.5)).toBeLessThanOrEqual(4000);
        for (let a = 0; a < 30; a++) expect(backoffMs(a, 0.999)).toBeLessThanOrEqual(RECONNECT_MAX_MS);
    });
});

describe('DataCore reconnect', () => {
    it('retries after a close with growing delays and reports reconnecting', () => {
        const h = harness();
        h.open();
        h.core.handleClosed('lost');
        expect(h.statuses().at(-1)).toBe('reconnecting');
        expect(h.connects).toHaveLength(1);
        h.advance(374);
        expect(h.connects).toHaveLength(1);
        h.advance(1);
        expect(h.connects).toHaveLength(2); // attempt 0: 500 * 0.75
        h.core.handleClosed('still down');
        h.advance(749);
        expect(h.connects).toHaveLength(2);
        h.advance(1);
        expect(h.connects).toHaveLength(3); // attempt 1: 1000 * 0.75
        h.core.handleClosed('still down');
        h.advance(1499);
        expect(h.connects).toHaveLength(3);
        h.advance(1);
        expect(h.connects).toHaveLength(4); // attempt 2: 2000 * 0.75
        expect(h.statuses().filter(s => s === 'closed')).toHaveLength(0);
    });

    it('resets the backoff after a successful handshake', () => {
        const h = harness();
        h.open();
        h.core.handleClosed('x');
        h.advance(375);
        h.core.handleClosed('x');
        h.advance(750);
        h.frame(KindHello, 0, 1, HELLO);
        expect(h.statuses().at(-1)).toBe('open');
        h.core.handleClosed('x');
        h.advance(375);
        expect(h.connects).toHaveLength(4); // first retry delay again
    });

    it('does not reconnect when the first connect fails (the page reports a boot error)', () => {
        const h = harness();
        h.main({ t: 'connect', url: 'ws://h/ws', token: 'tok' });
        h.core.handleClosed('refused');
        expect(h.statuses().at(-1)).toBe('closed');
        h.advance(60_000);
        expect(h.connects).toHaveLength(1);
    });

    it('does not reconnect after a deliberate close', () => {
        const h = harness();
        h.open();
        h.main({ t: 'close' });
        expect(h.closeSocketCalls).toBe(1);
        h.core.handleClosed('closed (1005)');
        expect(h.statuses().at(-1)).toBe('closed');
        h.advance(60_000);
        expect(h.connects).toHaveLength(1);
    });

    it('a close while waiting for a retry cancels it and reports closed', () => {
        const h = harness();
        h.open();
        h.core.handleClosed('lost');
        h.main({ t: 'close' });
        expect(h.statuses().at(-1)).toBe('closed');
        h.advance(60_000);
        expect(h.connects).toHaveLength(1);
    });

    it('does not reconnect after a protocol version mismatch', () => {
        const h = harness();
        h.main({ t: 'connect', url: 'ws://h/ws', token: 'tok' });
        h.frame(KindHello, 0, 1, { version: 99, agentVersion: 'x', mode: 'fake' });
        h.core.handleClosed('mismatch');
        h.advance(60_000);
        expect(h.connects).toHaveLength(1);
        expect(h.statuses().at(-1)).toBe('closed');
    });

    it('a nudge reconnects at once, and only once while an attempt is in flight', () => {
        const h = harness();
        h.open();
        h.core.handleClosed('lost');
        h.main({ t: 'nudge' });
        expect(h.connects).toHaveLength(2);
        h.main({ t: 'nudge' });
        expect(h.connects).toHaveLength(2);
        h.core.handleClosed('refused');
        h.advance(375);
        expect(h.connects).toHaveLength(2); // the pending retry was cancelled by the nudge: next wait is the next backoff step
        h.advance(375);
        expect(h.connects).toHaveLength(3);
    });

    it('a nudge while open does nothing', () => {
        const h = harness();
        h.open();
        h.main({ t: 'nudge' });
        expect(h.connects).toHaveLength(1);
    });

    it('abandons an attempt that never completes its handshake', () => {
        const h = harness();
        h.open();
        h.core.handleClosed('lost');
        h.advance(375);
        expect(h.closeSocketCalls).toBe(0);
        h.advance(RECONNECT_ATTEMPT_MS);
        expect(h.closeSocketCalls).toBe(1);
    });

    it('stops reconnecting when a hosted session has ended (401)', async () => {
        const h = harness({ token: '', sessionEnded: async () => true });
        h.open();
        h.core.handleClosed('lost');
        await Promise.resolve();
        await Promise.resolve();
        expect(h.statuses().at(-1)).toBe('closed');
        expect(h.of('status').at(-1)?.message).toBe('session ended');
        h.advance(60_000);
        expect(h.connects).toHaveLength(1);
    });

    it('keeps reconnecting hosted when the session is fine', async () => {
        const h = harness({ token: '', sessionEnded: async () => false });
        h.open();
        h.core.handleClosed('lost');
        await Promise.resolve();
        await Promise.resolve();
        expect(h.statuses().at(-1)).toBe('reconnecting');
        h.advance(375);
        expect(h.connects).toHaveLength(2);
    });

    it('never asks about the session in token mode', () => {
        let asked = 0;
        const h = harness({
            token: 'tok',
            sessionEnded: async () => {
                asked++;
                return true;
            }
        });
        h.open();
        h.core.handleClosed('lost');
        expect(asked).toBe(0);
    });

    it('fails in-flight requests as unavailable, and requests made while down answer at once instead of queueing', () => {
        const h = harness();
        h.open();
        h.main({ t: 'request', rid: 5, method: 'kinds.list' });
        h.core.handleClosed('lost');
        expect(h.of('response')[0]).toMatchObject({ rid: 5, error: { code: 'unavailable' } });
        h.main({ t: 'request', rid: 6, method: 'settings.set' });
        expect(h.of('response')[1]).toMatchObject({ rid: 6, error: { code: 'unavailable' } });
        h.main({ t: 'log.open', sid: 3, params: { type: 'logs' } as never, capacity: 10, multi: false });
        expect(h.of('log.error')[0]).toMatchObject({ sid: 3, error: { code: 'unavailable' } });
        const before = h.sent.length;
        h.advance(375);
        h.frame(KindHello, 0, 1, HELLO);
        expect(h.sent.slice(before).filter(s => (s.payload as { method: string }).method === 'settings.set')).toHaveLength(0);
    });
});

describe('DataCore resubscribe', () => {
    it('resubscribes every view with the same qid and gen, and the latest query and window', () => {
        const h = harness();
        h.open();
        h.subscribe(1);
        h.subscribe(2);
        h.main({ t: 'update', qid: 2, gen: 2, query: QUERY2, window: { offset: 10, limit: 20 } });
        h.main({ t: 'update', qid: 1, gen: 1, window: { offset: 5, limit: 30 } });
        const before = h.sent.length;
        h.dropAndReconnect();
        const subs = h.sent.slice(before).filter(s => (s.payload as { method: string }).method === 'view.subscribe');
        expect(subs.map(s => s.payload)).toEqual([
            { method: 'view.subscribe', params: { qid: 1, gen: 1, query: QUERY, window: { offset: 5, limit: 30 } } },
            { method: 'view.subscribe', params: { qid: 2, gen: 2, query: QUERY2, window: { offset: 10, limit: 20 } } }
        ]);
        // Each uses a fresh worker-side request id (no collision with the earlier ones).
        expect(new Set(subs.map(s => s.channel)).size).toBe(2);
    });

    it('accepts a fresh full snapshot with new wire ids and a new dictionary, dropping the old row table', () => {
        const h = harness();
        h.open();
        h.frame(KindDict, 0, 2, { base: 1, strings: ['c1'] });
        h.subscribe(1);
        h.frame(KindPatch, 1, 3, {
            gen: 1,
            total: 2,
            offset: 0,
            rows: [
                [1, 1, 1, 'old-a'],
                [2, 1, 1, 'old-b']
            ],
            ids: encodeIds([1, 2])
        });
        h.advance(20);
        expect(h.core.rowCount(1)).toBe(2);

        h.core.handleClosed('lost');
        h.advance(375);
        h.frame(KindHello, 0, 1, HELLO);
        expect(h.core.rowCount(1)).toBe(0); // reset until the new snapshot arrives
        const req = h.requests().at(-1)!;
        h.frame(KindResponse, req.channel, 2, { result: { columns: COLUMNS } });
        // New connection: the dictionary restarts, wire ids restart (id 1 is now a different object).
        h.frame(KindDict, 0, 3, { base: 1, strings: ['c2'] });
        h.frame(KindPatch, 1, 4, { gen: 1, total: 1, offset: 0, rows: [[1, 1, 1, 'new-a']], ids: encodeIds([1]) });
        h.advance(20);
        expect(h.core.rowCount(1)).toBe(1);
        const rows = h.of('rows').at(-1)!;
        expect(rows.rows).toEqual([[1, 1, 'c2', 'new-a']]);
        const commit = h.of('commit').at(-1)!;
        expect(commit.ids && [...commit.ids]).toEqual([1]);
    });

    it('buffers patches that beat the resubscribe response', () => {
        const h = harness();
        h.open();
        h.subscribe(1);
        h.core.handleClosed('lost');
        h.advance(375);
        h.frame(KindHello, 0, 1, HELLO);
        const req = h.requests().at(-1)!;
        h.frame(KindPatch, 1, 2, { gen: 1, total: 1, offset: 0, rows: [[1, 1, 0, 'x']], ids: encodeIds([1]) });
        expect(h.core.rowCount(1)).toBe(0);
        h.frame(KindResponse, req.channel, 2, { result: { columns: COLUMNS } });
        expect(h.core.rowCount(1)).toBe(1);
    });

    it('acks patches of the new connection by their own seq', () => {
        const h = harness();
        h.open();
        h.subscribe(1);
        h.dropAndReconnect();
        const req = h.requests().at(-1)!;
        h.frame(KindResponse, req.channel, 2, { result: { columns: COLUMNS } });
        h.frame(KindPatch, 1, 2, { gen: 1, total: 0, offset: 0 });
        expect(h.sent.at(-1)).toMatchObject({ kind: 5, seq: 2 });
    });

    it('does not resubscribe a view that was closed, and picks up one opened while down', () => {
        const h = harness();
        h.open();
        h.subscribe(1);
        h.core.handleClosed('lost');
        h.main({ t: 'unsubscribe', qid: 1 });
        h.main({ t: 'subscribe', qid: 2, gen: 1, query: QUERY2, window: { offset: 0, limit: 9 } });
        const before = h.sent.length;
        h.advance(375);
        h.frame(KindHello, 0, 1, HELLO);
        const methods = h.sent.slice(before).map(s => s.payload as { method: string; params: { qid: number } });
        expect(methods.map(m => [m.method, m.params.qid])).toEqual([['view.subscribe', 2]]);
    });

    it('clears a view that is empty after the reconnect once no patch arrives', () => {
        const h = harness();
        h.open();
        h.subscribe(1);
        h.frame(KindPatch, 1, 2, { gen: 1, total: 1, offset: 0, rows: [[1, 1, 0, 'a']], ids: encodeIds([1]) });
        h.advance(20);
        h.core.handleClosed('lost');
        h.advance(375);
        h.frame(KindHello, 0, 1, HELLO);
        const req = h.requests().at(-1)!;
        h.frame(KindResponse, req.channel, 2, { result: { columns: COLUMNS } });
        const commits = h.of('commit').length;
        h.advance(RESYNC_EMPTY_MS + 20);
        h.advance(20); // the flush the resync scheduled
        const c = h.of('commit');
        expect(c.length).toBe(commits + 1);
        expect(c.at(-1)).toMatchObject({ total: 0 });
        expect([...c.at(-1)!.ids!]).toEqual([]);
    });

    it('does not flash an empty table while the views wait for their snapshot', () => {
        const h = harness();
        h.open();
        h.subscribe(1);
        const commits = h.of('commit').length;
        const rows = h.of('rows').length;
        h.dropAndReconnect();
        const req = h.requests().at(-1)!;
        h.frame(KindResponse, req.channel, 2, { result: { columns: COLUMNS } });
        h.advance(500);
        expect(h.of('commit').length).toBe(commits);
        expect(h.of('rows').length).toBe(rows);
    });
});
