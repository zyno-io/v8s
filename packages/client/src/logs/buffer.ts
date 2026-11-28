/**
 * Ring buffer of log lines held as ONE growable byte arena (UTF-8, each line followed by `\n`) plus offset arrays: no JS string
 * per line is ever created on ingest. Lines are decoded only when they are shown. Lines are numbered by an absolute, monotonic
 * index so the UI's scroll anchors and search hits survive the oldest lines being dropped.
 */

import { parseAnsi } from './ansi.ts';

const NL = 0x0a;
const ESC = 0x1b;
/** Set in `cs` for a line whose visible text needs ANSI stripping before it can be searched. */
const ESC_FLAG = 0x80000000;
const CR = 0x0d;

export interface LogBufferOptions {
    /** Lines to keep after a trim (desktop 50k, phone 5k). */
    capacity: number;
    /** Extra slots before a trim happens; amortizes the compaction. Default 25% of capacity (at least 256). */
    slack?: number;
    /** Cap on the bytes held (the arena): desktop 32 MiB, phone 8 MiB. Long lines must not make 50k lines unbounded. */
    maxBytes?: number;
    /** Merged stream: lines start with `<pod>/<container>\t` (not searchable). */
    multi?: boolean;
}

export const MAX_BYTES_DESKTOP = 32 * 1024 * 1024;
export const MAX_BYTES_PHONE = 8 * 1024 * 1024;
const MIN_ARENA = 64 * 1024;

const decoder = new TextDecoder('utf-8');

export class LogBuffer {
    readonly capacity: number;
    readonly maxBytes: number;
    private readonly slots: number;
    private arena: Uint8Array;
    private arenaLen = 0;
    private off: Uint32Array;
    private len: Uint32Array;
    /** Per line: bytes of `<pod>/<container>\t` and timestamp prefix (search only looks past it), with ESC_FLAG when the line has escapes. */
    private cs: Uint32Array;
    private readonly multi: boolean;
    private count = 0;
    private firstAbs = 0;
    private partial: Uint8Array | null = null;

    constructor(opts: LogBufferOptions) {
        this.capacity = Math.max(1, opts.capacity);
        const slack = opts.slack ?? Math.max(256, Math.floor(this.capacity / 4));
        this.slots = this.capacity + slack;
        this.maxBytes = Math.max(1024, opts.maxBytes ?? MAX_BYTES_DESKTOP);
        this.arena = new Uint8Array(MIN_ARENA);
        this.off = new Uint32Array(Math.min(this.slots, 1024));
        this.len = new Uint32Array(this.off.length);
        this.cs = new Uint32Array(this.off.length);
        this.multi = !!opts.multi;
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
        const n = to - from;
        if (this.count >= this.slots || this.arenaLen + n + 1 > this.maxBytes) this.trim(n + 1);
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
            const c = new Uint32Array(size);
            o.set(this.off);
            l.set(this.len);
            c.set(this.cs);
            this.off = o;
            this.len = l;
            this.cs = c;
        }
        this.arena.set(src.subarray(from, to), this.arenaLen);
        this.arena[this.arenaLen + n] = NL;
        this.off[this.count] = this.arenaLen;
        this.len[this.count] = n;
        this.cs[this.count] = (contentStart(src, from, to, this.multi) - from) | (src.subarray(from, to).includes(ESC) ? ESC_FLAG : 0);
        this.arenaLen += n + 1;
        this.count++;
    }

    /**
     * Drops the oldest lines and compacts the arena: down to `capacity` when the slots are full, and, when the next line (`need`
     * bytes) would pass `maxBytes`, down to 75% of it (amortizes the copy). The newest data always survives. Shrinks an arena
     * that has become much larger than what it holds.
     */
    private trim(need: number): void {
        let drop = this.count >= this.slots ? Math.max(0, this.count - this.capacity) : 0;
        if (this.arenaLen + need > this.maxBytes) {
            const limit = Math.max(Math.floor(this.maxBytes * 0.75), need);
            const remaining = (d: number) => (d >= this.count ? 0 : this.arenaLen - this.off[d]);
            let lo = drop;
            let hi = this.count;
            while (lo < hi) {
                const mid = (lo + hi) >> 1;
                if (remaining(mid) + need <= limit) hi = mid;
                else lo = mid + 1;
            }
            drop = lo;
        }
        if (drop <= 0) return;
        const start = drop >= this.count ? this.arenaLen : this.off[drop];
        this.arena.copyWithin(0, start, this.arenaLen);
        this.arenaLen -= start;
        this.off.copyWithin(0, drop, this.count);
        this.len.copyWithin(0, drop, this.count);
        this.cs.copyWithin(0, drop, this.count);
        const keep = Math.max(0, this.count - drop);
        for (let i = 0; i < keep; i++) this.off[i] -= start;
        this.count = keep;
        this.firstAbs += drop;
        let cap = MIN_ARENA;
        while (cap < (this.arenaLen + need) * 2) cap *= 2;
        if (this.arena.length > cap * 2) {
            const next = new Uint8Array(cap);
            next.set(this.arena.subarray(0, this.arenaLen));
            this.arena = next;
        }
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
     * Absolute indexes (ascending, one per line) of lines whose VISIBLE text contains `pattern`, scanning lines `[fromAbs, total)`.
     * Visible means what the viewer shows: after the pod/container and timestamp prefix, with ANSI escapes stripped (the same text
     * the highlights are computed on). ASCII letters fold case unless `matchCase`. A match never spans two lines.
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
                    const c = this.cs[line];
                    if (!(c & ESC_FLAG) && i - this.off[line] >= c) {
                        out.push(this.firstAbs + line);
                        // skip to the next line
                        i = this.off[line] + this.len[line] + 1;
                        line++;
                        if (line >= this.count) break;
                        continue;
                    }
                    if (c & ESC_FLAG) {
                        // Lines with escapes are decided on their stripped text below.
                        i = this.off[line] + this.len[line] + 1;
                        line++;
                        if (line >= this.count) break;
                        continue;
                    }
                    // A hit inside the prefix: keep looking in the same line.
                }
            }
            i++;
        }
        let esc: number[] | null = null;
        for (let l = startLine; l < this.count; l++) {
            if (!(this.cs[l] & ESC_FLAG)) continue;
            esc ??= [];
            esc.push(l);
        }
        if (esc) {
            const needle = decoder.decode(pattern);
            const want = matchCase ? needle : needle.toLowerCase();
            const hits: number[] = [];
            for (const l of esc) {
                const start = this.off[l] + (this.cs[l] & ~ESC_FLAG);
                const visible = parseAnsi(decoder.decode(this.arena.subarray(start, this.off[l] + this.len[l]))).text;
                if ((matchCase ? visible : visible.toLowerCase()).includes(want)) hits.push(this.firstAbs + l);
            }
            if (hits.length) {
                out.push(...hits);
                out.sort((x, y) => x - y);
            }
        }
        return out;
    }
}

