import type { ShallowRef } from 'vue';

import { markRaw, shallowRef } from 'vue';

import type { ClustersListResult, ClusterStates, ErrorBody, KindsListResult, ObjectPayload, ObjectRef } from './protocol.gen.ts';
import type {
    AgentEndpoint,
    ClusterState,
    Column,
    ConnectionStatus,
    MainToWorker,
    ObjectFrameWire,
    Row,
    ViewQuery,
    Window,
    WorkerToMain
} from './types.ts';

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
    readonly gen: number;
    readonly query: ViewQuery;
    readonly window: Window;
    update(patch: { query?: ViewQuery; window?: Window }): void;
    close(): void;
}

/** A live single-object subscription (`object.subscribe`); frames arrive on `onFrame` until `close()`. */
export interface ObjectSubscription {
    readonly oid: number;
    close(): void;
}

export interface Agent {
    readonly status: ShallowRef<ConnectionStatus>;
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
        readonly live?: ObjectPayload
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
    const agentInfo = shallowRef<{ version: string; mode: string } | null>(null);
    const clusterStates = shallowRef<ClusterState[]>([]);
    const calls = new Map<number, { resolve: (v: unknown) => void; reject: (e: Error) => void }>();
    const views = new Map<number, InternalView>();
    const objects = new Map<number, (frame: ObjectFrameWire) => void>();
    let nextRid = 1;
    let nextQid = 1;
    let nextOid = 1;

    interface InternalView extends ViewHandle {
        gen: number;
        query: ViewQuery;
        window: Window;
        ids: Uint32Array;
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
                    onOpen?.();
                } else if (m.status === 'error' || m.status === 'closed') {
                    onFail?.(new Error(m.message ?? 'connection failed'));
                    for (const [, c] of calls) c.reject(new RpcError('unavailable', m.message ?? 'connection closed'));
                    calls.clear();
                }
                break;
            case 'response': {
                const c = calls.get(m.rid);
                if (!c) break;
                calls.delete(m.rid);
                if (m.error) c.reject(new RpcError(m.error.code, m.error.message, m.error.live));
                else c.resolve(m.result);
                break;
            }
            case 'clusterStates':
                clusterStates.value = m.states;
                break;
            case 'object':
                objects.get(m.oid)?.(m.frame);
                break;
            case 'columns': {
                const v = views.get(m.qid);
                if (v) v.columns.value = m.columns;
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
            try {
                await call('object.subscribe', { oid, ref });
            } catch (e) {
                objects.delete(oid);
                throw e;
            }
            return {
                oid,
                close() {
                    if (!objects.delete(oid)) return;
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
        close() {
            post({ t: 'close' });
            worker.terminate();
            status.value = 'closed';
        }
    };

    return new Promise<Agent>((resolve, reject) => {
        onOpen = () => resolve(agent);
        onFail = e => {
            reject(e);
            onFail = null;
        };
        post({ t: 'connect', url: opts.url, token: opts.token });
    });
}
