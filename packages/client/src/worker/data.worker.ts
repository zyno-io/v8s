import type { MainToWorker } from '../types.ts';

import { Subprotocol } from '../protocol.gen.ts';
/// <reference lib="webworker" />
import { DataCore } from './core.ts';

let ws: WebSocket | null = null;

const core = new DataCore({
    send: frame => ws?.send(frame as Uint8Array<ArrayBuffer>),
    post: (msg, transfer) => self.postMessage(msg, transfer ?? []),
    connect(url, token) {
        const sock = new WebSocket(url, [Subprotocol, 'token.' + token]);
        ws = sock;
        sock.binaryType = 'arraybuffer';
        sock.onmessage = ev => {
            try {
                core.handleFrame(ev.data as ArrayBuffer);
            } catch (err) {
                console.error('[v8s] frame error', err);
            }
        };
        sock.onclose = ev => core.handleClosed(ev.reason || `closed (${ev.code})`);
        sock.onerror = () => self.postMessage({ t: 'status', status: 'error', message: 'socket error' });
    },
    closeSocket: () => ws?.close(),
    schedule: (fn, ms) => setTimeout(fn, ms),
    cancel: h => clearTimeout(h as ReturnType<typeof setTimeout>),
    now: () => performance.now()
});

self.onmessage = (ev: MessageEvent<MainToWorker>) => core.handleMain(ev.data);
