import type { MainToWorker } from '../types.ts';

import { Subprotocol } from '../protocol.gen.ts';
/// <reference lib="webworker" />
import { DataCore } from './core.ts';

let ws: WebSocket | null = null;
const streams = new Map<number, WebSocket>();

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
    connectStream(sid, url, protocols) {
        const sock = new WebSocket(url, protocols);
        sock.binaryType = 'arraybuffer';
        streams.set(sid, sock);
        sock.onmessage = ev => {
            if (typeof ev.data === 'string') core.handleStreamText(sid, ev.data);
            else core.handleStreamBinary(sid, new Uint8Array(ev.data as ArrayBuffer));
        };
        sock.onclose = ev => {
            streams.delete(sid);
            core.handleStreamClosed(sid, ev.reason || '');
        };
        sock.onerror = () => {};
    },
    sendStream: (sid, text) => {
        const s = streams.get(sid);
        if (s && s.readyState === WebSocket.OPEN) s.send(text);
    },
    closeStream(sid) {
        streams.get(sid)?.close();
        streams.delete(sid);
    },
    schedule: (fn, ms) => setTimeout(fn, ms),
    cancel: h => clearTimeout(h as ReturnType<typeof setTimeout>),
    now: () => performance.now()
});

self.onmessage = (ev: MessageEvent<MainToWorker>) => core.handleMain(ev.data);
