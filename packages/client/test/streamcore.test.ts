import { describe, expect, it } from 'vitest';

import type { WorkerToMain } from '../src/types.ts';
import type { CoreIO } from '../src/worker/core.ts';

import { decodeFrame, encodeFrame, KindHello, KindJob, KindResponse } from '../src/codec.ts';
import { DataCore, streamUrl } from '../src/worker/core.ts';

function setup(token = 'tok') {
    const sent: ReturnType<typeof decodeFrame>[] = [];
    const posted: WorkerToMain[] = [];
    const streams: { sid: number; url: string; protocols: string[] }[] = [];
    const streamSent: string[] = [];
    const io: CoreIO = {
        send: f => sent.push(decodeFrame(f)),
        post: m => posted.push(m),
        connect: () => {},
        closeSocket: () => {},
        connectStream: (sid, url, protocols) => streams.push({ sid, url, protocols }),
        sendStream: (_s, t) => streamSent.push(t),
        closeStream: () => {},
        schedule: () => 1,
        cancel: () => {},
        now: () => 0
    };
    const core = new DataCore(io);
    core.handleMain({ t: 'connect', url: 'ws://127.0.0.1:7931/ws', token });
    core.handleFrame(encodeFrame(KindHello, 0, 0, { version: 1, agentVersion: 'x', mode: 'fake' }));
    return { core, sent, posted, streams, streamSent };
}

describe('DataCore streams and jobs', () => {
    it('derives the stream url', () => {
        expect(streamUrl('ws://h:1/ws')).toBe('ws://h:1/stream');
    });

    it('opens a log stream: stream.open, then the /stream socket with token and stream subprotocols', () => {
        const h = setup();
        h.core.handleMain({
            t: 'log.open',
            sid: 7,
            params: { type: 'logs', ref: { cluster: 'c', gvr: 'v1/pods', namespace: 'ns', name: 'p' }, container: 'app', follow: true },
            capacity: 100,
            multi: false
        });
        const req = h.sent.find(f => (f.payload as { method?: string }).method === 'stream.open')!;
        expect((req.payload as { params: { type: string; follow: boolean } }).params).toMatchObject({ type: 'logs', follow: true });
        h.core.handleFrame(encodeFrame(KindResponse, req.channel, 0, { result: { streamId: 'abc', usedContext: 'ctx' } }));
        expect(h.streams).toEqual([{ sid: 7, url: 'ws://127.0.0.1:7931/stream', protocols: ['v8s.v1', 'token.tok', 'stream.abc'] }]);
        expect(h.posted.find(m => m.t === 'log.opened')).toMatchObject({ sid: 7, usedContext: 'ctx' });
        h.core.handleStreamBinary(7, new TextEncoder().encode('hello\n'));
        expect(h.core.logSession(7)!.buf.total).toBe(1);
    });

    it('relays a refused stream.open as log.error with the code', () => {
        const h = setup();
        h.core.handleMain({ t: 'log.open', sid: 1, params: { type: 'logs' }, capacity: 10, multi: true });
        const req = h.sent.find(f => (f.payload as { method?: string }).method === 'stream.open')!;
        h.core.handleFrame(encodeFrame(KindResponse, req.channel, 0, { error: { code: 'write_confirm', message: 'denied' } }));
        expect(h.posted.find(m => m.t === 'log.error')).toMatchObject({ sid: 1, error: { code: 'write_confirm' } });
        expect(h.streams.length).toBe(0);
    });

    it('a close before the open answers does not leak a socket', () => {
        const h = setup();
        h.core.handleMain({ t: 'log.open', sid: 2, params: { type: 'logs' }, capacity: 10, multi: true });
        h.core.handleMain({ t: 'log.close', sid: 2 });
        const req = h.sent.find(f => (f.payload as { method?: string }).method === 'stream.open')!;
        h.core.handleFrame(encodeFrame(KindResponse, req.channel, 0, { result: { streamId: 'z' } }));
        expect(h.streams.length).toBe(0);
    });

    it('a socket drop settles a pending log open with log.error unavailable (not a cancelled one)', () => {
        const h = setup();
        h.core.handleMain({ t: 'log.open', sid: 3, params: { type: 'logs' }, capacity: 10, multi: true });
        h.core.handleMain({ t: 'log.open', sid: 4, params: { type: 'logs' }, capacity: 10, multi: true });
        h.core.handleMain({ t: 'log.close', sid: 4 });
        h.core.handleClosed('gone');
        const errs = h.posted.filter(m => m.t === 'log.error');
        expect(errs).toHaveLength(1);
        expect(errs[0]).toMatchObject({ sid: 3, error: { code: 'unavailable' } });
    });

    it('opens a flows stream on its own socket key and routes text frames to the flow session', () => {
        const h = setup();
        h.core.handleMain({
            t: 'flow.open',
            sid: 5,
            params: { type: 'flows', flows: { cluster: 'c', scope: { namespace: 'ns' }, filter: { verdict: 'DROPPED' } } },
            capacity: 50
        });
        const req = h.sent.find(f => (f.payload as { method?: string }).method === 'stream.open')!;
        expect((req.payload as { params: { type: string } }).params.type).toBe('flows');
        h.core.handleFrame(encodeFrame(KindResponse, req.channel, 0, { result: { streamId: 'fl1', usedContext: 'ctx' } }));
        expect(h.streams).toEqual([{ sid: -5, url: 'ws://127.0.0.1:7931/stream', protocols: ['v8s.v1', 'token.tok', 'stream.fl1'] }]);
        expect(h.posted.find(m => m.t === 'flow.opened')).toMatchObject({ sid: 5, usedContext: 'ctx' });
        h.core.handleStreamText(-5, JSON.stringify({ flows: [{ time: 1, verdict: 'DROPPED', src: {}, dst: {} }] }));
        expect(h.core.flowSession(5)!.total).toBe(1);
        // A log stream with the same number is a different session.
        expect(h.core.logSession(5)).toBeUndefined();
    });

    it('a refused flows open is relayed as flow.error (an unscoped follow is bad_request)', () => {
        const h = setup();
        h.core.handleMain({ t: 'flow.open', sid: 1, params: { type: 'flows' }, capacity: 10 });
        const req = h.sent.find(f => (f.payload as { method?: string }).method === 'stream.open')!;
        h.core.handleFrame(encodeFrame(KindResponse, req.channel, 0, { error: { code: 'bad_request', message: 'scope required' } }));
        expect(h.posted.find(m => m.t === 'flow.error')).toMatchObject({ sid: 1, error: { code: 'bad_request' } });
        expect(h.streams.length).toBe(0);
    });

    it('hosted mode (no token) offers only v8s.v1 and stream.<id>', () => {
        const h = setup('');
        h.core.handleMain({ t: 'log.open', sid: 9, params: { type: 'logs' }, capacity: 10, multi: true });
        const req = h.sent.find(f => (f.payload as { method?: string }).method === 'stream.open')!;
        h.core.handleFrame(encodeFrame(KindResponse, req.channel, 0, { result: { streamId: 'h1' } }));
        expect(h.streams[0].protocols).toEqual(['v8s.v1', 'stream.h1']);
    });

    it('forwards Job frames', () => {
        const h = setup();
        h.core.handleFrame(encodeFrame(KindJob, 0, 0, { jobId: 'j1', type: 'drain', state: 'running', startedAt: 1, progress: [] }));
        expect(h.posted.find(m => m.t === 'job')).toMatchObject({ frame: { jobId: 'j1', state: 'running' } });
    });
});
