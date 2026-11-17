import type { LogLineView, LogSearchState, LogUpdate } from '../types.ts';

import { detectLevel, parseAnsi, splitLine } from './ansi.ts';
import { encodeQuery, LogBuffer } from './buffer.ts';
import { CreditGate } from './credit.ts';

export const LOG_FLUSH_MS = 16;
/** Hard cap on lines in one posted window (the main thread asks for visible + overscan, at most this). */
export const MAX_VIEW_LINES = 400;

export interface LogSessionIO {
    /** Sends a text frame on the stream socket. */
    send(text: string): void;
    closeSocket(): void;
    post(msg: LogUpdate): void;
    schedule(fn: () => void, ms: number): unknown;
    cancel(handle: unknown): void;
    now(): number;
}

export interface LogSessionOptions {
    sid: number;
    capacity: number;
    /** Merged stream: lines are prefixed `<pod>/<container>\t`. */
    multi: boolean;
}

interface View {
    first: number;
    count: number;
    follow: boolean;
}

/**
 * One log stream as the Data Worker owns it: socket frames in, ring buffer, credit grants out, search, and at most one batched
 * post per frame carrying counters plus the lines of the viewport the main thread asked for (already ANSI-parsed).
 */
export class LogSession {
    readonly buf: LogBuffer;
    private credit = new CreditGate();
    private view: View = { first: 0, count: 0, follow: true };
    private timer: unknown = null;
    private lastFlush = -Infinity;
    private viewDirty = true;
    private lastPostedTotal = -1;
    private lastPostedFirst = -1;
    private state: 'live' | 'ended' | 'error' = 'live';
    private endError = '';
    private statePosted = false;
    private seq = 0;
    private closed = false;

    private query: Uint8Array | null = null;
    private queryText = '';
    private matchCase = false;
    private matches: number[] = [];
    private matchHead = 0;
    private searchedTo = 0;
    private cursor = -1;
    private searchDirty = false;
    private reveal = -1;

    constructor(
        private readonly o: LogSessionOptions,
        private readonly io: LogSessionIO
    ) {
        this.buf = new LogBuffer({ capacity: o.capacity });
    }

    // ---- socket side ----

    onBinary(bytes: Uint8Array): void {
        this.credit.received(bytes.length);
        const added = this.buf.append(bytes);
        const grant = this.credit.drained(bytes.length);
        if (grant > 0) this.io.send(JSON.stringify({ credit: grant }));
        if (added > 0 && this.query) this.scanNew();
        this.schedule();
    }

    onText(text: string): void {
        try {
            const m = JSON.parse(text) as { end?: boolean; error?: string };
            if (m.end) this.finish(m.error ? 'error' : 'ended', m.error ?? '');
        } catch {
            // not a control frame we know
        }
    }

    onClose(reason: string): void {
        if (this.state === 'live') this.finish(reason && reason !== 'closed' ? 'error' : 'ended', reason);
    }

    private finish(state: 'ended' | 'error', error: string): void {
        this.state = state;
        this.endError = error;
        this.statePosted = false;
        this.schedule();
    }

    // ---- main thread side ----

    setView(first: number, count: number, follow: boolean): void {
        this.view = { first, count: Math.min(count, MAX_VIEW_LINES), follow };
        this.viewDirty = true;
        this.schedule();
    }

    setPaused(paused: boolean): void {
        const grant = this.credit.setPaused(paused);
        if (grant > 0 && !this.closed) this.io.send(JSON.stringify({ credit: grant }));
    }

    clear(): void {
        this.buf.clear();
        this.matches = [];
        this.matchHead = 0;
        this.searchedTo = this.buf.total;
        this.cursor = -1;
        this.searchDirty = true;
        this.viewDirty = true;
        this.schedule();
    }

    search(q: string, matchCase: boolean): void {
        this.queryText = q;
        this.matchCase = matchCase;
        this.query = q ? encodeQuery(q) : null;
        this.matches = [];
        this.matchHead = 0;
        this.cursor = -1;
        this.searchedTo = this.buf.first;
        this.reveal = -1;
        if (this.query) this.scanNew();
        this.searchDirty = true;
        this.viewDirty = true;
        this.schedule();
    }

    /** Moves to the next/previous hit relative to the current one (or to the first visible line when none is current). */
    searchStep(dir: 1 | -1, fromLine?: number): void {
        this.trimMatches();
        const n = this.matches.length - this.matchHead;
        if (!n) return;
        let idx: number;
        if (this.cursor < 0 || this.cursor >= n) {
            const from = fromLine ?? this.view.first;
            idx = dir === 1 ? this.lowerBound(from) : this.lowerBound(from) - 1;
            if (dir === 1 && idx >= n) idx = 0;
            if (dir === -1 && idx < 0) idx = n - 1;
        } else {
            idx = (this.cursor + dir + n) % n;
        }
        this.cursor = idx;
        this.reveal = this.matches[this.matchHead + idx];
        this.searchDirty = true;
        this.viewDirty = true;
        this.schedule();
    }

