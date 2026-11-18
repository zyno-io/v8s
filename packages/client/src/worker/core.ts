import type { ClusterStates, DictAdd, Hello, JobFrame, Response, StreamOpenResult, ViewPatch, ViewSubscribeResult } from '../protocol.gen.ts';
import type { Column, MainToWorker, ObjectFrameWire, Row, ViewQuery, Window, WorkerToMain } from '../types.ts';

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
import { FlowSession } from '../flows/session.ts';
import { LogSession } from '../logs/session.ts';
import { ColDict, Subprotocol, Version } from '../protocol.gen.ts';

/** Max rows per posted message: keeps one structured clone well under ~2 ms. */
export const CHUNK_ROWS = 500;
/** Minimum spacing between posts for one view. */
export const FLUSH_MS = 16;
/** Wire request ids allocated by the worker itself (subscribe/update/unsubscribe); main-thread ids stay below this. */
const INTERNAL_RID_BASE = 0x80000000;
/** Reconnect backoff: 0.5 s doubling to a 15 s cap, then jittered down by up to 25 %. */
export const RECONNECT_BASE_MS = 500;
export const RECONNECT_MAX_MS = 15_000;
/** A reconnect attempt that has not completed its handshake by then is abandoned (a half-open network). */
export const RECONNECT_ATTEMPT_MS = 10_000;
/** After a reconnect's Hello the cluster states normally follow at once; views are resubscribed without them after this long. */
export const STATES_WAIT_MS = 2000;

/** Delay before reconnect attempt number `attempt` (0-based). `rand` is in [0, 1). Never above RECONNECT_MAX_MS. */
export function backoffMs(attempt: number, rand: number): number {
    const base = Math.min(RECONNECT_MAX_MS, RECONNECT_BASE_MS * 2 ** Math.min(attempt, 10));
    return Math.round(base * (0.75 + 0.25 * rand));
}

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
    /** Random number in [0, 1) for backoff jitter (default Math.random). */
    random?(): number;
    /**
     * Hosted mode only (no token): resolves true when the session has ended (`/api/me` answers 401). The browser hides a 401
     * on the WebSocket upgrade, so this is how the worker tells "sign in again" from "network down".
     */
    sessionEnded?(): Promise<boolean>;
}

interface View {
    qid: number;
    gen: number;
    columns: Column[] | null;
    dictCols: number[];
    /** The query and window as the page last set them: what a resubscribe after a reconnect sends. */
    query: ViewQuery;
    window: Window;
    /** Resubscribed after a reconnect: the next flush first tells the page that wire ids restarted. */
    reset: boolean;
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
    | { kind: 'passthrough'; rid: number; select?: string[] }
    | { kind: 'subscribe'; qid: number }
    | { kind: 'logopen'; sid: number; capacity: number; maxBytes?: number; multi: boolean }
    | { kind: 'flowopen'; sid: number; capacity: number }
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
    /** Whether a handshake ever completed: only then does a close lead to reconnecting. */
    private everReady = false;
    /** Closed on purpose (page asked, or protocol mismatch): never reconnect. */
    private stopped = false;
    private reconnecting = false;
    private attempt = 0;
    private socketLive = false;
    private retryTimer: unknown = null;
    private attemptTimer: unknown = null;
    /** The clusters the agent last reported live or this page last selected successfully: all the worker restores after a reconnect (never a merely pre-checked set). */
    private selected: string[] = [];
    /** Reconnected, waiting for the agent's first cluster states before resubscribing views. */
    private awaitStates = false;
    private statesTimer: unknown = null;
    private logs = new Map<number, LogSession>();
    private cancelledLogs = new Set<number>();
    private flows = new Map<number, FlowSession>();
    private cancelledFlows = new Set<number>();

    constructor(private io: CoreIO) {}

    // ---- main thread → worker ----

