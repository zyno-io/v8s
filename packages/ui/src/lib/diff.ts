export interface DiffLine {
    n: number;
    t: string;
}

export type DiffKind = 'same' | 'add' | 'del' | 'chg';

/** One side-by-side row: `l` is the live side, `r` the edited side. */
export interface DiffRow {
    kind: DiffKind;
    l?: DiffLine;
    r?: DiffLine;
}

type Op = { op: 'eq' | 'del' | 'add'; a?: number; b?: number };

const MAX_CELLS = 6_000_000;

/** Line-level LCS diff with common prefix/suffix trimmed first. Falls back to "replace the middle" for huge inputs. */
function lineOps(a: string[], b: string[]): Op[] {
    let s = 0;
    while (s < a.length && s < b.length && a[s] === b[s]) s++;
    let ea = a.length;
    let eb = b.length;
    while (ea > s && eb > s && a[ea - 1] === b[eb - 1]) {
        ea--;
        eb--;
    }
    const ops: Op[] = [];
    for (let i = 0; i < s; i++) ops.push({ op: 'eq', a: i, b: i });
    const n = ea - s;
    const m = eb - s;
    if (n * m > MAX_CELLS) {
        for (let i = 0; i < n; i++) ops.push({ op: 'del', a: s + i });
        for (let j = 0; j < m; j++) ops.push({ op: 'add', b: s + j });
    } else if (n === 0 || m === 0) {
        for (let i = 0; i < n; i++) ops.push({ op: 'del', a: s + i });
        for (let j = 0; j < m; j++) ops.push({ op: 'add', b: s + j });
    } else {
        // dp[i][j] = LCS length of a[s+i..] and b[s+j..]
        const w = m + 1;
        const dp = new Uint32Array((n + 1) * w);
        for (let i = n - 1; i >= 0; i--) {
            for (let j = m - 1; j >= 0; j--) {
                dp[i * w + j] = a[s + i] === b[s + j] ? dp[(i + 1) * w + j + 1] + 1 : Math.max(dp[(i + 1) * w + j], dp[i * w + j + 1]);
            }
        }
        let i = 0;
        let j = 0;
        while (i < n && j < m) {
            if (a[s + i] === b[s + j]) {
                ops.push({ op: 'eq', a: s + i, b: s + j });
                i++;
                j++;
            } else if (dp[(i + 1) * w + j] >= dp[i * w + j + 1]) {
                ops.push({ op: 'del', a: s + i });
                i++;
            } else {
                ops.push({ op: 'add', b: s + j });
                j++;
            }
        }
        while (i < n) ops.push({ op: 'del', a: s + i++ });
        while (j < m) ops.push({ op: 'add', b: s + j++ });
    }
    for (let k = 0; k < a.length - ea; k++) ops.push({ op: 'eq', a: ea + k, b: eb + k });
    return ops;
}

/** Side-by-side rows; runs of deletions followed by additions are paired into `chg` rows. */
export function diffLines(left: string, right: string): DiffRow[] {
    const a = left.split('\n');
    const b = right.split('\n');
    const ops = lineOps(a, b);
    const rows: DiffRow[] = [];
    let k = 0;
    while (k < ops.length) {
        const o = ops[k];
        if (o.op === 'eq') {
            rows.push({ kind: 'same', l: { n: o.a! + 1, t: a[o.a!] }, r: { n: o.b! + 1, t: b[o.b!] } });
            k++;
            continue;
        }
        const dels: number[] = [];
        const adds: number[] = [];
        while (k < ops.length && ops[k].op !== 'eq') {
            if (ops[k].op === 'del') dels.push(ops[k].a!);
            else adds.push(ops[k].b!);
            k++;
        }
        const pair = Math.max(dels.length, adds.length);
        for (let p = 0; p < pair; p++) {
            const l = p < dels.length ? { n: dels[p] + 1, t: a[dels[p]] } : undefined;
            const r = p < adds.length ? { n: adds[p] + 1, t: b[adds[p]] } : undefined;
            rows.push({ kind: l && r ? 'chg' : l ? 'del' : 'add', l, r });
        }
    }
    return rows;
}

export function countChanges(rows: readonly DiffRow[]): number {
    let n = 0;
    for (const r of rows) if (r.kind !== 'same') n++;
    return n;
}

export type DiffBlock = { type: 'rows'; rows: DiffRow[] } | { type: 'skip'; count: number };

/** Keeps `context` unchanged rows around every change and collapses the rest into skip blocks. */
export function collapseContext(rows: readonly DiffRow[], context = 3): DiffBlock[] {
    const keep = new Uint8Array(rows.length);
    rows.forEach((r, i) => {
        if (r.kind === 'same') return;
        for (let d = -context; d <= context; d++) if (i + d >= 0 && i + d < rows.length) keep[i + d] = 1;
    });
    const out: DiffBlock[] = [];
    let i = 0;
    while (i < rows.length) {
        const start = i;
        const on = keep[i] === 1;
        while (i < rows.length && (keep[i] === 1) === on) i++;
        if (on) out.push({ type: 'rows', rows: rows.slice(start, i) as DiffRow[] });
        else out.push({ type: 'skip', count: i - start });
    }
    return out;
}
