import type { Browser, CDPSession, Page } from 'playwright';

import { mkdirSync, writeFileSync } from 'node:fs';
import { dirname, join, resolve } from 'node:path';
import { chromium } from 'playwright';

import { startStack, uiDir } from './stack.ts';

const FAKE = 'clusters=3,pods=50000,churn=1000';
const SCROLL_MS = 10_000;
const CHURN_MS = 10_000;

/** Budgets (docs/performance.md §11). Asserted on desktop only. */
const BUDGETS = { scrollFrameP95Ms: 20, longTaskMs: 50, heapMB: 80, scriptingP95Ms: 4 };

/** Variants to run (comma list). The A/B check runs `desktop` only. */
const VARIANTS = (process.env.PERF_VARIANTS || 'desktop,mobile-4x')
    .split(',')
    .map(s => s.trim())
    .filter(Boolean);
const NO_ASSERT = process.env.PERF_NO_ASSERT === '1';

type Phase = 'idle' | 'scroll' | 'filter' | 'churn';

interface PerfWindow {
    __perf: { phase: Phase; frames: [Phase, number][]; longtasks: [Phase, number, number][] };
}

const INIT = `
(() => {
  const p = { phase: 'idle', frames: [], longtasks: [] };
  window.__perf = p;
  let last = 0;
  const loop = t => { if (last) p.frames.push([p.phase, t - last]); last = t; requestAnimationFrame(loop); };
  requestAnimationFrame(loop);
  try {
    new PerformanceObserver(list => { for (const e of list.getEntries()) p.longtasks.push([p.phase, e.duration, e.startTime]); }).observe({ type: 'longtask', buffered: true });
  } catch {}
})();
`;

function pct(values: number[], q: number): number {
    if (!values.length) return 0;
    const s = [...values].sort((a, b) => a - b);
    return s[Math.min(s.length - 1, Math.floor(q * s.length))];
}
const round = (n: number, d = 2) => Math.round(n * 10 ** d) / 10 ** d;

/** Talks to dedicated-worker targets through the browser session (Playwright exposes no per-worker CDPSession). */
class WorkerHeap {
    private nextId = 1;
    private waiting = new Map<number, (r: { result?: Record<string, number> }) => void>();
    private sessions = new Map<string, string>(); // targetId -> sessionId

    private browser: CDPSession;

    constructor(browser: CDPSession) {
        this.browser = browser;
        browser.on('Target.receivedMessageFromTarget', (e: { message: string }) => {
            const m = JSON.parse(e.message) as { id?: number; result?: Record<string, number> };
            if (m.id !== undefined) this.waiting.get(m.id)?.(m);
        });
    }

    private async sendTo(sessionId: string, method: string): Promise<{ result?: Record<string, number> }> {
        const id = this.nextId++;
        const p = new Promise<{ result?: Record<string, number> }>(res => this.waiting.set(id, res));
        await this.browser.send('Target.sendMessageToTarget', { sessionId, message: JSON.stringify({ id, method }) });
        return p;
    }

    /** Sum of used JS heap (MB) across all live worker targets, optionally after a GC in each. */
    async usedMB(gc: boolean): Promise<number> {
        const { targetInfos } = await this.browser.send('Target.getTargets');
        let total = 0;
        for (const t of targetInfos) {
            if (t.type !== 'worker') continue;
            let sid = this.sessions.get(t.targetId);
            if (!sid) {
                sid = (await this.browser.send('Target.attachToTarget', { targetId: t.targetId, flatten: false })).sessionId;
                this.sessions.set(t.targetId, sid);
            }
            if (gc) await this.sendTo(sid, 'HeapProfiler.collectGarbage');
            total += (await this.sendTo(sid, 'Runtime.getHeapUsage')).result?.usedSize ?? 0;
        }
        return total / 1048576;
    }
}

async function heapMB(cdp: CDPSession, workers: WorkerHeap, gc: boolean): Promise<{ page: number; workers: number; total: number }> {
    if (gc) await cdp.send('HeapProfiler.collectGarbage');
    const { metrics } = await cdp.send('Performance.getMetrics');
    const page = (metrics.find(m => m.name === 'JSHeapUsedSize')?.value ?? 0) / 1048576;
    const w = await workers.usedMB(gc);
    return { page, workers: w, total: page + w };
}

interface TraceEvent {
    ph: string;
    name: string;
    ts: number;
    dur?: number;
    pid: number;
    tid: number;
}

const SCRIPTING = new Set(['FunctionCall', 'EvaluateScript', 'RunMicrotasks', 'TimerFire', 'FireAnimationFrame']);

