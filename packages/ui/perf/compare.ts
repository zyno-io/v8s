/**
 * A/B comparison of perf runs: `node perf/compare.ts --base a.json [b.json ...] --head c.json [d.json ...]`.
 * Inputs are `perf/run.ts` outputs (any variants; only `desktop` is compared). Exits 1 when a regression rule fails.
 */
import { appendFileSync, readFileSync } from 'node:fs';

/**
 * Regression rules, head vs base (medians over the runs on each side). A rule fails only when every one of its
 * conditions holds, so a relative jump on a tiny absolute value does not trip it.
 * - `ratio`: head > base * ratio. `minDeltaAbs`: head - base must also exceed this (metric units).
 * - `pointsAbs`: head > base + points (percentage points).
 * - `floor`/`marginAbs` (long task): head > max(base, floor) + margin, in ms.
 * `firstRowsMs` and `filterSettleMs` are report-only.
 */
export const RULES = {
    scriptingTotal: { ratio: 1.15, minDeltaAbs: 40 }, // ms
    scriptingP95: { ratio: 1.2, minDeltaAbs: 0.3 }, // ms
    scrollDroppedPct: { pointsAbs: 5 },
    churnDroppedPct: { pointsAbs: 5 },
    longTask: { floor: 50, marginAbs: 40 }, // ms
    heapPeak: { ratio: 1.15, minDeltaAbs: 5 } // MB
} as const;

export interface RunMetrics {
    scriptingTotalMs: number;
    scriptingP95Ms: number;
    scrollDroppedPct: number;
    churnDroppedPct: number;
    longTaskMaxMs: number;
    heapPeakMB: number;
    firstRowsMs: number;
    /** -1 (never settled) is dropped before the median. */
    filterSettleMs: number;
}

export type MetricKey = keyof RunMetrics;

interface DesktopResult {
    name: string;
    firstRowsMs: number;
    filterSettleMs: number;
    heapMB: { mb: number }[];
    scripting: { totalMs: number; p95Ms: number };
    phases: Record<string, { droppedPct: number; longTaskMaxMs: number }>;
}

export function median(values: number[]): number {
    if (!values.length) return NaN;
    const s = [...values].sort((a, b) => a - b);
    const m = s.length >> 1;
    return s.length % 2 ? s[m] : (s[m - 1] + s[m]) / 2;
}

/** Extracts the compared metrics from a parsed `run.ts` output; throws when there is no desktop result. */
export function extractMetrics(out: { results: unknown[] }): RunMetrics {
    const d = (out.results as DesktopResult[]).find(r => r.name === 'desktop');
    if (!d) throw new Error('no desktop variant in results');
    return {
        scriptingTotalMs: d.scripting.totalMs,
        scriptingP95Ms: d.scripting.p95Ms,
        scrollDroppedPct: d.phases.scroll.droppedPct,
        churnDroppedPct: d.phases.churn.droppedPct,
        longTaskMaxMs: Math.max(d.phases.scroll.longTaskMaxMs, d.phases.churn.longTaskMaxMs),
        heapPeakMB: Math.max(...d.heapMB.map(h => h.mb)),
        firstRowsMs: d.firstRowsMs,
        filterSettleMs: d.filterSettleMs
    };
}

export interface Row {
    key: MetricKey;
    label: string;
    base: number;
    head: number;
    delta: number;
    /** null = report-only. */
    fail: boolean | null;
}

const round = (n: number, d = 2) => (Number.isFinite(n) ? Math.round(n * 10 ** d) / 10 ** d : n);

const LABELS: Record<MetricKey, string> = {
    scriptingTotalMs: 'scripting total during scroll (ms)',
    scriptingP95Ms: 'scripting p95 per frame (ms)',
    scrollDroppedPct: 'scroll dropped frames (%)',
    churnDroppedPct: 'churn dropped frames (%)',
    longTaskMaxMs: 'longest task, scroll/churn (ms)',
    heapPeakMB: 'peak JS heap (MB)',
    firstRowsMs: 'first rows (ms, report only)',
    filterSettleMs: 'filter settle (ms, report only)'
};

