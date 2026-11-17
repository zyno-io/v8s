import { describe, expect, it } from 'vitest';

import type { SocketLike } from '../src/shell.ts';

import { createShell, decodeInteractive, encodeResize, encodeStdin } from '../src/shell.ts';

function fakeSocket() {
    const sent: (string | Uint8Array)[] = [];
    const s: SocketLike & { open(): void; msg(d: unknown): void; closeFromServer(reason?: string): void } = {
        binaryType: '',
        readyState: 0,
        send: d => void sent.push(d),
        close() {
            s.readyState = 3;
        },
        onopen: null,
        onmessage: null,
        onclose: null,
        onerror: null,
        open() {
            s.readyState = 1;
            s.onopen?.();
        },
        msg(d) {
            s.onmessage?.({ data: d });
        },
        closeFromServer(reason) {
            s.readyState = 3;
            s.onclose?.({ reason });
        }
    };
    return { s, sent };
}

describe('interactive frames', () => {
    it('encodes stdin and resize, decodes stdout/stderr', () => {
        expect([...encodeStdin('hi')]).toEqual([0, 104, 105]);
        expect(JSON.parse(encodeResize(80.7, 24))).toEqual({ resize: { cols: 80, rows: 24 } });
        expect(decodeInteractive(new Uint8Array([1, 65]))).toEqual({ stderr: false, data: new Uint8Array([65]) });
        expect(decodeInteractive(new Uint8Array([2, 66]))?.stderr).toBe(true);
        expect(decodeInteractive(new Uint8Array([9, 1]))).toBeNull();
    });
});

describe('createShell', () => {
    it('holds the first resize until the socket opens, sends stdin unbatched', () => {
        const { s, sent } = fakeSocket();
        const sh = createShell({ socket: s, usedContext: 'ctx', raf: fn => fn() });
        sh.resize(100, 30);
        sh.write('ls');
        expect(sent).toEqual([]);
        s.open();
        expect(sent).toEqual([encodeResize(100, 30)]);
        sh.write('ls\r');
        expect([...(sent[1] as Uint8Array)]).toEqual([0, 108, 115, 13]);
    });

    it('splits a large paste into frames of at most 64 KiB, in order', () => {
        const { s, sent } = fakeSocket();
        const sh = createShell({ socket: s, usedContext: 'ctx', raf: fn => fn() });
        s.open();
        const big = new Uint8Array(64 * 1024 * 2 + 10).map((_, i) => i % 251);
        sh.write(big);
        expect(sent.map(f => (f as Uint8Array).length - 1)).toEqual([65536, 65536, 10]);
        const joined = sent.flatMap(f => [...(f as Uint8Array).subarray(1)]);
        expect(joined).toEqual([...big]);
        expect((sent[0] as Uint8Array)[0]).toBe(0);
        sent.length = 0;
        sh.write('');
        expect(sent).toEqual([]);
    });

    it('goes ready on the ready frame and batches output once per frame', () => {
        const { s } = fakeSocket();
        const frames: (() => void)[] = [];
        const sh = createShell({ socket: s, usedContext: 'ctx', raf: fn => frames.push(fn) });
        const got: string[] = [];
        sh.onData = (c, err) => got.push((err ? 'E:' : '') + new TextDecoder().decode(c));
        s.open();
        s.msg(JSON.stringify({ ready: { usedContext: 'ctx', shell: '/bin/sh' } }));
        expect(sh.state.value).toBe('ready');
        expect(sh.ready.value?.shell).toBe('/bin/sh');
        s.msg(new Uint8Array([1, 97]).buffer);
        s.msg(new Uint8Array([1, 98]).buffer);
        expect(got).toEqual([]);
        expect(frames.length).toBe(1);
        frames[0]();
        expect(got).toEqual(['ab']);
        s.msg(new Uint8Array([2, 99]).buffer);
        frames[1]();
        expect(got).toEqual(['ab', 'E:c']);
    });

    it('handles the exit frame, flushing output first', () => {
        const { s } = fakeSocket();
        const sh = createShell({ socket: s, usedContext: '', raf: () => 0 });
        const got: string[] = [];
        sh.onData = c => got.push(new TextDecoder().decode(c));
        s.msg(new Uint8Array([1, 120]).buffer);
        s.msg(JSON.stringify({ exit: 3, error: 'container restarted' }));
        expect(got).toEqual(['x']);
        expect(sh.state.value).toBe('exited');
        expect(sh.exit.value).toEqual({ code: 3, error: 'container restarted' });
    });

    it('an unexpected close is an exited session with an error', () => {
        const { s } = fakeSocket();
        const sh = createShell({ socket: s, usedContext: '' });
        s.open();
        s.closeFromServer();
        expect(sh.state.value).toBe('exited');
        expect(sh.exit.value?.error).toBe('connection closed');
    });
});

describe('createShell output before a terminal attaches', () => {
    it('replays buffered output when onData is set', () => {
        const { s } = fakeSocket();
        const sh = createShell({ socket: s, usedContext: '', raf: fn => fn() });
        s.msg(new Uint8Array([1, 104, 105]).buffer);
        const got: string[] = [];
        sh.onData = c => got.push(new TextDecoder().decode(c));
        expect(got).toEqual(['hi']);
    });
});