/** Per-frame main-thread scripting ms. A frame runs from one FireAnimationFrame start to the next; nested events count once. */
function scriptingPerFrame(trace: { traceEvents: TraceEvent[] }): number[] {
    const evs = trace.traceEvents.filter(e => SCRIPTING.has(e.name) && (e.ph === 'X' || e.ph === 'B' || e.ph === 'E'));
    // The page's renderer main thread is the one running the most animation frames (our rAF loop).
    const raf = new Map<string, number>();
    for (const e of evs) if (e.name === 'FireAnimationFrame' && e.ph !== 'E') raf.set(`${e.pid}:${e.tid}`, (raf.get(`${e.pid}:${e.tid}`) ?? 0) + 1);
    const main = [...raf.entries()].sort((a, b) => b[1] - a[1])[0]?.[0];
    if (!main) return [];
    const spans: { start: number; end: number; name: string }[] = [];
    const stack: TraceEvent[] = [];
    for (const e of evs.filter(e => `${e.pid}:${e.tid}` === main).sort((a, b) => a.ts - b.ts)) {
        if (e.ph === 'X') spans.push({ start: e.ts, end: e.ts + (e.dur ?? 0), name: e.name });
        else if (e.ph === 'B') stack.push(e);
        else {
            const b = stack.pop();
            if (b) spans.push({ start: b.ts, end: e.ts, name: b.name });
        }
    }
    spans.sort((a, b) => a.start - b.start || b.end - a.end);
    const frameStarts = spans.filter(s => s.name === 'FireAnimationFrame').map(s => s.start);
    const perFrame = Array.from({ length: frameStarts.length }, () => 0);
    let coveredUntil = -Infinity;
    let fi = 0;
    for (const s of spans) {
        if (s.start < coveredUntil) continue; // nested in an already counted span
        coveredUntil = s.end;
        while (fi + 1 < frameStarts.length && frameStarts[fi + 1] <= s.start) fi++;
        if (frameStarts.length && s.start >= frameStarts[0]) perFrame[fi] += (s.end - s.start) / 1000;
    }
    return perFrame;
}

async function setPhase(page: Page, phase: Phase) {
    await page.evaluate(p => ((window as unknown as PerfWindow).__perf.phase = p), phase);
}

async function connectAll(page: Page, url: string) {
    await page.goto(url);
    const dialog = page.getByRole('dialog', { name: 'Choose clusters' });
    const firstRow = page.locator('.scroller .row:not(.sk), .scroller .pcard:not(.sk)').first();
    // The agent keeps the cluster selection per process, so a later page load may already be connected.
    await Promise.race([dialog.waitFor({ timeout: 20000 }), firstRow.waitFor({ timeout: 20000 })]);
    if (await dialog.isVisible()) {
        const boxes = dialog.locator('.crow input[type=checkbox]');
        for (const box of await boxes.all()) if (!(await box.isChecked())) await box.check();
        await dialog.getByRole('button', { name: 'Connect' }).click();
    }
    await firstRow.waitFor({ timeout: 30000 });
}

/** Waits until the unfiltered total stops growing (full sync). */
async function waitSynced(page: Page, expected: number): Promise<number> {
    const read = () => page.evaluate(() => Number(document.querySelector('.scroller')?.getAttribute('aria-rowcount') ?? 0) - 1);
    let last = -1;
    let stable = 0;
    const start = Date.now();
    while (Date.now() - start < 60000) {
        const n = await read();
        if (n >= expected || (n === last && n > 0 && ++stable >= 6)) return n;
        if (n !== last) stable = 0;
        last = n;
        await page.waitForTimeout(250);
    }
    return last;
}

interface Variant {
    name: string;
    mobile: boolean;
    cpuThrottle: number;
    assert: boolean;
}

