// Profiles the Pods table the way the perf suite drives it, and says where the main thread's time goes.
//
//   yarn workspace @v8s/ui perf:profile [--throttle 2] [--rounds 1] [--out test-results/profile]
//
// Builds the UI, starts the fake agent (50k pods over 3 clusters, churn 1000/s), then per round: a 10 s wheel scroll
// through the whole table and 10 s of churn, each traced. Prints the main thread's time by category and a breakdown of
// every task of 50 ms or more, and saves the traces (load them in DevTools' Performance panel for the full picture).
// The A/B check (perf/ab.ts) says that a PR is slower; this says why. A CPU throttle of 2-4 makes the long tasks of a
// slower machine show up on a fast one.
import { mkdirSync, writeFileSync } from 'node:fs';
import { join, resolve } from 'node:path';
import { parseArgs } from 'node:util';
import { chromium } from 'playwright';

import { connectAll, waitSynced } from './page.ts';
import { startStack, uiDir } from './stack.ts';
import { analyzeTrace, formatBreakdown, type TraceEvent } from './trace.ts';

const FAKE = 'clusters=3,pods=50000,churn=1000';
const CATEGORIES = [
    'devtools.timeline',
    'disabled-by-default-devtools.timeline',
    'disabled-by-default-devtools.timeline.frame',
    'v8',
    'v8.execute',
    'disabled-by-default-v8.gc',
    'toplevel'
];

const { values } = parseArgs({
    options: {
        throttle: { type: 'string', default: '2' },
        rounds: { type: 'string', default: '1' },
        out: { type: 'string', default: join(uiDir, 'test-results', 'profile') }
    }
});
const throttle = Number(values.throttle);
const rounds = Number(values.rounds);
const outDir = resolve(values.out!);
mkdirSync(outDir, { recursive: true });

const stack = await startStack(FAKE);
const browser = await chromium.launch({ headless: true });
const report: string[] = [];
try {
    for (let round = 1; round <= rounds; round++) {
        const ctx = await browser.newContext({ viewport: { width: 1440, height: 900 }, deviceScaleFactor: 1 });
        const page = await ctx.newPage();
        page.on('pageerror', e => console.error('pageerror', e.message));
        const cdp = await ctx.newCDPSession(page);
        if (throttle > 1) await cdp.send('Emulation.setCPUThrottlingRate', { rate: throttle });
        await connectAll(page, stack.url);
        const total = await waitSynced(page, 50000);
        const scroller = page.locator('.scroller');
        const box = (await scroller.boundingBox())!;
        await page.mouse.move(box.x + box.width / 2, box.y + box.height / 2);

        const traced = async (phase: string, drive: () => Promise<void>) => {
            await browser.startTracing(page, { categories: CATEGORIES });
            await page.waitForTimeout(500); // let tracing start before the measured work
            await drive();
            const trace = JSON.parse((await browser.stopTracing()).toString()) as { traceEvents: TraceEvent[] };
            const file = join(outDir, `${phase}-${round}.json`);
            writeFileSync(file, JSON.stringify(trace));
            report.push(formatBreakdown(`round ${round} ${phase} (CPU throttle ${throttle}x) → ${file}`, analyzeTrace(trace.traceEvents)));
        };

        // The perf suite's scroll: the whole table in 10 s, a wheel event every ~8 ms.
        await traced('scroll', async () => {
            const distance = total * 32 - box.height;
            const start = Date.now();
            let sent = 0;
            while (Date.now() - start < 10_000) {
                const dy = distance * Math.min(1, (Date.now() - start) / 10_000) - sent;
                if (dy >= 1) {
                    await page.mouse.wheel(0, dy);
                    sent += dy;
                }
                await page.waitForTimeout(8);
            }
            await page.waitForTimeout(300);
        });
        await scroller.evaluate(el => (el.scrollTop = 0));
        await page.waitForTimeout(500);
        await traced('churn', () => page.waitForTimeout(10_000));
        await ctx.close();
    }
} finally {
    await browser.close();
    await stack.stop();
}
console.log(report.join('\n\n'));