function verdict(key: MetricKey, base: number, head: number): boolean | null {
    const delta = head - base;
    switch (key) {
        case 'scriptingTotalMs':
            return head > base * RULES.scriptingTotal.ratio && delta > RULES.scriptingTotal.minDeltaAbs;
        case 'scriptingP95Ms':
            return head > base * RULES.scriptingP95.ratio && delta > RULES.scriptingP95.minDeltaAbs;
        case 'scrollDroppedPct':
            return head > base + RULES.scrollDroppedPct.pointsAbs;
        case 'churnDroppedPct':
            return head > base + RULES.churnDroppedPct.pointsAbs;
        case 'longTaskMaxMs':
            return head > Math.max(base, RULES.longTask.floor) + RULES.longTask.marginAbs;
        case 'heapPeakMB':
            return head > base * RULES.heapPeak.ratio && delta > RULES.heapPeak.minDeltaAbs;
        default:
            return null;
    }
}

/** Median per side and the verdict for every metric. */
export function compareRuns(base: RunMetrics[], head: RunMetrics[]): Row[] {
    return (Object.keys(LABELS) as MetricKey[]).map(key => {
        const pick = (runs: RunMetrics[]) => median(runs.map(r => r[key]).filter(v => key !== 'filterSettleMs' || v >= 0));
        const b = pick(base);
        const h = pick(head);
        const reportOnly = verdict(key, b, h) === null;
        return { key, label: LABELS[key], base: b, head: h, delta: h - b, fail: reportOnly || Number.isNaN(b + h) ? null : verdict(key, b, h) };
    });
}

export function renderTable(rows: Row[]): string {
    const fmt = (n: number) => (Number.isFinite(n) ? String(round(n)) : 'n/a');
    const sign = (n: number) => (Number.isFinite(n) ? (n > 0 ? '+' : '') + round(n) : 'n/a');
    const lines = ['| metric | base median | head median | Δ | verdict |', '| --- | ---: | ---: | ---: | --- |'];
    for (const r of rows) {
        lines.push(`| ${r.label} | ${fmt(r.base)} | ${fmt(r.head)} | ${sign(r.delta)} | ${r.fail === null ? 'info' : r.fail ? 'FAIL' : 'ok'} |`);
    }
    return lines.join('\n');
}

export function renderRaw(side: string, runs: RunMetrics[]): string {
    const keys = Object.keys(LABELS) as MetricKey[];
    const lines = [`| ${side} run | ${keys.map(k => k).join(' | ')} |`, `| --- | ${keys.map(() => '---:').join(' | ')} |`];
    runs.forEach((r, i) => lines.push(`| ${i + 1} | ${keys.map(k => round(r[k])).join(' | ')} |`));
    return lines.join('\n');
}

export function parseArgs(argv: string[]): { base: string[]; head: string[] } {
    const sides: Record<'base' | 'head', string[]> = { base: [], head: [] };
    let cur: 'base' | 'head' | null = null;
    for (const a of argv) {
        if (a === '--base') cur = 'base';
        else if (a === '--head') cur = 'head';
        else if (cur) sides[cur].push(a);
        else throw new Error(`unexpected argument ${a}`);
    }
    if (!sides.base.length || !sides.head.length) throw new Error('usage: compare.ts --base a.json [b.json ...] --head c.json [d.json ...]');
    return sides;
}

function main(): number {
    const { base, head } = parseArgs(process.argv.slice(2));
    const load = (files: string[]) => files.map(f => extractMetrics(JSON.parse(readFileSync(f, 'utf8'))));
    const b = load(base);
    const h = load(head);
    const rows = compareRuns(b, h);
    const failed = rows.filter(r => r.fail);
    const md = [
        '### Perf A/B (desktop, 50k pods)',
        '',
        renderTable(rows),
        '',
        failed.length ? `**Regression:** ${failed.map(r => r.label).join('; ')}` : 'No regression.',
        '',
        '<details><summary>Raw runs</summary>',
        '',
        renderRaw('base', b),
        '',
        renderRaw('head', h),
        '',
        '</details>',
        ''
    ].join('\n');
    console.log(md);
    if (process.env.GITHUB_STEP_SUMMARY) appendFileSync(process.env.GITHUB_STEP_SUMMARY, md + '\n');
    return failed.length ? 1 : 0;
}

if (import.meta.main) {
    try {
        process.exit(main());
    } catch (e) {
        console.error(e instanceof Error ? e.message : e);
        process.exit(2);
    }
}