async function runVariant(browser: Browser, v: Variant, url: string) {
    const ctx = await browser.newContext(
        v.mobile
            ? { viewport: { width: 390, height: 844 }, deviceScaleFactor: 2, isMobile: true, hasTouch: true }
            : { viewport: { width: 1440, height: 900 }, deviceScaleFactor: 1 }
    );
    await ctx.addInitScript(INIT);
    const page = await ctx.newPage();
    page.on('console', m => m.type() === 'error' && console.error(`[${v.name}] console`, m.text()));
    page.on('pageerror', e => console.error(`[${v.name}] pageerror`, e.message));
    // Patch frames received per phase (proves churn reaches the client). Playwright only sees page-level sockets, so this may be 0 for
    // the worker-owned socket; the UI-side check is the summary/row updates below.
    let phaseNow: Phase = 'idle';
    const wsFrames: Record<string, number> = {};
    page.on('websocket', ws => ws.on('framereceived', () => (wsFrames[phaseNow] = (wsFrames[phaseNow] ?? 0) + 1)));
    const cdp = await ctx.newCDPSession(page);
    await cdp.send('Performance.enable');
    const workers = new WorkerHeap(await browser.newBrowserCDPSession());
    if (v.cpuThrottle > 1) await cdp.send('Emulation.setCPUThrottlingRate', { rate: v.cpuThrottle });

    const t0 = Date.now();
    await connectAll(page, url);
    const firstRowsMs = Date.now() - t0;
    const total = await waitSynced(page, 50000);
    const rowH = v.mobile ? 72 : 32;
    const heapSamples: { phase: string; mb: number; pageMB: number; workerMB: number }[] = [];
    const sampleHeap = async (phase: string, gc = false) => {
        const h = await heapMB(cdp, workers, gc);
        heapSamples.push({ phase, mb: round(h.total, 1), pageMB: round(h.page, 1), workerMB: round(h.workers, 1) });
    };
    await sampleHeap('synced', true);

    const phase = async (p: Phase) => {
        phaseNow = p;
        await setPhase(page, p);
    };
    const scroller = page.locator('.scroller');
    const box = (await scroller.boundingBox())!;
    await page.mouse.move(box.x + box.width / 2, box.y + box.height / 2);

    // (a) wheel-scroll through the whole table over ~10 s.
    await phase('scroll');
    await browser.startTracing(page, { categories: ['devtools.timeline'] });
    const distance = total * rowH - box.height;
    const start = Date.now();
    let sent = 0;
    while (Date.now() - start < SCROLL_MS) {
        const target = distance * Math.min(1, (Date.now() - start) / SCROLL_MS);
        const dy = target - sent;
        if (dy >= 1) {
            await page.mouse.wheel(0, dy);
            sent += dy;
        }
        await page.waitForTimeout(8);
    }
    await page.waitForTimeout(300);
    const trace = JSON.parse((await browser.stopTracing()).toString()) as { traceEvents: TraceEvent[] };
    const scriptFrames = scriptingPerFrame(trace);
    await sampleHeap('after-scroll', true);
    const skeletonsAtEnd = await page.locator('.scroller .sk').count();
    const bottomRows = await page.locator('.scroller .row:not(.sk), .scroller .pcard:not(.sk)').count();

    // (b) type into the filter.
    await scroller.evaluate(el => (el.scrollTop = 0));
    await page.waitForTimeout(300);
    await phase('filter');
    const filter = page.locator('#pods-filter');
    await filter.click();
    const typeStart = Date.now();
    const finalText = 'svc-1';
    await filter.pressSequentially(finalText, { delay: 80 });
    // Settled = every rendered name contains the final text (not just the first keystroke's result) and the summary shows matches.
    const settled = await page
        .waitForFunction(
            text => {
                const names = [...document.querySelectorAll('.scroller .row:not(.sk) .name, .scroller .pcard:not(.sk) .name')];
                return (
                    /match/.test(document.querySelector('.summary')?.textContent ?? '') &&
                    names.length > 0 &&
                    names.every(n => (n.textContent ?? '').toLowerCase().includes(text))
                );
            },
            finalText,
            { timeout: 10000, polling: 25 }
        )
        .then(() => true)
        .catch(() => false);
    const filterSettleMs = settled ? Date.now() - typeStart : -1;
    const matchText = (await page.locator('.summary').textContent())?.trim() ?? '';
    await sampleHeap('after-filter', true);
    await filter.fill('');
    await page.waitForTimeout(800);

    // (c) let churn run.
    await phase('churn');
    const sig = () => page.locator('.scroller').innerText();
    const chipsBefore = await page.locator('.chip').allInnerTexts();
    const before = await sig();
    await page.waitForTimeout(CHURN_MS);
    const churnVisibleChange = before !== (await sig()) || chipsBefore.join() !== (await page.locator('.chip').allInnerTexts()).join();
    await phase('idle');
    await sampleHeap('after-churn', true);

    const perf = await page.evaluate(() => (window as unknown as PerfWindow).__perf);
    const phases: Record<string, unknown> = {};
    for (const ph of ['scroll', 'filter', 'churn'] as Phase[]) {
        const f = perf.frames.filter(x => x[0] === ph).map(x => x[1]);
        const lt = perf.longtasks.filter(x => x[0] === ph).map(x => x[1]);
        phases[ph] = {
            frames: f.length,
            frameP50Ms: round(pct(f, 0.5)),
            frameP95Ms: round(pct(f, 0.95)),
            frameP99Ms: round(pct(f, 0.99)),
            frameMaxMs: round(Math.max(0, ...f)),
            droppedPct: round(f.length ? (100 * f.filter(x => x > 25).length) / f.length : 0, 1),
            longTasks: lt.length,
            longTaskMaxMs: round(Math.max(0, ...lt)),
            longTaskTotalMs: round(lt.reduce((a, b) => a + b, 0))
        };
    }
    const scriptingP95Ms = round(pct(scriptFrames, 0.95));
    const scripting = {
        frames: scriptFrames.length,
        p50Ms: round(pct(scriptFrames, 0.5)),
        p95Ms: scriptingP95Ms,
        totalMs: round(scriptFrames.reduce((a, b) => a + b, 0)),
        meanMs: round(scriptFrames.length ? scriptFrames.reduce((a, b) => a + b, 0) / scriptFrames.length : 0),
        maxMs: round(Math.max(0, ...scriptFrames))
    };
    const peakHeapMB = Math.max(...heapSamples.map(s => s.mb));

    const checks: { name: string; ok: boolean; actual: number; budget: number }[] = [];
    const scrollP95 = (phases.scroll as { frameP95Ms: number }).frameP95Ms;
    const lt = ['scroll', 'churn'].map(p => (phases[p] as { longTaskMaxMs: number }).longTaskMaxMs);
    checks.push({ name: 'scroll frame p95 (ms)', ok: scrollP95 <= BUDGETS.scrollFrameP95Ms, actual: scrollP95, budget: BUDGETS.scrollFrameP95Ms });
    checks.push({
        name: 'scripting p95 per frame during scroll (ms)',
        ok: scriptingP95Ms <= BUDGETS.scriptingP95Ms,
        actual: scriptingP95Ms,
        budget: BUDGETS.scriptingP95Ms
    });
    checks.push({
        name: 'max long task during scroll/churn (ms)',
        ok: Math.max(...lt) <= BUDGETS.longTaskMs,
        actual: Math.max(...lt),
        budget: BUDGETS.longTaskMs
    });
    checks.push({
        name: 'JS heap (page + workers) after GC, peak (MB)',
        ok: peakHeapMB <= BUDGETS.heapMB,
        actual: peakHeapMB,
        budget: BUDGETS.heapMB
    });

    if (process.env.PERF_SHOTS) {
        mkdirSync(process.env.PERF_SHOTS, { recursive: true });
        await page.screenshot({ path: join(process.env.PERF_SHOTS, `${v.name}.png`) });
    }
    await ctx.close();

    return {
        name: v.name,
        asserted: v.assert,
        cpuThrottle: v.cpuThrottle,
        viewport: v.mobile ? '390x844' : '1440x900',
        totalRows: total,
        firstRowsMs,
        scrollDistancePx: Math.round(distance),
        skeletonsAtEnd,
        bottomRowsRendered: bottomRows,
        filterSettleMs,
        filterResult: matchText,
        churnVisibleChange,
        pageSocketFrames: wsFrames,
        heapMB: heapSamples,
        scripting,
        phases,
        checks
    };
}

