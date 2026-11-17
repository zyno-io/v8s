import { Packr, Unpackr } from 'msgpackr';

import type { FrameKind } from './protocol.gen.ts';

import { KindAck, KindClusterState, KindDict, KindHello, KindJob, KindObject, KindPatch, KindRequest, KindResponse } from './protocol.gen.ts';

export { KindAck, KindClusterState, KindDict, KindHello, KindJob, KindObject, KindPatch, KindRequest, KindResponse };

export const HEADER_BYTES = 9;

// int64AsType: unix-ms timestamps are uint64 on the wire; the default decodes them to BigInt, which poisons arithmetic.
const unpackr = new Unpackr({ useRecords: false, mapsAsObjects: true, int64AsType: 'number' });
const packr = new Packr({ useRecords: false, mapsAsObjects: true, encodeUndefinedAsNil: true });

export interface Frame {
    kind: FrameKind;
    channel: number;
    seq: number;
    /** Decoded msgpack payload; undefined when the frame has none. */
    payload: unknown;
}

/** Builds `[u8 kind][u32 channel BE][u32 seq BE][msgpack payload]`. */
export function encodeFrame(kind: FrameKind, channel: number, seq: number, payload?: unknown): Uint8Array {
    const body = payload === undefined ? null : packr.pack(payload);
    const out = new Uint8Array(HEADER_BYTES + (body ? body.length : 0));
    const view = new DataView(out.buffer);
    out[0] = kind;
    view.setUint32(1, channel >>> 0, false);
    view.setUint32(5, seq >>> 0, false);
    if (body) out.set(body, HEADER_BYTES);
    return out;
}

/** Splits the header and decodes the payload (if any). Throws on truncated frames. */
export function decodeFrame(data: ArrayBuffer | Uint8Array): Frame {
    const bytes = data instanceof Uint8Array ? data : new Uint8Array(data);
    if (bytes.length < HEADER_BYTES) throw new Error(`frame too short: ${bytes.length} bytes`);
    const view = new DataView(bytes.buffer, bytes.byteOffset, bytes.byteLength);
    const kind = bytes[0];
    const channel = view.getUint32(1, false);
    const seq = view.getUint32(5, false);
    const payload = bytes.length > HEADER_BYTES ? unpackr.unpack(bytes.subarray(HEADER_BYTES)) : undefined;
    return { kind, channel, seq, payload };
}

const HOST_LITTLE_ENDIAN = new Uint8Array(new Uint16Array([1]).buffer)[0] === 1;

/** Converts the wire `ids` bin (little-endian u32s) into a freshly allocated, aligned Uint32Array. */
export function decodeIds(bin: Uint8Array): Uint32Array {
    const n = bin.length >>> 2;
    if (HOST_LITTLE_ENDIAN) {
        // slice() copies into a fresh ArrayBuffer, which also fixes Buffer/subarray alignment.
        return new Uint32Array(bin.slice(0, n << 2).buffer);
    }
    const out = new Uint32Array(n);
    const view = new DataView(bin.buffer, bin.byteOffset, bin.byteLength);
    for (let i = 0; i < n; i++) out[i] = view.getUint32(i << 2, true);
    return out;
}

/** Inverse of decodeIds; used by tests and fakes. */
export function encodeIds(ids: ArrayLike<number>): Uint8Array {
    const out = new Uint8Array(ids.length * 4);
    const view = new DataView(out.buffer);
    for (let i = 0; i < ids.length; i++) view.setUint32(i * 4, ids[i] >>> 0, true);
    return out;
}
