import type { ClusterStates, DictAdd, Hello, JobFrame, Response, StreamOpenResult, ViewPatch, ViewSubscribeResult } from '../protocol.gen.ts';
import type { Column, MainToWorker, ObjectFrameWire, Row, WorkerToMain } from '../types.ts';

import {
    decodeFrame,
    decodeIds,
    encodeFrame,
    KindAck,
    KindClusterState,
    KindDict,
    KindHello,
    KindJob,
    KindObject,
    KindPatch,
    KindRequest,
    KindResponse
} from '../codec.ts';
import { LogSession } from '../logs/session.ts';
import { ColDict, Subprotocol, Version } from '../protocol.gen.ts';

/** Max rows per posted message: keeps one structured clone well under ~2 ms. */
export const CHUNK_ROWS = 500;
/** Minimum spacing between posts for one view. */
export const FLUSH_MS = 16;
/** Wire request ids allocated by the worker itself (subscribe/update/unsubscribe); main-thread ids stay below this. */
const INTERNAL_RID_BASE = 0x80000000;

export interface CoreIO {
    /** Writes one binary frame to the socket. */
    send(frame: Uint8Array): void;
    post(msg: WorkerToMain, transfer?: Transferable[]): void;
    connect(url: string, token: string): void;
    closeSocket(): void;
    /** Opens the `/stream` socket of a log stream; frames come back through `handleStream*`. */
    connectStream(sid: number, url: string, protocols: string[]): void;
    sendStream(sid: number, text: string): void;
    closeStream(sid: number): void;
    schedule(fn: () => void, ms: number): unknown;
    cancel(handle: unknown): void;
    now(): number;
}

interface View {
    qid: number;
    gen: number;
    columns: Column[] | null;
    dictCols: number[];
    rows: Map<number, Row>;
    dirty: Set<number>;
    removed: Set<number>;
    total: number;
    offset: number;
    ids: Uint32Array | null;
    /** Patches that arrived before the subscribe response delivered the columns. */
    early: ViewPatch[];
    timer: unknown;
    lastFlush: number;
}

type Pending =
    | { kind: 'passthrough'; rid: number }
    | { kind: 'subscribe'; qid: number }
    | { kind: 'logopen'; sid: number; capacity: number; multi: boolean }
    | { kind: 'ignore' };

