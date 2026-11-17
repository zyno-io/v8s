import type { ShallowRef } from 'vue';

import { shallowRef } from 'vue';

import type { StreamReady } from './protocol.gen.ts';

import { StreamChanStderr, StreamChanStdin, StreamChanStdout } from './protocol.gen.ts';

export type ShellState = 'connecting' | 'ready' | 'exited';

export interface ShellExit {
    code: number;
    error?: string;
}

/** An interactive stream (exec, debug container, node shell). Output arrives batched per animation frame. */
export interface ShellHandle {
    readonly usedContext: string;
    readonly state: ShallowRef<ShellState>;
    readonly ready: ShallowRef<StreamReady | null>;
    readonly exit: ShallowRef<ShellExit | null>;
    /** Set by the terminal: called at most once per frame with merged output bytes (`stderr` only for non-TTY streams). */
    onData: ((chunk: Uint8Array, stderr: boolean) => void) | null;
    /** Keystrokes go out immediately, unbatched. */
    write(data: string | Uint8Array): void;
    resize(cols: number, rows: number): void;
    close(): void;
}

/** The slice of WebSocket the channel needs (a fake in tests). */
export interface SocketLike {
    binaryType: string;
    readyState: number;
    send(data: string | Uint8Array<ArrayBuffer>): void;
    close(): void;
    onopen: (() => void) | null;
    onmessage: ((ev: { data: unknown }) => void) | null;
    onclose: ((ev: { reason?: string; code?: number }) => void) | null;
    onerror: (() => void) | null;
}

const OPEN = 1;
const encoder = new TextEncoder();

/** Largest stdin frame body the agent accepts (docs/protocol.md): 64 KiB of data, plus the channel byte. */
export const MAX_STDIN_FRAME = 64 * 1024;

/** `[0x00][stdin bytes]`. */
export function encodeStdin(data: string | Uint8Array): Uint8Array<ArrayBuffer> {
    const body = typeof data === 'string' ? encoder.encode(data) : data;
    const out = new Uint8Array(body.length + 1);
    out[0] = StreamChanStdin;
    out.set(body, 1);
    return out;
}

export function encodeResize(cols: number, rows: number): string {
    return JSON.stringify({ resize: { cols: Math.max(1, Math.floor(cols)), rows: Math.max(1, Math.floor(rows)) } });
}

export interface InteractiveFrame {
    stderr: boolean;
    data: Uint8Array;
}

/** `[0x01][stdout]` / `[0x02][stderr]`; anything else is ignored. */
export function decodeInteractive(bytes: Uint8Array): InteractiveFrame | null {
    if (bytes.length < 1) return null;
    if (bytes[0] === StreamChanStdout) return { stderr: false, data: bytes.subarray(1) };
    if (bytes[0] === StreamChanStderr) return { stderr: true, data: bytes.subarray(1) };
    return null;
}

export interface ShellChannelOptions {
    socket: SocketLike;
    usedContext: string;
    /** Frame scheduler (requestAnimationFrame in the browser). */
    raf?: (fn: () => void) => unknown;
}

/** Wraps one `/stream` socket for an interactive session: framing, batching, resize and exit handling. */
export function createShell(o: ShellChannelOptions): ShellHandle {
    const sock = o.socket;
    const raf = o.raf ?? ((fn: () => void) => (typeof requestAnimationFrame === 'function' ? requestAnimationFrame(fn) : setTimeout(fn, 16)));
    const state = shallowRef<ShellState>('connecting');
    const ready = shallowRef<StreamReady | null>(null);
    const exit = shallowRef<ShellExit | null>(null);
    let chunks: Uint8Array[] = [];
    let chunkErr = false;
    let queued = false;
    let closed = false;
    let sink: ((chunk: Uint8Array, stderr: boolean) => void) | null = null;
    /** Sent before the socket opened or before `ready`: only resize is held back, stdin is sent as soon as the socket allows. */
    let pendingResize: string | null = null;

    const flush = () => {
        queued = false;
        if (!chunks.length) return;
        let n = 0;
        for (const c of chunks) n += c.length;
        const merged = new Uint8Array(n);
        let p = 0;
        for (const c of chunks) {
            merged.set(c, p);
            p += c.length;
        }
        const err = chunkErr;
        // No terminal attached yet (its chunk is still loading): keep the output and replay it when `onData` is set.
        if (!sink) {
            chunks = [merged];
            return;
        }
        chunks = [];
        sink(merged, err);
    };
    const enqueue = (data: Uint8Array, stderr: boolean) => {
        if (chunks.length && chunkErr !== stderr) flush();
        chunkErr = stderr;
        chunks.push(data);
        if (!queued) {
            queued = true;
            raf(flush);
        }
    };

    const handle: ShellHandle = {
        usedContext: o.usedContext,
        state,
        ready,
        exit,
        get onData() {
            return sink;
        },
        set onData(fn) {
            sink = fn;
            if (fn && chunks.length) flush();
        },
        write(data) {
            if (closed || sock.readyState !== OPEN) return;
            const body = typeof data === 'string' ? encoder.encode(data) : data;
            // A large paste goes out as several frames, in order; each is at most MAX_STDIN_FRAME bytes of data.
            for (let i = 0; i < body.length; i += MAX_STDIN_FRAME) sock.send(encodeStdin(body.subarray(i, i + MAX_STDIN_FRAME)));
        },
        resize(cols, rows) {
            const msg = encodeResize(cols, rows);
            if (closed) return;
            if (sock.readyState !== OPEN) pendingResize = msg;
            else sock.send(msg);
        },
        close() {
            if (closed) return;
            closed = true;
            sock.close();
            if (state.value !== 'exited') {
                state.value = 'exited';
                exit.value = exit.value ?? { code: 0 };
            }
        }
    };

    sock.binaryType = 'arraybuffer';
    sock.onopen = () => {
        if (pendingResize) {
            sock.send(pendingResize);
            pendingResize = null;
        }
    };
    sock.onmessage = ev => {
        const d = ev.data;
        if (typeof d === 'string') {
            try {
                const m = JSON.parse(d) as { ready?: StreamReady; exit?: number; error?: string };
                if (m.ready) {
                    ready.value = m.ready;
                    state.value = 'ready';
                } else if (m.exit !== undefined || m.error) {
                    flush();
                    exit.value = { code: m.exit ?? 1, error: m.error };
                    state.value = 'exited';
                }
            } catch {
                // ignore malformed control frames
            }
            return;
        }
        const f = decodeInteractive(d instanceof Uint8Array ? d : new Uint8Array(d as ArrayBuffer));
        if (f && f.data.length) enqueue(f.data, f.stderr);
    };
    sock.onclose = ev => {
        closed = true;
        flush();
        if (state.value !== 'exited') {
            state.value = 'exited';
            exit.value = { code: 1, error: ev.reason || 'connection closed' };
        }
    };
    sock.onerror = () => {};
    return handle;
}
