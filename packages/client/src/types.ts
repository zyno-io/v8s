import type {
    ClusterState,
    Column,
    ErrorBody,
    Flow,
    FlowRate,
    JobFrame,
    ObjectPayload,
    StreamOpenParams,
    ViewQuery,
    Window
} from './protocol.gen.ts';

declare const rawPayload: unique symbol;

/**
 * Brand for agent payload types. Datasets must never be wrapped in ref()/reactive(); the brand makes the
 * intent visible in signatures and lets `NoReactive` reject payloads in helper APIs.
 */
export type RawPayload<T> = T & { readonly [rawPayload]?: never };

/** A cell is a resolved string (dict/str), a number (int/time/tone). */
export type Cell = string | number;

/** `[id, version, ...cells]` in column order; dict cells are already resolved to strings by the worker. */
export type Row = RawPayload<readonly [id: number, version: number, ...cells: Cell[]]>;

/** Helper for APIs that must refuse reactive wrappers: `function f<T>(v: NoReactive<T>)`. */
export type NoReactive<T> = T extends { readonly __v_isRef: true } | { readonly __v_isReactive: true } ? never : T;

export const ROW_ID = 0;
export const ROW_VER = 1;
export const ROW_CELLS = 2;

export interface AgentEndpoint {
    url: string;
    /** The agent token (desktop, serve, fake). Empty in hosted mode, where the session cookie authenticates the socket. */
    token: string;
}

export type ConnectionStatus = 'connecting' | 'open' | 'reconnecting' | 'closed' | 'error';

/** The KindObject payload on the wire: ObjectPayload fields inline (Go embeds the struct) plus `deleted` / `error`. */
export type ObjectFrameWire = ObjectPayload & { deleted?: boolean; error?: ErrorBody };

export type { ClusterState, Column, ViewQuery, Window };

// ---- log streams (owned by the Data Worker) ----

/** One log line ready to paint: ANSI already stripped into `spans` (flat `[start, end, fg, bg, flags]` runs, see logs/ansi.ts). */
export interface LogLineView {
    /** Absolute line number (monotonic; survives the oldest lines being dropped). */
    n: number;
    text: string;
    /** `<pod>/<container>` of a merged multi-pod line. */
    src?: string;
    /** RFC3339 timestamp prefix, split off the text. */
    ts?: string;
    spans?: number[];
    /** 2 = warning, 3 = error, from structured `level=`/JSON lines only. */
    level?: 2 | 3;
    /** `[start, end, ...]` search hits inside `text`. */
    hl?: number[];
}

export interface LogSearchState {
    query: string;
    /** Lines containing the query. */
    count: number;
    /** 1-based index of the current hit, 0 when none. */
    current: number;
    /** Absolute line to scroll to after a next/prev step. */
    reveal?: number;
}

/** One batched post for a log stream: at most one per frame. */
export interface LogUpdate {
    t: 'log';
    sid: number;
    seq: number;
    /** Lines ever appended (absolute index one past the newest). */
    total: number;
    /** Absolute index of the oldest line still held. */
    first: number;
    /** Absolute number of `lines[0]`. */
    from: number;
    /** The viewport, present when it changed or new lines fall inside it. */
    lines?: LogLineView[];
    search?: LogSearchState;
    state: 'live' | 'ended' | 'error';
    error?: string;
}

// ---- flow streams (owned by the Data Worker) ----

/** One batched post for a flows stream: at most one per frame. */
export interface FlowUpdate {
    t: 'flow';
    sid: number;
    seq: number;
    /** Flows ever received (absolute index one past the newest). */
    total: number;
    /** Absolute index of the oldest flow still held. */
    first: number;
    /** Absolute index of `flows[0]`. */
    from: number;
    /** The viewport, present when it changed or new flows fall inside it. */
    flows?: Flow[];
    /** Latest sampling report from the agent, present when it changed. */
    rate?: FlowRate;
    state: 'live' | 'ended' | 'error';
    error?: string;
}

// ---- worker protocol (structured-clone messages) ----

export type MainToWorker =
    | { t: 'connect'; url: string; token: string }
    | { t: 'request'; rid: number; method: string; params?: unknown }
    | { t: 'subscribe'; qid: number; gen: number; query: ViewQuery; window: Window; usage?: boolean }
    | { t: 'update'; qid: number; gen: number; query?: ViewQuery; window?: Window; usage?: boolean }
    | { t: 'unsubscribe'; qid: number }
    | { t: 'log.open'; sid: number; params: StreamOpenParams; capacity: number; maxBytes?: number; multi: boolean }
    | { t: 'log.view'; sid: number; first: number; count: number; follow: boolean }
    | { t: 'log.search'; sid: number; q: string; matchCase: boolean }
    | { t: 'log.step'; sid: number; dir: 1 | -1; from?: number }
    | { t: 'log.pause'; sid: number; paused: boolean }
    | { t: 'log.clear'; sid: number }
    | { t: 'log.close'; sid: number }
    | { t: 'flow.open'; sid: number; params: StreamOpenParams; capacity: number }
    | { t: 'flow.view'; sid: number; first: number; count: number; follow: boolean }
    | { t: 'flow.pause'; sid: number; paused: boolean }
    | { t: 'flow.clear'; sid: number }
    | { t: 'flow.close'; sid: number }
    /** The network is back or the tab is visible again: reconnect now instead of waiting out the backoff. */
    | { t: 'nudge' }
    | { t: 'close' };

export type WorkerToMain =
    | { t: 'status'; status: ConnectionStatus; message?: string; agentVersion?: string; mode?: string }
    | { t: 'response'; rid: number; result?: unknown; error?: ErrorBody }
    /** A KindObject frame for an `object.subscribe` oid (payload as in ObjectFrame; the embedded ObjectPayload is flattened). */
    | { t: 'object'; oid: number; frame: ObjectFrameWire }
    | { t: 'clusterStates'; states: ClusterState[] }
    | { t: 'job'; frame: JobFrame }
    | { t: 'log.opened'; sid: number; usedContext?: string }
    | { t: 'log.error'; sid: number; error: ErrorBody }
    | LogUpdate
    | FlowUpdate
    | { t: 'flow.opened'; sid: number; usedContext?: string }
    | { t: 'flow.error'; sid: number; error: ErrorBody }
    /** `columns: null`: a refused subscribe is being retried (the page shows Loading…). */
    | { t: 'columns'; qid: number; columns: Column[] | null; error?: ErrorBody }
    /** The view was resubscribed on a new connection: wire ids restarted, so ids held by the page no longer mean what they did. Precedes the first rows. */
    | { t: 'reset'; qid: number }
    /** Up to 500 rows; applied immediately without a version bump. */
    | { t: 'rows'; qid: number; gen: number; rows: Row[] }
    /** Last message of a flush: ids/total/offset/removed, then the main thread bumps the view version. */
    | { t: 'commit'; qid: number; gen: number; total: number; offset: number; ids?: Uint32Array; removed?: number[] };
