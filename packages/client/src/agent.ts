import type { ShallowRef } from 'vue';

import { markRaw, shallowRef } from 'vue';

import type {
    ClustersListResult,
    ClusterStates,
    ErrorBody,
    Flow,
    FlowFilter,
    FlowRate,
    FlowScope,
    JobFrame,
    JobsListResult,
    KindsListResult,
    ObjectPayload,
    ObjectRef,
    StreamOpenParams,
    StreamOpenResult
} from './protocol.gen.ts';
import type { ShellHandle } from './shell.ts';
import type {
    AgentEndpoint,
    ClusterState,
    Column,
    ConnectionStatus,
    FlowUpdate,
    LogLineView,
    LogSearchState,
    LogUpdate,
    MainToWorker,
    ObjectFrameWire,
    Row,
    ViewQuery,
    Window,
    WorkerToMain
} from './types.ts';

import { Subprotocol } from './protocol.gen.ts';
import { createShell } from './shell.ts';

export interface ViewHandle {
    readonly qid: number;
    /**
     * Held non-reactively: read these after touching `version.value`. Never wrap in ref()/reactive().
     * `ids` is the id list of the current window; ids[i] is the row at view index `offset + i`.
     */
    readonly rows: Map<number, Row>;
    ids: Uint32Array;
    /** Bumped once per committed flush from the worker. */
    readonly version: ShallowRef<number>;
    readonly total: ShallowRef<number>;
    /** View index of ids[0]. */
    readonly offset: ShallowRef<number>;
    readonly columns: ShallowRef<Column[] | null>;
    /** The subscribe failure (the agent refused the query, or an involved-object events view could not load); null while fine. */
    readonly error: ShallowRef<ErrorBody | null>;
    /**
     * Bumped when the view was resubscribed after a reconnect. Row ids are per subscription, so an id the page remembers (a focused
     * or expanded row, a scroll anchor) may name a different row now: clear such state when this changes.
     */
    readonly epoch: ShallowRef<number>;
    readonly gen: number;
    readonly query: ViewQuery;
    readonly window: Window;
    update(patch: { query?: ViewQuery; window?: Window }): void;
    close(): void;
}

export interface LogOpenOptions extends Omit<StreamOpenParams, 'type'> {
    /** Lines kept in the ring buffer: desktop 50k, phone 5k. */
    capacity?: number;
    /** Bytes kept in the ring buffer: desktop 32 MiB, phone 8 MiB. */
    maxBytes?: number;
    /** The stream is merged from several pods/containers (lines carry a `<pod>/<container>\t` prefix). */
    multi?: boolean;
}

/**
 * A log stream owned by the Data Worker. Nothing here is a dataset: the ring buffer lives in the worker and the main thread only
 * holds the counters and the lines of the viewport it asked for. Read `lineAt()` after touching `version.value`.
 */
export interface LogStreamHandle {
    readonly sid: number;
    readonly usedContext: string;
    readonly state: ShallowRef<'live' | 'ended' | 'error'>;
    readonly endError: ShallowRef<string>;
    /** Lines ever appended (absolute index one past the newest). */
    readonly total: ShallowRef<number>;
    /** Absolute index of the oldest line still buffered; everything before was dropped. */
    readonly first: ShallowRef<number>;
    /** Bumped once per batched post (at most one per frame). */
    readonly version: ShallowRef<number>;
    readonly search: ShallowRef<LogSearchState>;
    /** Set after a next/prev step: scroll to `line`. `seq` changes on every step even when the line repeats. */
    readonly reveal: ShallowRef<{ line: number; seq: number } | null>;
    lineAt(abs: number): LogLineView | undefined;
    /** Tells the worker which lines to post: `[first, first + count)`, or the newest `count` when `follow`. */
    setView(first: number, count: number, follow: boolean): void;
    searchFor(q: string, matchCase?: boolean): void;
    searchStep(dir: 1 | -1, fromLine?: number): void;
    /** Withholds credit (TCP backpressure to the kubelet) while true, e.g. a hidden tab. */
    setPaused(paused: boolean): void;
    clear(): void;
    close(): void;
}

