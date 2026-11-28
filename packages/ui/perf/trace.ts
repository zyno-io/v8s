/**
 * Main-thread breakdown of a Chrome trace (Playwright `browser.startTracing` / `stopTracing` JSON), for perf/profile.ts:
 * where the renderer's main thread spent its time, and what ran inside each long task.
 */

export interface TraceEvent {
    ph: string;
    name: string;
    ts: number;
    dur?: number;
    tdur?: number;
    pid: number;
    tid: number;
    args?: { name?: string; elementCount?: number; data?: Record<string, unknown>; beginData?: Record<string, unknown> };
}

/** Top-level task events: the scheduler's (`ThreadControllerImpl::RunTask`, `RunTask` in older Chrome). */
const TASK = new Set(['ThreadControllerImpl::RunTask', 'RunTask']);
const GC = /^(MinorGC|MajorGC|V8\.GC_)/;

/** Inclusive totals (ms) by category. Nested events count in each category they belong to (a forced layout inside a script counts in both). */
export interface Totals {
    script: number;
    style: number;
    layout: number;
    prePaint: number;
    paint: number;
    gc: number;
}

export interface LongTask {
    /** ms from the first main-thread event of the trace. */
    atMs: number;
    durMs: number;
    /** Thread (CPU) time: much less than durMs means the thread was descheduled (a loaded machine), not busy. */
    cpuMs: number;
    /** Direct children of the task by name, ms, largest first. */
    parts: [string, number][];
    /** Layouts inside the task: ms and dirty / total layout objects. */
    layouts: { ms: number; dirty: number; total: number }[];
    /** The longest script entries: event name, the DOM event type or function name, ms. */
    scripts: { name: string; what: string; ms: number }[];
}

export interface Breakdown {
    totals: Totals;
    tasks: number;
    longTasks: LongTask[];
}

const ms = (us: number) => Math.round(us / 10) / 100;

/** The renderer main thread with the most events (the page's; others are extensions or workers). */
export function mainThread(events: readonly TraceEvent[]): string | null {
    const names = new Set<string>();
    for (const e of events) if (e.ph === 'M' && e.name === 'thread_name' && e.args?.name === 'CrRendererMain') names.add(`${e.pid}:${e.tid}`);
    const count = new Map<string, number>();
    for (const e of events) {
        const k = `${e.pid}:${e.tid}`;
        if (names.has(k)) count.set(k, (count.get(k) ?? 0) + 1);
    }
    let best: string | null = null;
    for (const [k, n] of count) if (best === null || n > count.get(best)!) best = k;
    return best;
}

export function analyzeTrace(events: readonly TraceEvent[], longTaskMs = 50): Breakdown | null {
    const main = mainThread(events);
    if (!main) return null;
    const ev = events.filter(e => e.ph === 'X' && e.dur !== undefined && `${e.pid}:${e.tid}` === main).sort((a, b) => a.ts - b.ts || b.dur! - a.dur!);
    const t0 = ev.length ? ev[0].ts : 0;
    const totals: Totals = { script: 0, style: 0, layout: 0, prePaint: 0, paint: 0, gc: 0 };
    // Script: only outermost script spans, so a function call inside an event dispatch is not counted twice.
    let scriptUntil = -Infinity;
    for (const e of ev) {
        const d = e.dur!;
        if (
            e.name === 'EventDispatch' ||
            e.name === 'FunctionCall' ||
            e.name === 'TimerFire' ||
            e.name === 'RunMicrotasks' ||
            e.name === 'FireAnimationFrame'
        ) {
            if (e.ts >= scriptUntil) {
                totals.script += d;
                scriptUntil = e.ts + d;
            }
        } else if (e.name === 'UpdateLayoutTree') totals.style += d;
        else if (e.name === 'Layout') totals.layout += d;
        else if (e.name === 'PrePaint') totals.prePaint += d;
        else if (e.name === 'Paint') totals.paint += d;
        else if (GC.test(e.name)) totals.gc += d;
    }
    for (const k of Object.keys(totals) as (keyof Totals)[]) totals[k] = ms(totals[k]);

    // Outermost tasks (a nested RunTask is part of its parent).
    const tasks: TraceEvent[] = [];
    let taskUntil = -Infinity;
    for (const e of ev)
        if (TASK.has(e.name) && e.ts >= taskUntil) {
            tasks.push(e);
            taskUntil = e.ts + e.dur!;
        }
    const longTasks: LongTask[] = [];
    for (const t of tasks) {
        if (t.dur! < longTaskMs * 1000) continue;
        const end = t.ts + t.dur!;
        const inside = ev.filter(e => e !== t && e.ts >= t.ts && e.ts + e.dur! <= end && !TASK.has(e.name));
        const parts = new Map<string, number>();
        let until = -Infinity;
        for (const e of inside)
            if (e.ts >= until) {
                parts.set(e.name, (parts.get(e.name) ?? 0) + e.dur!);
                until = e.ts + e.dur!;
            }
        longTasks.push({
            atMs: ms(t.ts - t0),
            durMs: ms(t.dur!),
            cpuMs: ms(t.tdur ?? t.dur!),
            parts: [...parts].sort((a, b) => b[1] - a[1]).map(([n, d]) => [n, ms(d)] as [string, number]),
            layouts: inside
                .filter(e => e.name === 'Layout')
                .map(e => ({
                    ms: ms(e.dur!),
                    dirty: Number(e.args?.beginData?.dirtyObjects ?? 0),
                    total: Number(e.args?.beginData?.totalObjects ?? 0)
                })),
            scripts: inside
                .filter(e => e.name === 'EventDispatch' || e.name === 'FunctionCall' || e.name === 'TimerFire')
                .sort((a, b) => b.dur! - a.dur!)
                .slice(0, 4)
                .map(e => {
                    const d = e.args?.data ?? {};
                    const what =
                        String(d.type ?? d.functionName ?? '') + (d.url ? ` ${String(d.url).split('/').pop()}:${String(d.lineNumber ?? '')}` : '');
                    return { name: e.name, what: what.trim(), ms: ms(e.dur!) };
                })
        });
    }
    return { totals, tasks: tasks.length, longTasks };
}

/** Plain-text report of one breakdown. */
export function formatBreakdown(label: string, b: Breakdown | null): string {
    if (!b) return `== ${label}: no renderer main thread in the trace`;
    const t = b.totals;
    const out = [
        `== ${label}`,
        `   main thread (ms, inclusive): script ${t.script} · style ${t.style} · layout ${t.layout} · pre-paint ${t.prePaint} · paint ${t.paint} · GC ${t.gc}`,
        `   ${b.tasks} tasks, ${b.longTasks.length} of them >= 50 ms`
    ];
    for (const lt of b.longTasks) {
        out.push(
            `   -- at +${lt.atMs} ms: ${lt.durMs} ms (CPU ${lt.cpuMs} ms${lt.cpuMs < lt.durMs * 0.7 ? ': mostly descheduled, a loaded machine' : ''})`
        );
        out.push(
            `      ${lt.parts
                .slice(0, 8)
                .map(([n, d]) => `${n} ${d}`)
                .join(' · ')}`
        );
        for (const l of lt.layouts.filter(l => l.ms >= 2)) out.push(`      Layout ${l.ms} ms, ${l.dirty} of ${l.total} objects dirty`);
        for (const s of lt.scripts.filter(s => s.ms >= 2)) out.push(`      ${s.name} ${s.what} ${s.ms} ms`);
    }
    return out.join('\n');
}