/** `ws://host/ws?x` → `ws://host/stream?x`. */
export function streamUrl(dataUrl: string): string {
    return dataUrl.replace(/\/ws(\?|#|$)/, '/stream$1');
}

/**
 * The Data Worker's logic, free of `self`/WebSocket so it can be unit-tested with hand-built frames.
 * It owns per-view row maps, resolves dictionary strings, drops stale-generation patches, acks every
 * patch frame, and coalesces posts to the main thread (one per view per ~16 ms, chunked).
 */
export class DataCore {
    private dict: string[] = [];
    private views = new Map<number, View>();
    private pending = new Map<number, Pending>();
    private nextRid = INTERNAL_RID_BASE;
    private ready = false;
    private queued: MainToWorker[] = [];
    private endpoint = { url: '', token: '' };
    private logs = new Map<number, LogSession>();
    private cancelledLogs = new Set<number>();

    constructor(private io: CoreIO) {}

    // ---- main thread → worker ----

    handleMain(msg: MainToWorker): void {
        if (msg.t === 'connect') {
            this.endpoint = { url: msg.url, token: msg.token };
            this.io.post({ t: 'status', status: 'connecting' });
            this.io.connect(msg.url, msg.token);
            return;
        }
        if (msg.t === 'close') {
            this.io.closeSocket();
            return;
        }
        if (!this.ready) {
            this.queued.push(msg);
            return;
        }
        switch (msg.t) {
            case 'request':
                this.pending.set(msg.rid, { kind: 'passthrough', rid: msg.rid });
                this.sendRequest(msg.rid, msg.method, msg.params);
                break;
            case 'subscribe': {
                const view = this.newView(msg.qid, msg.gen);
                this.views.set(msg.qid, view);
                const rid = this.nextRid++;
                this.pending.set(rid, { kind: 'subscribe', qid: msg.qid });
                this.sendRequest(rid, 'view.subscribe', { qid: msg.qid, gen: msg.gen, query: msg.query, window: msg.window });
                break;
            }
            case 'update': {
                const view = this.views.get(msg.qid);
                if (!view) break;
                if (msg.gen > view.gen) {
                    view.gen = msg.gen;
                    // Anything computed for the old query must not reach the screen after the bump.
                    view.ids = null;
                    view.early.length = 0;
                }
                const rid = this.nextRid++;
                this.pending.set(rid, { kind: 'ignore' });
                this.sendRequest(rid, 'view.update', { qid: msg.qid, gen: msg.gen, query: msg.query, window: msg.window });
                break;
            }
            case 'log.open': {
                const rid = this.nextRid++;
                this.pending.set(rid, { kind: 'logopen', sid: msg.sid, capacity: msg.capacity, multi: msg.multi });
                this.sendRequest(rid, 'stream.open', { ...msg.params, type: 'logs' });
                break;
            }
            case 'log.view':
                this.logs.get(msg.sid)?.setView(msg.first, msg.count, msg.follow);
                break;
            case 'log.search':
                this.logs.get(msg.sid)?.search(msg.q, msg.matchCase);
                break;
            case 'log.step':
                this.logs.get(msg.sid)?.searchStep(msg.dir, msg.from);
                break;
            case 'log.pause':
                this.logs.get(msg.sid)?.setPaused(msg.paused);
                break;
            case 'log.clear':
                this.logs.get(msg.sid)?.clear();
                break;
            case 'log.close': {
                const s = this.logs.get(msg.sid);
                if (s) {
                    s.dispose();
                    this.logs.delete(msg.sid);
                    this.io.closeStream(msg.sid);
                } else {
                    this.cancelledLogs.add(msg.sid);
                }
                break;
            }
            case 'unsubscribe': {
                const view = this.views.get(msg.qid);
                if (view?.timer) this.io.cancel(view.timer);
                this.views.delete(msg.qid);
                const rid = this.nextRid++;
                this.pending.set(rid, { kind: 'ignore' });
                this.sendRequest(rid, 'view.unsubscribe', { qid: msg.qid });
                break;
            }
        }
    }

    private newView(qid: number, gen: number): View {
        return {
            qid,
            gen,
            columns: null,
            dictCols: [],
            rows: new Map(),
            dirty: new Set(),
            removed: new Set(),
            total: 0,
            offset: 0,
            ids: null,
            early: [],
            timer: null,
            lastFlush: -Infinity
        };
    }

    private sendRequest(rid: number, method: string, params?: unknown): void {
        this.io.send(encodeFrame(KindRequest, rid, 0, { method, params }));
    }

    // ---- socket → worker ----

    handleFrame(data: ArrayBuffer | Uint8Array): void {
        const f = decodeFrame(data);
        switch (f.kind) {
            case KindHello: {
                const hello = f.payload as Hello;
                if (!hello || hello.version !== Version) {
                    this.io.post({ t: 'status', status: 'error', message: `protocol version mismatch (agent ${hello?.version}, client ${Version})` });
                    this.io.closeSocket();
                    return;
                }
                this.ready = true;
                this.io.post({ t: 'status', status: 'open', agentVersion: hello.agentVersion, mode: hello.mode });
                const q = this.queued;
                this.queued = [];
                for (const m of q) this.handleMain(m);
                break;
            }
            case KindDict: {
                const add = f.payload as DictAdd;
                for (let i = 0; i < add.strings.length; i++) this.dict[add.base + i] = add.strings[i];
                break;
            }
            case KindClusterState:
                this.io.post({ t: 'clusterStates', states: (f.payload as ClusterStates).states ?? [] });
                break;
            case KindObject:
                // Not acknowledged; object payloads are single objects, so they go to the main thread as they come.
                this.io.post({ t: 'object', oid: f.channel, frame: f.payload as ObjectFrameWire });
                break;
            case KindJob:
                this.io.post({ t: 'job', frame: f.payload as JobFrame });
                break;
            case KindResponse:
                this.handleResponse(f.channel, f.payload as Response);
                break;
            case KindPatch:
                this.handlePatch(f.channel, f.payload as ViewPatch);
                // Ack after applying, for every patch frame, including dropped ones, or the agent stalls.
                this.io.send(encodeFrame(KindAck, 0, f.seq));
                break;
        }
    }

    handleClosed(message?: string): void {
        this.ready = false;
        for (const [, p] of this.pending) {
            if (p.kind === 'passthrough') {
                this.io.post({ t: 'response', rid: p.rid, error: { code: 'unavailable', message: 'connection closed' } });
            }
        }
        this.pending.clear();
        for (const v of this.views.values()) if (v.timer) this.io.cancel(v.timer);
        this.io.post({ t: 'status', status: 'closed', message });
    }

    private handleResponse(rid: number, res: Response): void {
        const p = this.pending.get(rid);
        if (!p) return;
        this.pending.delete(rid);
        if (p.kind === 'passthrough') {
            this.io.post({ t: 'response', rid: p.rid, result: res.result, error: res.error });
        } else if (p.kind === 'logopen') {
            if (this.cancelledLogs.delete(p.sid)) return;
            if (res.error || !res.result) {
                this.io.post({ t: 'log.error', sid: p.sid, error: res.error ?? { code: 'internal', message: 'empty stream.open result' } });
                return;
            }
            const r = res.result as StreamOpenResult;
            if (!r.streamId) {
                this.io.post({ t: 'log.error', sid: p.sid, error: { code: 'internal', message: 'stream.open returned no stream id' } });
                return;
            }
            const sid = p.sid;
            const session = new LogSession(
                { sid, capacity: p.capacity, multi: p.multi },
                {
                    send: text => this.io.sendStream(sid, text),
                    closeSocket: () => this.io.closeStream(sid),
                    post: m => this.io.post(m),
                    schedule: (fn, ms) => this.io.schedule(fn, ms),
                    cancel: h => this.io.cancel(h),
                    now: () => this.io.now()
                }
            );
            this.logs.set(sid, session);
            this.io.connectStream(sid, streamUrl(this.endpoint.url), [Subprotocol, 'token.' + this.endpoint.token, 'stream.' + r.streamId]);
            this.io.post({ t: 'log.opened', sid, usedContext: r.usedContext });
        } else if (p.kind === 'subscribe') {
            const view = this.views.get(p.qid);
            if (!view) return;
            if (res.error || !res.result) {
                this.io.post({ t: 'columns', qid: p.qid, columns: [] });
                return;
            }
            const { columns } = res.result as ViewSubscribeResult;
            view.columns = columns;
            view.dictCols = [];
            columns.forEach((c, i) => c.type === ColDict && view.dictCols.push(i + 2));
            this.io.post({ t: 'columns', qid: p.qid, columns });
            const early = view.early;
            view.early = [];
            for (const patch of early) this.applyPatch(view, patch);
        }
    }

    private handlePatch(qid: number, patch: ViewPatch): void {
        const view = this.views.get(qid);
        if (!view) return;
        if (patch.gen < view.gen) return; // stale: computed for an older query
        if (!view.columns) {
            view.early.push(patch);
            return;
        }
        this.applyPatch(view, patch);
    }

    private applyPatch(view: View, patch: ViewPatch): void {
        if (patch.gen < view.gen) return;
        const dictCols = view.dictCols;
        const dict = this.dict;
        const rows = patch.rows;
        if (rows) {
            for (let i = 0; i < rows.length; i++) {
                const r = rows[i] as unknown as Row & unknown[];
                for (let c = 0; c < dictCols.length; c++) {
                    const col = dictCols[c];
                    const ref = r[col] as number;
                    r[col] = ref ? (dict[ref] ?? '') : '';
                }
                const id = r[0] as number;
                view.rows.set(id, r);
                view.dirty.add(id);
                view.removed.delete(id);
            }
        }
        if (patch.removed) {
            for (const id of patch.removed) {
                view.rows.delete(id);
                view.dirty.delete(id);
                view.removed.add(id);
            }
        }
        view.total = patch.total;
        view.offset = patch.offset;
        if (patch.ids) {
            view.ids = decodeIds(patch.ids);
            // `removed` is advisory (stale-gen patches or abandoned frames can lose it): the id list is authoritative.
            const keep = new Set<number>(view.ids);
            for (const id of view.rows.keys()) {
                if (!keep.has(id)) {
                    view.rows.delete(id);
                    view.dirty.delete(id);
                }
            }
        }
        this.scheduleFlush(view);
    }

    // ---- coalesced posting ----

    private scheduleFlush(view: View): void {
        if (view.timer) return;
        const wait = Math.max(0, FLUSH_MS - (this.io.now() - view.lastFlush));
        view.timer = this.io.schedule(() => {
            view.timer = null;
            this.flush(view);
        }, wait);
    }

    /** Posts everything pending for a view now. Exposed for tests; normally timer-driven. */
    flush(view: View | number): void {
        const v = typeof view === 'number' ? this.views.get(view) : view;
        if (!v) return;
        if (v.timer) {
            this.io.cancel(v.timer);
            v.timer = null;
        }
        v.lastFlush = this.io.now();
        let chunk: Row[] = [];
        for (const id of v.dirty) {
            const row = v.rows.get(id);
            if (!row) continue;
            chunk.push(row);
            if (chunk.length === CHUNK_ROWS) {
                this.io.post({ t: 'rows', qid: v.qid, gen: v.gen, rows: chunk });
                chunk = [];
            }
        }
        if (chunk.length) this.io.post({ t: 'rows', qid: v.qid, gen: v.gen, rows: chunk });
        v.dirty.clear();
        const ids = v.ids;
        v.ids = null;
        const removed = v.removed.size ? [...v.removed] : undefined;
        v.removed.clear();
        this.io.post(
            { t: 'commit', qid: v.qid, gen: v.gen, total: v.total, offset: v.offset, ids: ids ?? undefined, removed },
            ids ? [ids.buffer as ArrayBuffer] : undefined
        );
    }

    // ---- log stream sockets ----

    handleStreamBinary(sid: number, bytes: Uint8Array): void {
        this.logs.get(sid)?.onBinary(bytes);
    }

    handleStreamText(sid: number, text: string): void {
        this.logs.get(sid)?.onText(text);
    }

    handleStreamClosed(sid: number, reason: string): void {
        this.logs.get(sid)?.onClose(reason);
    }

    /** Test/diagnostic accessor. */
    logSession(sid: number): LogSession | undefined {
        return this.logs.get(sid);
    }

    /** Test/diagnostic accessor. */
    rowCount(qid: number): number {
        return this.views.get(qid)?.rows.size ?? 0;
    }
}