export interface FlowOpenOptions {
    cluster: string;
    scope: FlowScope;
    filter?: FlowFilter;
    /** Flows kept in the ring buffer: desktop 20k, phone 2k. */
    capacity?: number;
    useWriteContext?: boolean;
}

/**
 * A Hubble flows stream owned by the Data Worker. Like a log stream nothing here is a dataset: the ring buffer lives in the worker
 * and the main thread holds the counters and the flows of the viewport it asked for. Read `flowAt()` after touching `version.value`.
 */
export interface FlowStreamHandle {
    readonly sid: number;
    readonly usedContext: string;
    readonly state: ShallowRef<'live' | 'ended' | 'error'>;
    readonly endError: ShallowRef<string>;
    /** Flows ever received (absolute index one past the newest). */
    readonly total: ShallowRef<number>;
    /** Absolute index of the oldest flow still buffered. */
    readonly first: ShallowRef<number>;
    /** Latest sampling report (`shown`/`total` per second); null until the agent sends one. */
    readonly rate: ShallowRef<FlowRate | null>;
    /** Bumped once per batched post (at most one per frame). */
    readonly version: ShallowRef<number>;
    flowAt(abs: number): Flow | undefined;
    /** Tells the worker which flows to post: `[first, first + count)`, or the newest `count` when `follow`. */
    setView(first: number, count: number, follow: boolean): void;
    /** Pauses the agent side (hidden tab); a stream paused for 10 s is cancelled by the agent. */
    setPaused(paused: boolean): void;
    clear(): void;
    close(): void;
}

/** A live single-object subscription (`object.subscribe`); frames arrive on `onFrame` until `close()`. */
export interface ObjectSubscription {
    readonly oid: number;
    close(): void;
}

/** A save-file handle from `showSaveFilePicker`. */
export interface LogFileTarget {
    createWritable(): Promise<WritableStream<Uint8Array>>;
}

export interface Agent {
    readonly status: ShallowRef<ConnectionStatus>;
    /** Bumped every time the connection comes back after a drop (views are already resubscribed by then). */
    readonly reconnects: ShallowRef<number>;
    /** Skips the reconnect backoff and tries now (the "Retry now" button). No effect unless reconnecting. */
    reconnectNow(): void;
    readonly statusMessage: ShallowRef<string>;
    readonly agentInfo: ShallowRef<{ version: string; mode: string } | null>;
    readonly clusterStates: ShallowRef<ClusterState[]>;
    listClusters(): Promise<ClustersListResult>;
    selectClusters(ids: string[]): Promise<ClusterStates>;
    listKinds(): Promise<KindsListResult>;
    /** Any request/response method (`object.get`, `settings.set`, …). Rejects with RpcError. */
    call<T = unknown>(method: string, params?: unknown): Promise<T>;
    /** Subscribes to one object. The promise resolves once the agent accepted the subscription. */
    subscribeObject(ref: ObjectRef, onFrame: (frame: ObjectFrameWire) => void): Promise<ObjectSubscription>;
    subscribeView(query: ViewQuery, window: Window): ViewHandle;
    /** Opens a log stream in the Data Worker. Rejects with RpcError (`write_confirm` etc.) when `stream.open` is refused. */
    openStream(type: 'logs', params: LogOpenOptions): Promise<LogStreamHandle>;
    /** Opens a Hubble flows stream in the Data Worker. Rejects with RpcError (`bad_request` for an unscoped follow, `unavailable`, …). */
    openStream(type: 'flows', params: FlowOpenOptions): Promise<FlowStreamHandle>;
    /** Opens an interactive stream (exec, debug container, node shell) on its own socket, on the main thread. */
    openStream(type: 'exec' | 'debug' | 'nodeshell', params: Omit<StreamOpenParams, 'type'>): Promise<ShellHandle>;
    /** Streams the whole log to a file (File System Access API when available, else a browser download). */
    downloadLog(params: Omit<StreamOpenParams, 'type' | 'download'>, filename: string, target?: LogFileTarget | null): Promise<void>;
    /**
     * Asks where to save (File System Access API). Call it first thing in the click handler, once, outside any confirm/retry
     * wrapper: the picker needs user activation. Resolves `null` when the API is missing (the download falls back to the
     * browser's own), `false` when the user dismissed the picker; pass a result to `downloadLog`.
     */
    pickLogFile(filename: string): Promise<LogFileTarget | null | false>;
    /** Every job the agent knows (running first), updated by `Job` frames; reattaches after a reload through `jobs.list`. */
    readonly jobs: ShallowRef<readonly JobFrame[]>;
    refreshJobs(): Promise<void>;
    close(): void;
}

