import { describe, expect, it } from 'vitest';

import { decodeFrame, decodeIds, encodeFrame, encodeIds, HEADER_BYTES, KindAck, KindPatch, KindRequest } from '../src/codec.ts';

describe('frame codec', () => {
    it('writes kind, big-endian channel and seq, then msgpack', () => {
        const f = encodeFrame(KindRequest, 0x01020304, 0x0a0b0c0d, { method: 'clusters.list' });
        expect([...f.subarray(0, HEADER_BYTES)]).toEqual([2, 1, 2, 3, 4, 0x0a, 0x0b, 0x0c, 0x0d]);
        const d = decodeFrame(f);
        expect(d.kind).toBe(KindRequest);
        expect(d.channel).toBe(0x01020304);
        expect(d.seq).toBe(0x0a0b0c0d);
        expect(d.payload).toEqual({ method: 'clusters.list' });
    });

    it('acks carry no payload', () => {
        const f = encodeFrame(KindAck, 0, 77);
        expect(f.length).toBe(HEADER_BYTES);
        expect(decodeFrame(f)).toEqual({ kind: KindAck, channel: 0, seq: 77, payload: undefined });
    });

    it('decodes from an ArrayBuffer and a subarray with a byte offset', () => {
        const f = encodeFrame(KindPatch, 5, 9, { gen: 1, total: 3, offset: 0 });
        expect(decodeFrame(f.buffer as ArrayBuffer).channel).toBe(5);
        const padded = new Uint8Array(f.length + 3);
        padded.set(f, 3);
        expect(decodeFrame(padded.subarray(3)).payload).toEqual({ gen: 1, total: 3, offset: 0 });
    });

    it('rejects truncated frames', () => {
        expect(() => decodeFrame(new Uint8Array(4))).toThrow();
    });

    it('encodes undefined fields as nil rather than the msgpack undefined extension', () => {
        const d = decodeFrame(encodeFrame(KindRequest, 1, 0, { method: 'x', params: { anchorId: undefined } }));
        expect((d.payload as { params: { anchorId: unknown } }).params.anchorId).toBeNull();
    });

    it('decodes ids bin as little-endian u32s, even from unaligned views', () => {
        const ids = [1, 2, 300, 0xffffffff, 70000];
        const bin = encodeIds(ids);
        expect([...decodeIds(bin)]).toEqual(ids);
        const padded = new Uint8Array(bin.length + 1);
        padded.set(bin, 1);
        expect([...decodeIds(padded.subarray(1))]).toEqual(ids);
    });

    it('round-trips ids inside a msgpack bin', () => {
        const f = encodeFrame(KindPatch, 1, 1, { gen: 1, total: 2, offset: 0, ids: encodeIds([10, 11]) });
        const p = decodeFrame(f).payload as { ids: Uint8Array };
        expect([...decodeIds(p.ids)]).toEqual([10, 11]);
    });

    it('decodes uint64 (unix ms from Go) as a number, not a BigInt', () => {
        const ms = 1_700_000_000_000;
        const payload = new Uint8Array(1 + 2 + 1 + 8);
        payload.set([0x81, 0xa1, 0x61, 0xcf]); // {"a": uint64}
        new DataView(payload.buffer).setBigUint64(4, BigInt(ms), false);
        const frame = new Uint8Array(HEADER_BYTES + payload.length);
        frame[0] = KindPatch;
        frame.set(payload, HEADER_BYTES);
        expect(decodeFrame(frame).payload).toEqual({ a: ms });
    });
});