    private lowerBound(line: number): number {
        let lo = this.matchHead;
        let hi = this.matches.length;
        while (lo < hi) {
            const mid = (lo + hi) >>> 1;
            if (this.matches[mid] < line) lo = mid + 1;
            else hi = mid;
        }
        return lo - this.matchHead;
    }

    private trimMatches(): void {
        const first = this.buf.first;
        while (this.matchHead < this.matches.length && this.matches[this.matchHead] < first) {
            this.matchHead++;
            if (this.cursor > 0) this.cursor--;
            else this.cursor = -1;
        }
        if (this.matchHead > 4096 && this.matchHead * 2 > this.matches.length) {
            this.matches = this.matches.slice(this.matchHead);
            this.matchHead = 0;
        }
    }

    private scanNew(): void {
        if (!this.query) return;
        const from = Math.max(this.searchedTo, this.buf.first);
        if (from < this.buf.total) {
            const hits = this.buf.search(this.query, this.matchCase, from);
            for (const h of hits) this.matches.push(h);
        }
        this.searchedTo = this.buf.total;
        this.searchDirty = true;
    }

    dispose(): void {
        this.closed = true;
        if (this.timer) this.io.cancel(this.timer);
        this.timer = null;
    }

    // ---- posting ----

    private schedule(): void {
        if (this.timer || this.closed) return;
        const wait = Math.max(0, LOG_FLUSH_MS - (this.io.now() - this.lastFlush));
        this.timer = this.io.schedule(() => {
            this.timer = null;
            this.flush();
        }, wait);
    }

    /** Posts now. Exposed for tests; normally timer-driven (at most one per frame). */
    flush(): void {
        if (this.closed) return;
        if (this.timer) {
            this.io.cancel(this.timer);
            this.timer = null;
        }
        this.lastFlush = this.io.now();
        const buf = this.buf;
        const total = buf.total;
        const first = buf.first;
        const v = this.view;
        let from = v.follow ? Math.max(first, total - v.count) : Math.max(first, Math.min(v.first, Math.max(first, total - 1)));
        const to = Math.min(total, from + v.count);
        const metaChanged = total !== this.lastPostedTotal || first !== this.lastPostedFirst;
        if (!metaChanged && !this.viewDirty && !this.searchDirty && this.statePosted) return;
        // Lines are re-posted when the viewport moved, or when appended lines fall inside it (follow, or a view reaching the tail).
        const needLines = this.viewDirty || (metaChanged && (v.follow || v.first + v.count > this.lastPostedTotal));
        let lines: LogLineView[] | undefined;
        if (needLines) {
            lines = [];
            if (v.count > 0 && total > 0) for (let n = from; n < to; n++) lines.push(this.lineView(n));
            else from = total;
        }
        let search: LogSearchState | undefined;
        if (this.searchDirty) {
            this.trimMatches();
            const count = this.matches.length - this.matchHead;
            search = { query: this.queryText, count, current: this.cursor >= 0 ? this.cursor + 1 : 0 };
            if (this.reveal >= 0) {
                search.reveal = this.reveal;
                this.reveal = -1;
            }
        }
        this.io.post({
            t: 'log',
            sid: this.o.sid,
            seq: ++this.seq,
            total,
            first,
            from,
            lines,
            search,
            state: this.state,
            error: this.endError || undefined
        });
        this.lastPostedTotal = total;
        this.lastPostedFirst = first;
        this.viewDirty = false;
        this.searchDirty = false;
        this.statePosted = true;
    }

    private lineView(n: number): LogLineView {
        const raw = this.buf.text(n);
        const sp = splitLine(raw, this.o.multi);
        const a = parseAnsi(sp.rest);
        const out: LogLineView = { n, text: a.text };
        if (sp.src) out.src = sp.src;
        if (sp.ts) out.ts = sp.ts;
        if (a.spans.length) out.spans = a.spans;
        const level = detectLevel(a.text);
        if (level) out.level = level;
        if (this.query && this.queryText) {
            const hl = highlights(a.text, this.queryText, this.matchCase);
            if (hl.length) out.hl = hl;
        }
        return out;
    }
}

/** `[start, end, ...]` offsets of every occurrence of `q` in `text` (non-overlapping). */
export function highlights(text: string, q: string, matchCase: boolean): number[] {
    const out: number[] = [];
    if (!q) return out;
    const hay = matchCase ? text : text.toLowerCase();
    const needle = matchCase ? q : q.toLowerCase();
    let i = 0;
    while (out.length < 64) {
        const k = hay.indexOf(needle, i);
        if (k < 0) break;
        out.push(k, k + needle.length);
        i = k + Math.max(1, needle.length);
    }
    return out;
}