export interface WorkerLike {
    postMessage(msg: MainToWorker, transfer?: Transferable[]): void;
    onmessage: ((ev: MessageEvent<WorkerToMain>) => void) | null;
    terminate(): void;
}

export interface ConnectOptions extends AgentEndpoint {
    /** Override for tests. */
    workerFactory?: () => WorkerLike;
}

function defaultWorker(): WorkerLike {
    return new Worker(new URL('./worker/data.worker.ts', import.meta.url), { type: 'module' }) as unknown as WorkerLike;
}

export class RpcError extends Error {
    constructor(
        readonly code: string,
        message: string,
        /** `conflict` errors carry the live object. */
        readonly live?: ObjectPayload,
        /** Server hint for the user (e.g. which Flux action to use instead). */
        readonly hint?: string,
        /** Multi-document apply: documents that succeeded before the failing one. */
        readonly applied?: ObjectRef[]
    ) {
        super(message);
    }
}

export type { ErrorBody };

/** Opens the data worker and the socket. Resolves once the agent's Hello has been accepted. */
export function connectAgent(opts: ConnectOptions): Promise<Agent> {
    const worker = (opts.workerFactory ?? defaultWorker)();
    const status = shallowRef<ConnectionStatus>('connecting');
    const statusMessage = shallowRef('');
    const reconnects = shallowRef(0);
    let wasOpen = false;
    const agentInfo = shallowRef<{ version: string; mode: string } | null>(null);
    const clusterStates = shallowRef<ClusterState[]>([]);
    const calls = new Map<number, { resolve: (v: unknown) => void; reject: (e: Error) => void }>();
    const views = new Map<number, InternalView>();
    const objects = new Map<number, (frame: ObjectFrameWire) => void>();
    const objectRefs = new Map<number, ObjectRef>();
    const jobs = shallowRef<readonly JobFrame[]>([]);
    const jobMap = new Map<string, JobFrame>();
    let jobTimer: ReturnType<typeof setTimeout> | undefined;
    const logs = new Map<number, InternalLog>();
    const flowStreams = new Map<number, { apply(m: FlowUpdate): void }>();
    const flowOpens = new Map<
        number,
        { resolve: (h: FlowStreamHandle) => void; reject: (e: Error) => void; make: (usedContext: string) => FlowStreamHandle }
    >();
    const logOpens = new Map<
        number,
        { resolve: (h: LogStreamHandle) => void; reject: (e: Error) => void; make: (usedContext: string) => LogStreamHandle }
    >();
    let nextSid = 1;
    let nextRid = 1;
    let nextQid = 1;
    let nextOid = 1;

    interface InternalView extends ViewHandle {
        gen: number;
        query: ViewQuery;
        window: Window;
        ids: Uint32Array;
    }

    interface InternalLog {
        handle: LogStreamHandle;
        apply(m: LogUpdate): void;
    }

    // Job frames carry whole job states; republish the list at most every 50 ms.
    function publishJobs() {
        if (jobTimer) return;
        jobTimer = setTimeout(() => {
            jobTimer = undefined;
            jobs.value = sortJobs([...jobMap.values()]);
        }, 50);
    }

    let onOpen: (() => void) | null = null;
    let onFail: ((e: Error) => void) | null = null;

    worker.onmessage = ev => {
        const m = ev.data;
        switch (m.t) {
            case 'status':
                status.value = m.status;
                statusMessage.value = m.message ?? '';
                if (m.status === 'open') {
                    agentInfo.value = { version: m.agentVersion ?? '', mode: m.mode ?? '' };
                    if (wasOpen) {
                        // Back after a drop: the worker resubscribed the views; object subscriptions are ours to restore.
                        for (const [oid, ref] of objectRefs) call('object.subscribe', { oid, ref }).catch(() => {});
                        reconnects.value++;
                    }
                    wasOpen = true;
                    void agent.refreshJobs();
                    onOpen?.();
                } else if (m.status === 'error' || m.status === 'closed' || m.status === 'reconnecting') {
                    onFail?.(new Error(m.message ?? 'connection failed'));
                    for (const [, c] of calls) c.reject(new RpcError('unavailable', m.message ?? 'connection closed'));
                    calls.clear();
                }
                break;
            case 'response': {
                const c = calls.get(m.rid);
                if (!c) break;
                calls.delete(m.rid);
                if (m.error) c.reject(new RpcError(m.error.code, m.error.message, m.error.live, m.error.hint, m.error.applied));
                else c.resolve(m.result);
                break;
            }
            case 'clusterStates':
                clusterStates.value = m.states;
                break;
            case 'object':
                objects.get(m.oid)?.(m.frame);
                break;
            case 'job':
                jobMap.set(m.frame.jobId, m.frame);
                publishJobs();
                break;
            case 'log.opened':
                logOpens.get(m.sid)?.resolve(logOpens.get(m.sid)!.make(m.usedContext ?? ''));
                logOpens.delete(m.sid);
                break;
            case 'log.error': {
                const o = logOpens.get(m.sid);
                logOpens.delete(m.sid);
                o?.reject(new RpcError(m.error.code, m.error.message, m.error.live, m.error.hint, m.error.applied));
                break;
            }
            case 'log':
                logs.get(m.sid)?.apply(m);
                break;
            case 'flow.opened':
                flowOpens.get(m.sid)?.resolve(flowOpens.get(m.sid)!.make(m.usedContext ?? ''));
                flowOpens.delete(m.sid);
                break;
            case 'flow.error': {
                const o = flowOpens.get(m.sid);
                flowOpens.delete(m.sid);
                o?.reject(new RpcError(m.error.code, m.error.message, m.error.live, m.error.hint, m.error.applied));
                break;
            }
            case 'flow':
                flowStreams.get(m.sid)?.apply(m);
                break;
            case 'columns': {
                const v = views.get(m.qid);
                if (v) {
                    v.columns.value = m.columns;
                    v.error.value = m.error ?? null;
                }
                break;
            }
            case 'reset': {
                const v = views.get(m.qid);
                if (v) {
                    // Old ids may be reused by the new snapshot, which can arrive in several chunks: drop rows keyed by them now.
                    v.rows.clear();
                    v.epoch.value++;
                }
                break;
            }
            case 'rows': {
                const v = views.get(m.qid);
                if (!v || m.gen < v.gen) break;
                const rows = v.rows;
                for (let i = 0; i < m.rows.length; i++) {
                    const r = m.rows[i];
                    rows.set(r[0], r);
                }
                break;
            }
            case 'commit': {
                const v = views.get(m.qid);
                if (!v || m.gen < v.gen) break;
                if (m.removed) for (const id of m.removed) v.rows.delete(id);
                if (m.ids) {
                    v.ids = m.ids;
                    // `removed` is advisory; the window's id list is authoritative (O(window)).
                    const keep = new Set<number>(m.ids);
                    for (const id of v.rows.keys()) if (!keep.has(id)) v.rows.delete(id);
                }
                if (v.total.value !== m.total) v.total.value = m.total;
                if (v.offset.value !== m.offset) v.offset.value = m.offset;
                v.version.value++;
                break;
            }
        }
    };

    function post(msg: MainToWorker, transfer?: Transferable[]) {
        worker.postMessage(msg, transfer);
    }

    function call<T>(method: string, params?: unknown): Promise<T> {
        const rid = nextRid++;
        return new Promise<T>((resolve, reject) => {
            calls.set(rid, { resolve: resolve as (v: unknown) => void, reject });
            post({ t: 'request', rid, method, params });
        });
    }

    const agent: Agent = {
        status,
        reconnects,
        reconnectNow: () => post({ t: 'nudge' }),
        statusMessage,
        agentInfo,
        clusterStates,
        listClusters: () => call('clusters.list'),
        async selectClusters(ids) {
            const res = await call<ClusterStates>('clusters.select', { ids });
            if (res?.states) clusterStates.value = res.states;
            return res;
        },
        listKinds: () => call('kinds.list'),
        call,
        async subscribeObject(ref, onFrame) {
            const oid = nextOid++;
            objects.set(oid, onFrame);
            objectRefs.set(oid, ref);
            try {
                await call('object.subscribe', { oid, ref });
            } catch (e) {
                objects.delete(oid);
                objectRefs.delete(oid);
                throw e;
            }
            return {
                oid,
                close() {
                    if (!objects.delete(oid)) return;
                    objectRefs.delete(oid);
                    call('object.unsubscribe', { oid }).catch(() => {});
                }
            };
        },
        subscribeView(query, window) {
            const qid = nextQid++;
            const handle: InternalView = markRaw({
                qid,
                rows: new Map<number, Row>(),
                ids: new Uint32Array(0),
                version: shallowRef(0),
                total: shallowRef(0),
                offset: shallowRef(0),
                columns: shallowRef<Column[] | null>(null),
                error: shallowRef<ErrorBody | null>(null),
                epoch: shallowRef(0),
                gen: 1,
                query,
                window,
                update(patch) {
                    if (!views.has(qid)) return;
                    const msg: MainToWorker = { t: 'update', qid, gen: handle.gen };
                    if (patch.query) {
                        handle.gen++;
                        handle.query = patch.query;
                        msg.gen = handle.gen;
                        msg.query = patch.query;
                    }
                    if (patch.window) {
                        handle.window = patch.window;
                        msg.window = patch.window;
                    }
                    post(msg);
                },
                close() {
                    if (!views.delete(qid)) return;
                    post({ t: 'unsubscribe', qid });
                }
            });
            views.set(qid, handle);
            post({ t: 'subscribe', qid, gen: handle.gen, query, window });
            return handle;
        },
        jobs,
        async refreshJobs() {
            try {
                const res = await call<JobsListResult>('jobs.list');
                for (const j of res?.jobs ?? []) jobMap.set(j.jobId, j);
                jobs.value = sortJobs([...jobMap.values()]);
            } catch {
                // jobs are optional: an older agent simply has none
            }
        },
        openStream(type: string, params: LogOpenOptions | FlowOpenOptions | Omit<StreamOpenParams, 'type'>): Promise<never> {
            if (type === 'logs') return openLogs(params as LogOpenOptions) as Promise<never>;
            if (type === 'flows') return openFlows(params as FlowOpenOptions) as Promise<never>;
            return openShell(type, params as Omit<StreamOpenParams, 'type'>) as Promise<never>;
        },
        pickLogFile,
        async downloadLog(params, filename, target) {
            await downloadLog(params, filename, target ?? null);
        },
        close() {
            removeNudgers();
            post({ t: 'close' });
            worker.terminate();
            status.value = 'closed';
        }
    };

    function openLogs(params: LogOpenOptions): Promise<LogStreamHandle> {
        const { capacity = 50_000, maxBytes, multi, ...rest } = params;
        const isMulti = multi ?? !(rest.ref?.gvr === 'v1/pods' && !!rest.container);
        const sid = nextSid++;
        return new Promise<LogStreamHandle>((resolve, reject) => {
            const make = (usedContext: string): LogStreamHandle => {
                const state = shallowRef<'live' | 'ended' | 'error'>('live');
                const endError = shallowRef('');
                const total = shallowRef(0);
                const first = shallowRef(0);
                const version = shallowRef(0);
                const search = shallowRef<LogSearchState>({ query: '', count: 0, current: 0 });
                const reveal = shallowRef<{ line: number; seq: number } | null>(null);
                let lines: readonly LogLineView[] = [];
                let from = 0;
                let revealSeq = 0;
                let closed = false;
                const handle: LogStreamHandle = markRaw({
                    sid,
                    usedContext,
                    state,
                    endError,
                    total,
                    first,
                    version,
                    search,
                    reveal,
                    lineAt(abs: number) {
                        const i = abs - from;
                        return i >= 0 && i < lines.length ? lines[i] : undefined;
                    },
                    setView: (f: number, count: number, follow: boolean) => post({ t: 'log.view', sid, first: f, count, follow }),
                    searchFor: (q: string, matchCase = false) => post({ t: 'log.search', sid, q, matchCase }),
                    searchStep: (dir: 1 | -1, fromLine?: number) => post({ t: 'log.step', sid, dir, from: fromLine }),
                    setPaused: (paused: boolean) => post({ t: 'log.pause', sid, paused }),
                    clear: () => post({ t: 'log.clear', sid }),
                    close() {
                        if (closed) return;
                        closed = true;
                        logs.delete(sid);
                        post({ t: 'log.close', sid });
                    }
                });
                logs.set(sid, {
                    handle,
                    apply(m) {
                        if (m.lines) {
                            lines = m.lines;
                            from = m.from;
                        }
                        if (m.search) {
                            search.value = m.search;
                            if (m.search.reveal !== undefined) reveal.value = { line: m.search.reveal, seq: ++revealSeq };
                        }
                        if (state.value !== m.state) state.value = m.state;
                        if (m.error && endError.value !== m.error) endError.value = m.error;
                        if (total.value !== m.total) total.value = m.total;
                        if (first.value !== m.first) first.value = m.first;
                        version.value++;
                    }
                });
                return handle;
            };
            logOpens.set(sid, { resolve, reject, make });
            post({ t: 'log.open', sid, params: { ...rest, type: 'logs' }, capacity, maxBytes, multi: isMulti });
        });
    }

    function openFlows(params: FlowOpenOptions): Promise<FlowStreamHandle> {
        const { capacity = 20_000, useWriteContext, ...flows } = params;
        const sid = nextSid++;
        return new Promise<FlowStreamHandle>((resolve, reject) => {
            const make = (usedContext: string): FlowStreamHandle => {
                const state = shallowRef<'live' | 'ended' | 'error'>('live');
                const endError = shallowRef('');
                const total = shallowRef(0);
                const first = shallowRef(0);
                const rate = shallowRef<FlowRate | null>(null);
                const version = shallowRef(0);
                let list: readonly Flow[] = [];
                let from = 0;
                let closed = false;
                const handle: FlowStreamHandle = markRaw({
                    sid,
                    usedContext,
                    state,
                    endError,
                    total,
                    first,
                    rate,
                    version,
                    flowAt(abs: number) {
                        const i = abs - from;
                        return i >= 0 && i < list.length ? list[i] : undefined;
                    },
                    setView: (f: number, count: number, follow: boolean) => post({ t: 'flow.view', sid, first: f, count, follow }),
                    setPaused: (paused: boolean) => post({ t: 'flow.pause', sid, paused }),
                    clear: () => post({ t: 'flow.clear', sid }),
                    close() {
                        if (closed) return;
                        closed = true;
                        flowStreams.delete(sid);
                        post({ t: 'flow.close', sid });
                    }
                });
                flowStreams.set(sid, {
                    apply(m) {
                        if (m.flows) {
                            list = m.flows;
                            from = m.from;
                        }
                        if (m.rate) rate.value = m.rate;
                        if (state.value !== m.state) state.value = m.state;
                        if (m.error && endError.value !== m.error) endError.value = m.error;
                        if (total.value !== m.total) total.value = m.total;
                        if (first.value !== m.first) first.value = m.first;
                        version.value++;
                    }
                });
                return handle;
            };
            flowOpens.set(sid, { resolve, reject, make });
            post({
                t: 'flow.open',
                sid,
                params: { type: 'flows', flows: { cluster: flows.cluster, scope: flows.scope, filter: flows.filter ?? {} }, useWriteContext },
                capacity
            });
        });
    }

    async function openShell(type: string, params: Omit<StreamOpenParams, 'type'>): Promise<ShellHandle> {
        const res = await call<StreamOpenResult>('stream.open', { ...params, type });
        if (!res?.streamId) throw new RpcError('internal', 'stream.open returned no stream id');
        const sock = new WebSocket(
            streamUrlOf(opts.url),
            opts.token ? [Subprotocol, 'token.' + opts.token, 'stream.' + res.streamId] : [Subprotocol, 'stream.' + res.streamId]
        );
        return createShell({ socket: sock as unknown as import('./shell.ts').SocketLike, usedContext: res.usedContext ?? '' });
    }

    async function pickLogFile(filename: string): Promise<LogFileTarget | null | false> {
        const picker = (globalThis as unknown as { showSaveFilePicker?: (o: { suggestedName: string }) => Promise<LogFileTarget> })
            .showSaveFilePicker;
        if (!picker) return null;
        try {
            return await picker({ suggestedName: filename });
        } catch (e) {
            return e instanceof DOMException && e.name === 'AbortError' ? false : null;
        }
    }

    async function downloadLog(params: Omit<StreamOpenParams, 'type' | 'download'>, filename: string, file: LogFileTarget | null): Promise<void> {
        const res = await call<StreamOpenResult>('stream.open', { ...params, type: 'logs', download: true, follow: false });
        if (!res?.downloadUrl) throw new RpcError('internal', 'stream.open returned no download url');
        const url = httpOrigin(opts.url) + res.downloadUrl;
        if (file) {
            const r = await fetch(url, opts.token ? { headers: { Authorization: 'Bearer ' + opts.token } } : { credentials: 'same-origin' });
            if (!r.ok || !r.body) throw new RpcError('unavailable', `download failed (${r.status})`);
            await r.body.pipeTo(await file.createWritable());
            return;
        }
        // The single-use streamId in the URL is the credential: the agent token never goes into a URL.
        const a = document.createElement('a');
        a.href = url;
        a.download = filename;
        document.body.appendChild(a);
        a.click();
        a.remove();
    }

    // The network came back or the tab was foregrounded (laptop wake, Wi-Fi roam): do not wait out the backoff.
    const nudge = () => post({ t: 'nudge' });
    const onVisible = () => document.visibilityState === 'visible' && nudge();
    function removeNudgers() {
        if (typeof window === 'undefined') return;
        window.removeEventListener('online', nudge);
        document.removeEventListener('visibilitychange', onVisible);
    }
    if (typeof window !== 'undefined') {
        window.addEventListener('online', nudge);
        document.addEventListener('visibilitychange', onVisible);
    }

    return new Promise<Agent>((resolve, reject) => {
        onOpen = () => resolve(agent);
        onFail = e => {
            reject(e);
            onFail = null;
        };
        post({ t: 'connect', url: opts.url, token: opts.token });
    });
}

/** Running first, then newest first. */
export function sortJobs(list: JobFrame[]): JobFrame[] {
    return list.sort((a, b) => Number(b.state === 'running') - Number(a.state === 'running') || b.startedAt - a.startedAt);
}

/** `ws://host/ws` → `ws://host/stream`. */
export function streamUrlOf(dataUrl: string): string {
    return dataUrl.replace(/\/ws(\?|#|$)/, '/stream$1');
}

/** `ws://host:port/ws` → `http://host:port`. */
export function httpOrigin(dataUrl: string): string {
    const u = new URL(dataUrl);
    return (u.protocol === 'wss:' ? 'https://' : 'http://') + u.host;
}
