import { describe, expect, it } from 'vitest';

import { compareRuns, extractMetrics, median, parseArgs, type RunMetrics } from '../perf/compare.ts';

const run = (over: Partial<RunMetrics> = {}): RunMetrics => ({
    scriptingTotalMs: 400,
    scriptingP95Ms: 2,
    scrollDroppedPct: 1,
    churnDroppedPct: 1,
    longTaskMaxMs: 0,
    heapPeakMB: 40,
    firstRowsMs: 900,
    filterSettleMs: 300,
    ...over
});

const failing = (base: RunMetrics[], head: RunMetrics[]) =>
    compareRuns(base, head)
        .filter(r => r.fail)
        .map(r => r.key);

describe('median', () => {
    it('odd count', () => expect(median([5, 1, 3])).toBe(3));
    it('even count averages the middle two', () => expect(median([4, 1, 3, 2])).toBe(2.5));
    it('single and empty', () => {
        expect(median([7])).toBe(7);
        expect(median([])).toBeNaN();
    });
});

describe('compareRuns', () => {
    it('passes identical and slightly noisy runs', () => {
        expect(failing([run()], [run()])).toEqual([]);
        expect(failing([run(), run()], [run({ scriptingTotalMs: 430, heapPeakMB: 43, scrollDroppedPct: 4 }), run()])).toEqual([]);
    });
    it('uses medians with an even number of runs', () => {
        const rows = compareRuns(
            [run({ scriptingTotalMs: 100 }), run({ scriptingTotalMs: 200 })],
            [run({ scriptingTotalMs: 300 }), run({ scriptingTotalMs: 500 })]
        );
        const r = rows.find(x => x.key === 'scriptingTotalMs')!;
        expect([r.base, r.head, r.delta]).toEqual([150, 400, 250]);
    });
    it('scripting total needs both the ratio and the absolute delta', () => {
        expect(failing([run({ scriptingTotalMs: 400 })], [run({ scriptingTotalMs: 461 })])).toEqual(['scriptingTotalMs']);
        expect(failing([run({ scriptingTotalMs: 400 })], [run({ scriptingTotalMs: 450 })])).toEqual([]); // < 15%
        expect(failing([run({ scriptingTotalMs: 100 })], [run({ scriptingTotalMs: 135 })])).toEqual([]); // < 40 ms
    });
    it('scripting p95', () => {
        expect(failing([run({ scriptingP95Ms: 2 })], [run({ scriptingP95Ms: 2.5 })])).toEqual(['scriptingP95Ms']);
        expect(failing([run({ scriptingP95Ms: 0.5 })], [run({ scriptingP95Ms: 0.7 })])).toEqual([]); // < 0.3 ms
    });
    it('dropped frames, scroll and churn separately', () => {
        expect(failing([run()], [run({ scrollDroppedPct: 6.1 })])).toEqual(['scrollDroppedPct']);
        expect(failing([run()], [run({ churnDroppedPct: 6.1 })])).toEqual(['churnDroppedPct']);
        expect(failing([run()], [run({ churnDroppedPct: 5.9 })])).toEqual([]);
    });
    it('longest task is floored at 50 ms', () => {
        expect(failing([run({ longTaskMaxMs: 0 })], [run({ longTaskMaxMs: 89 })])).toEqual([]);
        expect(failing([run({ longTaskMaxMs: 0 })], [run({ longTaskMaxMs: 91 })])).toEqual(['longTaskMaxMs']);
        expect(failing([run({ longTaskMaxMs: 100 })], [run({ longTaskMaxMs: 139 })])).toEqual([]);
        expect(failing([run({ longTaskMaxMs: 100 })], [run({ longTaskMaxMs: 141 })])).toEqual(['longTaskMaxMs']);
    });
    it('heap peak', () => {
        expect(failing([run({ heapPeakMB: 40 })], [run({ heapPeakMB: 47 })])).toEqual(['heapPeakMB']);
        expect(failing([run({ heapPeakMB: 10 })], [run({ heapPeakMB: 14 })])).toEqual([]); // < 5 MB
    });
    it('first rows and filter settle are report-only; -1 settles are ignored', () => {
        expect(failing([run()], [run({ firstRowsMs: 9000, filterSettleMs: 9000 })])).toEqual([]);
        const r = compareRuns([run({ filterSettleMs: -1 }), run({ filterSettleMs: 200 })], [run()]).find(x => x.key === 'filterSettleMs')!;
        expect(r.base).toBe(200);
        expect(r.fail).toBeNull();
    });
});

describe('extractMetrics / parseArgs', () => {
    it('reads the desktop variant', () => {
        const phase = (droppedPct: number, longTaskMaxMs: number) => ({ droppedPct, longTaskMaxMs });
        const m = extractMetrics({
            results: [
                { name: 'mobile-4x' },
                {
                    name: 'desktop',
                    firstRowsMs: 1,
                    filterSettleMs: 2,
                    heapMB: [{ mb: 10 }, { mb: 30 }],
                    scripting: { totalMs: 5, p95Ms: 1 },
                    phases: { scroll: phase(2, 10), filter: phase(0, 0), churn: phase(3, 60) }
                }
            ]
        });
        expect(m).toMatchObject({ scriptingTotalMs: 5, scrollDroppedPct: 2, churnDroppedPct: 3, longTaskMaxMs: 60, heapPeakMB: 30 });
    });
    it('rejects a run missing a metric (a renamed field must not silently disable a gate)', () => {
        const phase = { droppedPct: 0, longTaskMaxMs: 0 };
        const desktop = {
            name: 'desktop',
            firstRowsMs: 1,
            filterSettleMs: -1,
            heapMB: [{ mb: 10 }],
            scripting: { p95Ms: 1 },
            phases: { scroll: phase, filter: phase, churn: phase }
        };
        expect(() => extractMetrics({ results: [desktop] })).toThrow(/scriptingTotalMs/);
    });
    it('splits args by side', () => {
        expect(parseArgs(['--base', 'a', 'b', '--head', 'c'])).toEqual({ base: ['a', 'b'], head: ['c'] });
        expect(() => parseArgs(['--base', 'a'])).toThrow();
    });
});
