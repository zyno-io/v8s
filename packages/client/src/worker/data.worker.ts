import type { MainToWorker } from '../types.ts';

import { Subprotocol } from '../protocol.gen.ts';
/// <reference lib="webworker" />
import { DataCore } from './core.ts';

let ws: WebSocket | null = null;
let endpointUrl = '';
const streams = new Map<number, WebSocket>();
/** Control texts sent before a stream socket finished opening (a flows pause right after `flow.opened`); flushed on open. */
const queued = new Map<number, string[]>();

const core = new DataCore({
    send: frame => ws?.send(frame as Uint8Array<ArrayBuffer>),
    post: (msg, transfer) => self.postMessage(msg, transfer ?? []),
    connect(url, token) {
        endpointUrl = url;
        const sock = new WebSocket(url, token ? [Subprotocol, 'token.' + token] : [Subprotocol]);
        ws = sock;
        sock.binaryType = 'arraybuffer';
        sock.onmessage = ev => {
            if (ws !== sock) return;
            try {
                core.handleFrame(ev.data as ArrayBuffer);
            } catch (err) {
                console.error('[v8s] frame error', err);
            }
        };
        sock.onclose = ev => {
            if (ws !== sock) return;
            ws = null;
            core.handleClosed(ev.reason || `closed (${ev.code})`);
        };
        // A failed socket always ends in `close`, which the core turns into closed (first connect) or a reconnect.
        sock.onerror = () => {};
    },
    closeSocket: () => ws?.close(),
    connectStream(sid, url, protocols) {
        const sock = new WebSocket(url, protocols);
        sock.binaryType = 'arraybuffer';
        streams.set(sid, sock);
        sock.onopen = () => {
            for (const t of queued.get(sid) ?? []) sock.send(t);
            queued.delete(sid);
        };
        sock.onmessage = ev => {
            if (typeof ev.data === 'string') core.handleStreamText(sid, ev.data);
            else core.handleStreamBinary(sid, new Uint8Array(ev.data as ArrayBuffer));
        };
        sock.onclose = ev => {
            streams.delete(sid);
            queued.delete(sid);
            core.handleStreamClosed(sid, ev.reason || '');
        };
        sock.onerror = () => {};
    },
    sendStream: (sid, text) => {
        const s = streams.get(sid);
        if (!s) return;
        if (s.readyState === WebSocket.OPEN) s.send(text);
        else if (s.readyState === WebSocket.CONNECTING) queued.set(sid, [...(queued.get(sid) ?? []), text]);
    },
    closeStream(sid) {
        streams.get(sid)?.close();
        streams.delete(sid);
        queued.delete(sid);
    },
    schedule: (fn, ms) => setTimeout(fn, ms),
    cancel: h => clearTimeout(h as ReturnType<typeof setTimeout>),
    now: () => performance.now(),
    // Hosted only: the upgrade's 401 is hidden from scripts, so ask `/api/me` (same origin as the socket, cookie included).
    async sessionEnded() {
        try {
            const u = new URL(endpointUrl);
            u.protocol = u.protocol === 'wss:' ? 'https:' : 'http:';
            u.pathname = '/api/me';
            u.search = '';
            return (await fetch(u, { credentials: 'same-origin' })).status === 401;
        } catch {
            return false;
        }
    }
});

self.onmessage = (ev: MessageEvent<MainToWorker>) => core.handleMain(ev.data);