const SP = 0x20;
const TAB = 0x09;
const isWs = (b: number) => b === SP || (b >= 0x09 && b <= 0x0d);
const isDigit = (b: number) => b >= 0x30 && b <= 0x39;

/**
 * Index in `src` where a line's visible content starts: past a `<pod>/<container>\t` prefix (multi only) and an RFC3339
 * timestamp plus its space. Mirrors `splitLine`, on bytes.
 */
export function contentStart(src: Uint8Array, from: number, to: number, multi: boolean): number {
    let p = from;
    if (multi) {
        let i = p;
        while (i < to && !isWs(src[i]) && src[i] !== 0x2f) i++;
        if (i > p && i < to && src[i] === 0x2f) {
            const j0 = i + 1;
            let j = j0;
            while (j < to && !isWs(src[j])) j++;
            if (j > j0 && j < to && src[j] === TAB) p = j + 1;
        }
    }
    // \d{4}-\d\d-\d\dT[\d:.]+(?:Z|[+-]\d\d:\d\d)<space>
    const dig = (i: number, n: number) => {
        for (let k = 0; k < n; k++) if (i + k >= to || !isDigit(src[i + k])) return false;
        return true;
    };
    if (!(dig(p, 4) && src[p + 4] === 0x2d && dig(p + 5, 2) && src[p + 7] === 0x2d && dig(p + 8, 2) && src[p + 10] === 0x54)) return p;
    let i = p + 11;
    const t0 = i;
    while (i < to && (isDigit(src[i]) || src[i] === 0x3a || src[i] === 0x2e)) i++;
    if (i === t0) return p;
    if (i < to && src[i] === 0x5a) i++;
    else if (i < to && (src[i] === 0x2b || src[i] === 0x2d) && dig(i + 1, 2) && src[i + 3] === 0x3a && dig(i + 4, 2)) i += 6;
    else return p;
    return i < to && src[i] === SP ? i + 1 : p;
}

const encoder = new TextEncoder();
export const encodeQuery = (s: string): Uint8Array => encoder.encode(s);
