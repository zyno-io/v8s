/**
 * Ring buffer of log lines held as ONE growable byte arena (UTF-8, each line followed by `\n`) plus offset arrays: no JS string
 * per line is ever created on ingest. Lines are decoded only when they are shown. Lines are numbered by an absolute, monotonic
 * index so the UI's scroll anchors and search hits survive the oldest lines being dropped.
 */

const NL = 0x0a;
const CR = 0x0d;

export interface LogBufferOptions {
    /** Lines to keep after a trim (desktop 50k, phone 5k). */
    capacity: number;
    /** Extra slots before a trim happens; amortises the compaction. Default 25% of capacity (at least 256). */
    slack?: number;
}

const decoder = new TextDecoder('utf-8');

export class LogBuffer {
    readonly capacity: number;
    private readonly slots: number;
    private arena: Uint8Array;
    private arenaLen = 0;
    private off: Uint32Array;
    private len: Uint32Array;
    private count = 0;
    private firstAbs = 0;
    private partial: Uint8Array | null = null;

    constructor(opts: LogBufferOptions) {
        this.capacity = Math.max(1, opts.capacity);
        const slack = opts.slack ?? Math.max(256, Math.floor(this.capacity / 4));
        this.slots = this.capacity + slack;
        this.arena = new Uint8Array(64 * 1024);
        this.off = new Uint32Array(Math.min(this.slots, 1024));
        this.len = new Uint32Array(this.off.length);
    }

    /** Absolute index of the first line still held; lines before it were dropped. */
    get first(): number {
        return this.firstAbs;
    }
    /** Absolute index one past the last complete line (= number of lines ever appended). */
    get total(): number {
        return this.firstAbs + this.count;
    }
    /** Lines currently held. */
    get held(): number {
        return this.count;
    }
    /** Lines dropped from the top so far. */
    get dropped(): number {
        return this.firstAbs;
    }
    /** Bytes of arena in use (tests, diagnostics). */
    get bytes(): number {
        return this.arenaLen;
    }

    clear(): void {
        this.firstAbs += this.count;
        this.count = 0;
        this.arenaLen = 0;
        this.partial = null;
    }

    /** Appends raw bytes; complete lines are stored, a trailing partial line is carried to the next call. Returns lines added. */
    append(chunk: Uint8Array): number {
        let data = chunk;
        if (this.partial) {
            const merged = new Uint8Array(this.partial.length + chunk.length);
            merged.set(this.partial, 0);
            merged.set(chunk, this.partial.length);
            data = merged;
            this.partial = null;
        }
        let pos = 0;
        let added = 0;
        for (;;) {
            const nl = data.indexOf(NL, pos);
            if (nl < 0) break;
            let end = nl;
            if (end > pos && data[end - 1] === CR) end--;
            this.push(data, pos, end);
            added++;
            pos = nl + 1;
        }
        if (pos < data.length) this.partial = data.slice(pos);
        return added;
    }

    private push(src: Uint8Array, from: number, to: number): void {
        if (this.count >= this.slots) this.trim();
        const n = to - from;
        if (this.arenaLen + n + 1 > this.arena.length) {
            let cap = this.arena.length;
            while (cap < this.arenaLen + n + 1) cap *= 2;
            const next = new Uint8Array(cap);
            next.set(this.arena.subarray(0, this.arenaLen));
            this.arena = next;
        }
        if (this.count >= this.off.length) {
            const size = Math.min(this.slots, this.off.length * 2);
            const o = new Uint32Array(size);
            const l = new Uint32Array(size);
            o.set(this.off);
            l.set(this.len);
            this.off = o;
            this.len = l;
        }
        this.arena.set(src.subarray(from, to), this.arenaLen);
        this.arena[this.arenaLen + n] = NL;
        this.off[this.count] = this.arenaLen;
        this.len[this.count] = n;
        this.arenaLen += n + 1;
        this.count++;
    }

    /** Drops the oldest lines down to `capacity` and compacts the arena. */
    private trim(): void {
        const drop = this.count - this.capacity;
        if (drop <= 0) return;
        const start = this.off[drop];
        this.arena.copyWithin(0, start, this.arenaLen);
        this.arenaLen -= start;
        this.off.copyWithin(0, drop, this.count);
        this.len.copyWithin(0, drop, this.count);
        const keep = this.count - drop;
        for (let i = 0; i < keep; i++) this.off[i] -= start;
        this.count = keep;
        this.firstAbs += drop;
    }

    has(abs: number): boolean {
        return abs >= this.firstAbs && abs < this.firstAbs + this.count;
    }

    /** The line's bytes (a view into the arena: valid until the next append). */
    bytesOf(abs: number): Uint8Array {
        const i = abs - this.firstAbs;
        return this.arena.subarray(this.off[i], this.off[i] + this.len[i]);
    }

    text(abs: number): string {
        return this.has(abs) ? decoder.decode(this.bytesOf(abs)) : '';
    }

    /**
     * Absolute indexes (ascending, one per line) of lines containing `pattern`, scanning lines `[fromAbs, total)`.
     * ASCII letters fold case unless `matchCase`. The arena separates lines with `\n`, so a match never spans two lines.
     */
    search(pattern: Uint8Array, matchCase: boolean, fromAbs = this.firstAbs): number[] {
        const out: number[] = [];
        const m = pattern.length;
        if (!m || this.count === 0 || pattern.includes(NL)) return out;
        const lo = (b: number) => (!matchCase && b >= 65 && b <= 90 ? b + 32 : b);
        const p = new Uint8Array(m);
        for (let i = 0; i < m; i++) p[i] = lo(pattern[i]);
        const p0 = p[0];
        const p0u = !matchCase && p0 >= 97 && p0 <= 122 ? p0 - 32 : p0;
        const startLine = Math.max(fromAbs, this.firstAbs) - this.firstAbs;
        if (startLine >= this.count) return out;
        const a = this.arena;
        let line = startLine;
        for (let i = this.off[startLine]; i < this.arenaLen;) {
            const b = a[i];
            if ((b === p0 || b === p0u) && i + m <= this.arenaLen) {
                let k = 1;
                while (k < m && lo(a[i + k]) === p[k]) k++;
                if (k === m) {
                    while (line + 1 < this.count && this.off[line + 1] <= i) line++;
                    out.push(this.firstAbs + line);
                    // skip to the next line
                    i = this.off[line] + this.len[line] + 1;
                    line++;
                    if (line >= this.count) break;
                    continue;
                }
            }
            i++;
        }
        return out;
    }
}

const encoder = new TextEncoder();
export const encodeQuery = (s: string): Uint8Array => encoder.encode(s);
