import type { Flow, FlowRate, FlowsFrame } from '../protocol.gen.ts';
import type { FlowUpdate } from '../types.ts';

export const FLOW_FLUSH_MS = 16;
/** Hard cap on flows in one posted window. */
export const MAX_VIEW_FLOWS = 300;

export interface FlowSessionIO {
    /** Sends a text frame on the stream socket (pause / resume). */
    send(text: string): void;
    closeSocket(): void;
    post(msg: FlowUpdate): void;
    schedule(fn: () => void, ms: number): unknown;
    cancel(handle: unknown): void;
    now(): number;
}

export interface FlowSessionOptions {
    sid: number;
    /** Flows kept in the ring buffer (desktop 20k, phone 2k). */
    capacity: number;
}

/** A fixed-capacity ring of flows addressed by absolute index (monotonic; survives the oldest being overwritten). */
export class FlowRing {
    private items: (Flow | undefined)[];
    total = 0;

    constructor(readonly capacity: number) {
        this.items = Array.from<Flow | undefined>({ length: capacity });
    }

    get first(): number {
        return Math.max(0, this.total - this.capacity);
    }

    push(f: Flow): void {
        this.items[this.total % this.capacity] = f;
        this.total++;
    }

    at(abs: number): Flow | undefined {
        if (abs < this.first || abs >= this.total) return undefined;
        return this.items[abs % this.capacity];
    }
}

interface View {
    first: number;
    count: number;
    follow: boolean;
}

/**
 * One Hubble flows stream as the Data Worker owns it: JSON text frames in, ring buffer, at most one batched post per frame
 * carrying the counters, the sampling rate and the flows of the viewport the main thread asked for.
 */
export class FlowSession {
    private ring: FlowRing;
    /** Absolute index of the first flow after a `clear()`. */
    private floor = 0;
    private view: View = { first: 0, count: 0, follow: true };
    private timer: unknown = null;
    private lastFlush = -Infinity;
    private viewDirty = true;
    private lastPostedTotal = -1;
    private lastPostedFirst = -1;
    private rate: FlowRate | undefined;
    private rateDirty = false;
    private state: 'live' | 'ended' | 'error' = 'live';
    private endError = '';
    private statePosted = false;
    private seq = 0;
    private closed = false;
    private paused = false;

    constructor(
        private readonly o: FlowSessionOptions,
        private readonly io: FlowSessionIO
    ) {
        this.ring = new FlowRing(o.capacity);
    }

    get total(): number {
        return this.ring.total;
    }

    get first(): number {
        return Math.max(this.ring.first, this.floor);
    }

    at(abs: number): Flow | undefined {
        return abs < this.floor ? undefined : this.ring.at(abs);
    }

    // ---- socket side ----

    onText(text: string): void {
        let m: FlowsFrame;
        try {
            m = JSON.parse(text) as FlowsFrame;
        } catch {
            return;
        }
        if (m.flows) for (const f of m.flows) this.ring.push(f);
        if (m.rate) {
            this.rate = m.rate;
            this.rateDirty = true;
        }
        if (m.end) this.finish(m.error ? 'error' : 'ended', m.error ?? '');
        else if (m.flows || m.rate) this.schedule();
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
        this.view = { first, count: Math.min(count, MAX_VIEW_FLOWS), follow };
        this.viewDirty = true;
        this.schedule();
    }

    /** Tells the agent whether any view shows the flows (a stream paused for 10 s is cancelled agent-side). */
    setPaused(paused: boolean): void {
        if (paused === this.paused || this.closed) return;
        this.paused = paused;
        this.io.send(JSON.stringify(paused ? { pause: true } : { resume: true }));
    }

    clear(): void {
        this.floor = this.ring.total;
        this.viewDirty = true;
        this.schedule();
    }

    dispose(): void {
        this.closed = true;
        if (this.timer) this.io.cancel(this.timer);
        this.timer = null;
    }

    // ---- posting ----

    private schedule(): void {
        if (this.timer || this.closed) return;
        const wait = Math.max(0, FLOW_FLUSH_MS - (this.io.now() - this.lastFlush));
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
        const total = this.ring.total;
        const first = this.first;
        const v = this.view;
        let from = v.follow ? Math.max(first, total - v.count) : Math.max(first, Math.min(v.first, Math.max(first, total - 1)));
        const to = Math.min(total, from + v.count);
        const metaChanged = total !== this.lastPostedTotal || first !== this.lastPostedFirst;
        if (!metaChanged && !this.viewDirty && !this.rateDirty && this.statePosted) return;
        const needFlows = this.viewDirty || (metaChanged && (v.follow || v.first + v.count > this.lastPostedTotal));
        let flows: Flow[] | undefined;
        if (needFlows) {
            flows = [];
            if (v.count > 0 && total > 0) {
                for (let n = from; n < to; n++) {
                    const f = this.ring.at(n);
                    if (f) flows.push(f);
                }
            } else from = total;
        }
        this.io.post({
            t: 'flow',
            sid: this.o.sid,
            seq: ++this.seq,
            total,
            first,
            from,
            flows,
            rate: this.rateDirty ? this.rate : undefined,
            state: this.state,
            error: this.endError || undefined
        });
        this.lastPostedTotal = total;
        this.lastPostedFirst = first;
        this.viewDirty = false;
        this.rateDirty = false;
        this.statePosted = true;
    }
}