async function main() {
    const stack = await startStack(FAKE);
    const browser = await chromium.launch({ headless: true, args: ['--enable-precise-memory-info'] });
    const results = [];
    try {
        const all: Variant[] = [
            { name: 'desktop', mobile: false, cpuThrottle: 1, assert: true },
            { name: 'mobile-4x', mobile: true, cpuThrottle: 4, assert: false }
        ];
        const unknown = VARIANTS.filter(n => !all.some(v => v.name === n));
        if (unknown.length) throw new Error(`unknown PERF_VARIANTS: ${unknown.join(', ')}`);
        for (const v of all.filter(v => VARIANTS.includes(v.name))) results.push(await runVariant(browser, v, stack.url));
    } finally {
        await browser.close();
        await stack.stop();
    }
    const out = { when: new Date().toISOString(), fake: FAKE, budgets: BUDGETS, results };
    const file = process.env.PERF_OUT ? resolve(process.env.PERF_OUT) : join(uiDir, 'test-results', 'perf.json');
    mkdirSync(dirname(file), { recursive: true });
    writeFileSync(file, JSON.stringify(out, null, 2));
    console.log(JSON.stringify(out, null, 2));
    const failed = results
        .filter(r => r.asserted)
        .flatMap(r => r.checks.filter(c => !c.ok).map(c => `${r.name}: ${c.name} ${c.actual} > ${c.budget}`));
    if (failed.length && !NO_ASSERT) {
        console.error('PERF BUDGETS EXCEEDED:\n' + failed.join('\n'));
        process.exit(1);
    }
    console.log(`perf budgets met (results in ${file})`);
    process.exit(0);
}

main().catch(err => {
    console.error(err);
    process.exit(1);
});
