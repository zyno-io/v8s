import { describe, expect, it } from 'vitest';

import { analyzeTrace, formatBreakdown, mainThread, type TraceEvent } from '../perf/trace.ts';

const meta = (pid: number, tid: number, name: string): TraceEvent => ({ ph: 'M', name: 'thread_name', ts: 0, pid, tid, args: { name } });
const x = (name: string, ts: number, dur: number, extra: Partial<TraceEvent> = {}): TraceEvent => ({
    ph: 'X',
    name,
    ts,
    dur,
    pid: 1,
    tid: 1,
    ...extra
});

describe('perf trace breakdown', () => {
    it('picks the busiest renderer main thread', () => {
        const ev = [meta(1, 1, 'CrRendererMain'), meta(2, 2, 'CrRendererMain'), meta(3, 3, 'Compositor'), x('Layout', 0, 1)];
        expect(mainThread(ev)).toBe('1:1');
        expect(mainThread([meta(3, 3, 'Compositor')])).toBeNull();
    });

    it('totals by category without double-counting nested script, and breaks down a long task', () => {
        const ev: TraceEvent[] = [
            meta(1, 1, 'CrRendererMain'),
            // A 60 ms task (thread time 20 ms): a scroll handler with a nested call, then style, a layout, paint.
            x('ThreadControllerImpl::RunTask', 1000, 60_000, { tdur: 20_000 }),
            x('EventDispatch', 1000, 10_000, { args: { data: { type: 'scroll' } } }),
            x('FunctionCall', 2000, 5000),
            x('UpdateLayoutTree', 11_000, 4000),
            x('Layout', 15_000, 8000, { args: { beginData: { dirtyObjects: 1700, totalObjects: 2300 } } }),
            x('Paint', 23_000, 6000),
            x('V8.GC_SCAVENGER', 30_000, 1000),
            // A short task: counted, not broken down.
            x('ThreadControllerImpl::RunTask', 100_000, 5000),
            x('Layout', 100_000, 2000)
        ];
        const b = analyzeTrace(ev)!;
        expect(b.totals).toEqual({ script: 10, style: 4, layout: 10, prePaint: 0, paint: 6, gc: 1 });
        expect(b.tasks).toBe(2);
        expect(b.longTasks).toHaveLength(1);
        const lt = b.longTasks[0];
        expect([lt.atMs, lt.durMs, lt.cpuMs]).toEqual([0, 60, 20]);
        expect(lt.parts[0]).toEqual(['EventDispatch', 10]);
        expect(lt.layouts).toEqual([{ ms: 8, dirty: 1700, total: 2300 }]);
        expect(lt.scripts[0]).toEqual({ name: 'EventDispatch', what: 'scroll', ms: 10 });
        expect(formatBreakdown('t', b)).toContain('mostly descheduled');
    });
});
