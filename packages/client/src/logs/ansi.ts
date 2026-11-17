/**
 * ANSI SGR parsing for log lines. Runs in the Data Worker on the visible window only; the result is a stripped string plus flat
 * span runs so the main thread never sees an escape sequence.
 */

/** Style flags of a span. */
export const BOLD = 1;
export const DIM = 2;
export const ITALIC = 4;
export const UNDERLINE = 8;

/** Colour encoding: -1 none, 0..15 palette index, otherwise `0x1000000 | 0xRRGGBB`. */
export const NO_COLOR = -1;
export const RGB_FLAG = 0x1000000;

/** Spans are flat: `[start, end, fg, bg, flags, start, end, ...]` over the stripped text (UTF-16 offsets). */
export const SPAN_STRIDE = 5;

export interface Ansi {
    text: string;
    /** Empty when the line carries no styling. */
    spans: number[];
}

// CSI (ESC [ ... final), OSC (ESC ] ... BEL|ST), and other two-byte escapes.
// oxlint-disable-next-line no-control-regex
const ESC = /\x1b(?:\[([0-9;:?]*)([\x20-\x2f]*[\x40-\x7e])|\][^\x07\x1b]*(?:\x07|\x1b\\)|[@-Z\\-_])/g;

const ANSI256_BASE = 16;

function color256(n: number): number {
    if (n < 16) return n;
    if (n < 232) {
        const c = n - ANSI256_BASE;
        const lv = (v: number) => (v === 0 ? 0 : 55 + v * 40);
        return RGB_FLAG | (lv(Math.floor(c / 36)) << 16) | (lv(Math.floor(c / 6) % 6) << 8) | lv(c % 6);
    }
    const g = 8 + (n - 232) * 10;
    return RGB_FLAG | (g << 16) | (g << 8) | g;
}

interface Style {
    fg: number;
    bg: number;
    flags: number;
}

function applySgr(params: string, st: Style): void {
    const p = params === '' ? [0] : params.split(/[;:]/).map(s => (s === '' ? 0 : Number(s)));
    for (let i = 0; i < p.length; i++) {
        const c = p[i];
        if (c === 0) {
            st.fg = NO_COLOR;
            st.bg = NO_COLOR;
            st.flags = 0;
        } else if (c === 1) st.flags |= BOLD;
        else if (c === 2) st.flags |= DIM;
        else if (c === 3) st.flags |= ITALIC;
        else if (c === 4) st.flags |= UNDERLINE;
        else if (c === 22) st.flags &= ~(BOLD | DIM);
        else if (c === 23) st.flags &= ~ITALIC;
        else if (c === 24) st.flags &= ~UNDERLINE;
        else if (c >= 30 && c <= 37) st.fg = c - 30;
        else if (c >= 90 && c <= 97) st.fg = c - 90 + 8;
        else if (c >= 40 && c <= 47) st.bg = c - 40;
        else if (c >= 100 && c <= 107) st.bg = c - 100 + 8;
        else if (c === 39) st.fg = NO_COLOR;
        else if (c === 49) st.bg = NO_COLOR;
        else if (c === 38 || c === 48) {
            let v = NO_COLOR;
            if (p[i + 1] === 5 && i + 2 < p.length) {
                v = color256(p[i + 2]);
                i += 2;
            } else if (p[i + 1] === 2 && i + 4 < p.length) {
                v = RGB_FLAG | ((p[i + 2] & 255) << 16) | ((p[i + 3] & 255) << 8) | (p[i + 4] & 255);
                i += 4;
            } else break;
            if (c === 38) st.fg = v;
            else st.bg = v;
        }
    }
}

/** Fast path: no ESC byte means the text is returned as is. */
export function parseAnsi(raw: string): Ansi {
    if (raw.indexOf('\x1b') < 0) return { text: raw, spans: [] };
    const st: Style = { fg: NO_COLOR, bg: NO_COLOR, flags: 0 };
    const spans: number[] = [];
    let out = '';
    let last = 0;
    let runStart = 0;
    let runStyle: Style = { ...st };
    const closeRun = (end: number) => {
        if (end > runStart && (runStyle.fg !== NO_COLOR || runStyle.bg !== NO_COLOR || runStyle.flags !== 0)) {
            spans.push(runStart, end, runStyle.fg, runStyle.bg, runStyle.flags);
        }
    };
    ESC.lastIndex = 0;
    let m: RegExpExecArray | null;
    while ((m = ESC.exec(raw))) {
        out += raw.slice(last, m.index);
        last = m.index + m[0].length;
        if (m[2] === 'm' && m[1] !== undefined && !m[1].startsWith('?')) {
            closeRun(out.length);
            applySgr(m[1], st);
            runStart = out.length;
            runStyle = { ...st };
        }
    }
    out += raw.slice(last);
    closeRun(out.length);
    return { text: out, spans };
}

export type LogLevel = 0 | 2 | 3;

const KV = /(?:^|[\s,{])(?:level|lvl|severity)=("?)([A-Za-z]+)\1/;
const JSON_LEVEL = /"(?:level|lvl|severity|log\.level)"\s*:\s*"([A-Za-z]+)"/;
const BRACKET = /^\s*(?:\d{4}-\d\d-\d\d[T ][\d:.,]+Z?\s+)?\[?(ERROR|ERR|FATAL|CRITICAL|PANIC|WARN|WARNING)\]?[\s:]/i;

function levelOf(word: string): LogLevel {
    switch (word.toLowerCase()) {
        case 'error':
        case 'err':
        case 'fatal':
        case 'panic':
        case 'critical':
        case 'crit':
        case 'dpanic':
            return 3;
        case 'warn':
        case 'warning':
            return 2;
        default:
            return 0;
    }
}

/**
 * Level of a structured line: `level=error`, JSON `"level":"error"`, or a leading `ERROR`/`WARN` token. 0 means "no gutter":
 * unstructured lines and info/debug lines are never tinted.
 */
export function detectLevel(text: string): LogLevel {
    if (text.length < 5) return 0;
    let m = KV.exec(text);
    if (m) return levelOf(m[2]);
    if (text.charCodeAt(0) === 123 /* { */ || text.indexOf('{"') >= 0) {
        m = JSON_LEVEL.exec(text);
        if (m) return levelOf(m[1]);
    }
    m = BRACKET.exec(text);
    return m ? levelOf(m[1]) : 0;
}

const TS = /^(\d{4}-\d\d-\d\dT[\d:.]+(?:Z|[+-]\d\d:\d\d)) /;
const SRC = /^([^\s\t/]+\/[^\s\t]+)\t/;

export interface SplitLine {
    src?: string;
    ts?: string;
    rest: string;
}

/** Splits `<pod>/<container>\t` (merged streams only) and the RFC3339 timestamp prefix off a raw line. */
export function splitLine(raw: string, multi: boolean): SplitLine {
    let rest = raw;
    let src: string | undefined;
    if (multi) {
        const m = SRC.exec(rest);
        if (m) {
            src = m[1];
            rest = rest.slice(m[0].length);
        }
    }
    const t = TS.exec(rest);
    if (t) return { src, ts: t[1], rest: rest.slice(t[0].length) };
    return { src, rest };
}
