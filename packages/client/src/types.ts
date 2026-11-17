import type { ClusterState, Column, ErrorBody, ObjectPayload, ViewQuery, Window } from './protocol.gen.ts';

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
    token: string;
}

export type ConnectionStatus = 'connecting' | 'open' | 'closed' | 'error';

/** The KindObject payload on the wire: ObjectPayload fields inline (Go embeds the struct) plus `deleted` / `error`. */
export type ObjectFrameWire = ObjectPayload & { deleted?: boolean; error?: ErrorBody };

export type { ClusterState, Column, ViewQuery, Window };

// ---- worker protocol (structured-clone messages) ----

export type MainToWorker =
    | { t: 'connect'; url: string; token: string }
    | { t: 'request'; rid: number; method: string; params?: unknown }
    | { t: 'subscribe'; qid: number; gen: number; query: ViewQuery; window: Window }
    | { t: 'update'; qid: number; gen: number; query?: ViewQuery; window?: Window }
    | { t: 'unsubscribe'; qid: number }
    | { t: 'close' };

export type WorkerToMain =
    | { t: 'status'; status: ConnectionStatus; message?: string; agentVersion?: string; mode?: string }
    | { t: 'response'; rid: number; result?: unknown; error?: ErrorBody }
    /** A KindObject frame for an `object.subscribe` oid (payload as in ObjectFrame; the embedded ObjectPayload is flattened). */
    | { t: 'object'; oid: number; frame: ObjectFrameWire }
    | { t: 'clusterStates'; states: ClusterState[] }
    | { t: 'columns'; qid: number; columns: Column[] }
    /** Up to 500 rows; applied immediately without a version bump. */
    | { t: 'rows'; qid: number; gen: number; rows: Row[] }
    /** Last message of a flush: ids/total/offset/removed, then the main thread bumps the view version. */
    | { t: 'commit'; qid: number; gen: number; total: number; offset: number; ids?: Uint32Array; removed?: number[] };