    handleMain(msg: MainToWorker): void {
        if (msg.t === 'connect') {
            this.endpoint = { url: msg.url, token: msg.token };
            this.io.post({ t: 'status', status: 'connecting' });
            this.socketLive = true;
            this.io.connect(msg.url, msg.token);
            return;
        }
        if (msg.t === 'close') {
            this.stopped = true;
            const waiting = this.reconnecting && !this.socketLive;
            this.clearTimers();
            if (waiting) {
                this.reconnecting = false;
                this.io.post({ t: 'status', status: 'closed', message: 'closed' });
            } else this.io.closeSocket();
            return;
        }
        if (msg.t === 'nudge') {
            this.reconnectNow();
            return;
        }
        // Reconnected, views not resubscribed yet: they are recorded locally and sent once, in order, by `restore`.
        if (this.awaitStates && (msg.t === 'subscribe' || msg.t === 'update' || msg.t === 'unsubscribe') && this.localOnly(msg)) return;
        if (!this.ready) {
            // Before the first Hello everything waits for it. After that nothing queues: requests fail, view bookkeeping is
            // local, and log/flow session controls (scroll, search, pause, close) act on their local sessions below.
            if (!this.everReady) {
                this.queued.push(msg);
                return;
            }
            if (this.localOnly(msg)) return;
        }
        switch (msg.t) {
            case 'request':
                this.pending.set(msg.rid, {
                    kind: 'passthrough',
                    rid: msg.rid,
                    select: msg.method === 'clusters.select' ? ((msg.params as { ids?: string[] } | undefined)?.ids ?? []) : undefined
                });
                this.sendRequest(msg.rid, msg.method, msg.params);
                break;
            case 'subscribe': {
                const view = this.newView(msg.qid, msg.gen, msg.query, msg.window);
                this.views.set(msg.qid, view);
                const rid = this.nextRid++;
                this.pending.set(rid, { kind: 'subscribe', qid: msg.qid });
                this.sendRequest(rid, 'view.subscribe', { qid: msg.qid, gen: msg.gen, query: msg.query, window: msg.window });
                break;
            }
            case 'update': {
                const view = this.views.get(msg.qid);
                if (!view) break;
                this.noteUpdate(view, msg);
                const rid = this.nextRid++;
                this.pending.set(rid, { kind: 'ignore' });
                this.sendRequest(rid, 'view.update', { qid: msg.qid, gen: msg.gen, query: msg.query, window: msg.window });
                break;
            }
            case 'log.open': {
                const rid = this.nextRid++;
                this.pending.set(rid, { kind: 'logopen', sid: msg.sid, capacity: msg.capacity, maxBytes: msg.maxBytes, multi: msg.multi });
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
            case 'flow.open': {
                const rid = this.nextRid++;
                this.pending.set(rid, { kind: 'flowopen', sid: msg.sid, capacity: msg.capacity });
                this.sendRequest(rid, 'stream.open', { ...msg.params, type: 'flows' });
                break;
            }
            case 'flow.view':
                this.flows.get(msg.sid)?.setView(msg.first, msg.count, msg.follow);
                break;
            case 'flow.pause':
                this.flows.get(msg.sid)?.setPaused(msg.paused);
                break;
            case 'flow.clear':
                this.flows.get(msg.sid)?.clear();
                break;
            case 'flow.close': {
                const s = this.flows.get(msg.sid);
                if (s) {
                    s.dispose();
                    this.flows.delete(msg.sid);
                    this.io.closeStream(this.flowKey(msg.sid));
                } else {
                    this.cancelledFlows.add(msg.sid);
                }
                break;
            }
            case 'unsubscribe': {
                const view = this.views.get(msg.qid);
                if (view) this.dropView(view);
                this.views.delete(msg.qid);
                const rid = this.nextRid++;
                this.pending.set(rid, { kind: 'ignore' });
                this.sendRequest(rid, 'view.unsubscribe', { qid: msg.qid });
                break;
            }
        }
    }

    /** Records a view.update on the worker's copy: gen bump, and the query/window a resubscribe would need. */
    private noteUpdate(view: View, msg: { gen: number; query?: ViewQuery; window?: Window }): void {
        if (msg.gen > view.gen) {
            view.gen = msg.gen;
            // Anything computed for the old query must not reach the screen after the bump.
            view.ids = null;
            view.early.length = 0;
        }
        if (msg.query) view.query = msg.query;
        if (msg.window) view.window = msg.window;
    }

    /**
     * Handles a message while the socket is down after having been up. Requests and stream opens fail at once (replaying a
     * stale mutation minutes later would be wrong); view bookkeeping is applied locally and sent on resubscribe. True: done.
     */
    private localOnly(msg: MainToWorker): boolean {
        const unavailable = { code: 'unavailable', message: 'reconnecting to the agent' };
        switch (msg.t) {
            case 'request':
                this.io.post({ t: 'response', rid: msg.rid, error: unavailable });
                return true;
            case 'log.open':
                this.io.post({ t: 'log.error', sid: msg.sid, error: unavailable });
                return true;
            case 'flow.open':
                this.io.post({ t: 'flow.error', sid: msg.sid, error: unavailable });
                return true;
            case 'subscribe':
                this.views.set(msg.qid, this.newView(msg.qid, msg.gen, msg.query, msg.window));
                return true;
            case 'update': {
                const view = this.views.get(msg.qid);
                if (view) this.noteUpdate(view, msg);
                return true;
            }
            case 'unsubscribe': {
                const view = this.views.get(msg.qid);
                if (view) this.dropView(view);
                this.views.delete(msg.qid);
                return true;
            }
            default:
                return false; // log/flow session controls act on local sessions
        }
    }

    private dropView(view: View): void {
        if (view.timer) this.io.cancel(view.timer);
        view.timer = null;
    }

    private newView(qid: number, gen: number, query: ViewQuery, window: Window): View {
        return {
            qid,
            gen,
            query,
            window,
            reset: false,
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
                    this.stopped = true;
                    this.clearTimers();
                    this.io.closeSocket();
                    return;
                }
                const reconnected = this.everReady;
                this.ready = true;
                this.everReady = true;
                this.reconnecting = false;
                this.attempt = 0;
                this.clearTimers();
                // Dictionary ids and wire view ids are per connection: nothing from the previous socket carries over.
                this.dict = [];
                this.io.post({ t: 'status', status: 'open', agentVersion: hello.agentVersion, mode: hello.mode });
                if (reconnected && (this.views.size || this.selected.length)) {
                    // The agent pushes its cluster states right after Hello; if it came back with none live (a restart, an evicted
                    // hosted engine) select this page's clusters again BEFORE resubscribing, so each view resolves against them.
                    this.awaitStates = true;
                    this.statesTimer = this.io.schedule(() => this.restore(false), STATES_WAIT_MS);
                }
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
            case KindClusterState: {
                const states = (f.payload as ClusterStates).states ?? [];
                if (this.awaitStates) this.restore(states.length > 0);
                // What the agent really has connected (this page may have loaded onto an agent another tab already connected).
                else if (states.length) this.selected = states.map(s => s.id);
                this.io.post({ t: 'clusterStates', states });
                break;
            }
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

    /** A view lives on across a reconnect: same qid and gen, fresh row table, one new view.subscribe; the page keeps showing its rows until the new snapshot's id list replaces them. */
    private resubscribe(view: View): void {
        this.dropView(view);
        view.columns = null;
        view.dictCols = [];
        view.rows.clear();
        view.dirty.clear();
        view.removed.clear();
        view.ids = null;
        view.early = [];
        view.reset = true;
        // Wire ids restart on the new connection, so an anchorId from the old one would point at a different row (or none):
        // ask for the same absolute range the last patch covered instead.
        const window = { offset: view.window.anchorId ? view.offset : view.window.offset, limit: view.window.limit };
        const rid = this.nextRid++;
        this.pending.set(rid, { kind: 'subscribe', qid: view.qid });
        this.sendRequest(rid, 'view.subscribe', { qid: view.qid, gen: view.gen, query: view.query, window });
    }

    /** Reselects this page's clusters if none are live, then resubscribes every view in order. */
    private restore(live: boolean): void {
        if (!this.awaitStates) return;
        this.awaitStates = false;
        if (this.statesTimer) this.io.cancel(this.statesTimer);
        this.statesTimer = null;
        if (!live && this.selected.length) {
            const rid = this.nextRid++;
            this.pending.set(rid, { kind: 'ignore' });
            this.sendRequest(rid, 'clusters.select', { ids: this.selected });
        }
        for (const view of this.views.values()) this.resubscribe(view);
    }

    private clearTimers(): void {
        this.awaitStates = false;
        if (this.statesTimer) this.io.cancel(this.statesTimer);
        this.statesTimer = null;
        if (this.retryTimer) this.io.cancel(this.retryTimer);
        if (this.attemptTimer) this.io.cancel(this.attemptTimer);
        this.retryTimer = this.attemptTimer = null;
    }

    /** Network came back or the tab became visible: skip the wait and try now. */
    private reconnectNow(): void {
        if (!this.reconnecting || this.stopped || this.socketLive) return;
        this.clearTimers();
        this.attemptConnect();
    }

    private attemptConnect(): void {
        this.retryTimer = null;
        this.socketLive = true;
        this.io.connect(this.endpoint.url, this.endpoint.token);
        // A socket stuck connecting (dead network) would block every later attempt: give up on it.
        this.attemptTimer = this.io.schedule(() => {
            this.attemptTimer = null;
            this.io.closeSocket();
        }, RECONNECT_ATTEMPT_MS);
    }

    private scheduleRetry(): void {
        const rand = this.io.random ? this.io.random() : Math.random();
        const delay = backoffMs(this.attempt++, rand);
        this.retryTimer = this.io.schedule(() => this.attemptConnect(), delay);
    }

    handleClosed(message?: string): void {
        this.ready = false;
        this.socketLive = false;
        this.awaitStates = false;
        if (this.statesTimer) this.io.cancel(this.statesTimer);
        this.statesTimer = null;
        if (this.attemptTimer) this.io.cancel(this.attemptTimer);
        this.attemptTimer = null;
        for (const [, p] of this.pending) {
            if (p.kind === 'passthrough') {
                this.io.post({ t: 'response', rid: p.rid, error: { code: 'unavailable', message: 'connection closed' } });
            } else if (p.kind === 'flowopen' && !this.cancelledFlows.delete(p.sid)) {
                this.io.post({ t: 'flow.error', sid: p.sid, error: { code: 'unavailable', message: 'connection closed' } });
            } else if (p.kind === 'logopen' && !this.cancelledLogs.delete(p.sid)) {
                // A stream.open that never gets its response: settle the opener so the pane can offer Reconnect.
                this.io.post({ t: 'log.error', sid: p.sid, error: { code: 'unavailable', message: 'connection closed' } });
            }
        }
        this.pending.clear();
        for (const v of this.views.values()) this.dropView(v);
        if (this.stopped || !this.everReady) {
            this.reconnecting = false;
            this.clearTimers();
            this.io.post({ t: 'status', status: 'closed', message });
            return;
        }
        this.reconnecting = true;
        this.io.post({ t: 'status', status: 'reconnecting', message });
        this.scheduleRetry();
        // Hosted: an ended session is not a network problem; stop instead of knocking on a door that answers 401.
        if (!this.endpoint.token && this.io.sessionEnded) {
            void this.io.sessionEnded().then(
                ended => {
                    if (!ended || !this.reconnecting || this.ready || this.stopped) return;
                    this.stopped = true;
                    this.reconnecting = false;
                    this.clearTimers();
                    if (this.socketLive) this.io.closeSocket();
                    else this.io.post({ t: 'status', status: 'closed', message: 'session ended' });
                },
                () => {}
            );
        }
    }

    private handleResponse(rid: number, res: Response): void {
        const p = this.pending.get(rid);
        if (!p) return;
        this.pending.delete(rid);
        if (p.kind === 'passthrough') {
            if (p.select && !res.error) this.selected = p.select;
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
                { sid, capacity: p.capacity, maxBytes: p.maxBytes, multi: p.multi },
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
            this.io.connectStream(sid, streamUrl(this.endpoint.url), this.streamProtocols(r.streamId));
            this.io.post({ t: 'log.opened', sid, usedContext: r.usedContext });
        } else if (p.kind === 'flowopen') {
            if (this.cancelledFlows.delete(p.sid)) return;
            const r = res.result as StreamOpenResult | undefined;
            if (res.error || !r?.streamId) {
                this.io.post({ t: 'flow.error', sid: p.sid, error: res.error ?? { code: 'internal', message: 'stream.open returned no stream id' } });
                return;
            }
            const sid = p.sid;
            const key = this.flowKey(sid);
            const session = new FlowSession(
                { sid, capacity: p.capacity },
                {
                    send: text => this.io.sendStream(key, text),
                    closeSocket: () => this.io.closeStream(key),
                    post: m => this.io.post(m),
                    schedule: (fn, ms) => this.io.schedule(fn, ms),
                    cancel: h => this.io.cancel(h),
                    now: () => this.io.now()
                }
            );
            this.flows.set(sid, session);
            this.io.connectStream(key, streamUrl(this.endpoint.url), this.streamProtocols(r.streamId));
            this.io.post({ t: 'flow.opened', sid, usedContext: r.usedContext });
        } else if (p.kind === 'subscribe') {
            const view = this.views.get(p.qid);
            if (!view) return;
            if (res.error || !res.result) {
                this.io.post({
                    t: 'columns',
                    qid: p.qid,
                    columns: [],
                    error: res.error ?? { code: 'internal', message: 'The agent returned no result for the view.' }
                });
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
        if (v.reset) {
            v.reset = false;
            this.io.post({ t: 'reset', qid: v.qid });
        }
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

    /** Log and flow streams share one socket-key space: flows use the negative of their sid. */
    private flowKey(sid: number): number {
        return -sid;
    }

    /** Offered subprotocols of a `/stream` socket: no `token.*` in hosted mode (the cookie authenticates). */
    private streamProtocols(streamId: string): string[] {
        return this.endpoint.token ? [Subprotocol, 'token.' + this.endpoint.token, 'stream.' + streamId] : [Subprotocol, 'stream.' + streamId];
    }

    handleStreamBinary(sid: number, bytes: Uint8Array): void {
        if (sid > 0) this.logs.get(sid)?.onBinary(bytes);
    }

    handleStreamText(sid: number, text: string): void {
        if (sid < 0) this.flows.get(-sid)?.onText(text);
        else this.logs.get(sid)?.onText(text);
    }

    handleStreamClosed(sid: number, reason: string): void {
        if (sid < 0) this.flows.get(-sid)?.onClose(reason);
        else this.logs.get(sid)?.onClose(reason);
    }

    /** Test/diagnostic accessor. */
    flowSession(sid: number): FlowSession | undefined {
        return this.flows.get(sid);
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
