import { mkdirSync, writeFileSync } from 'node:fs';
import { join } from 'node:path';
import { chromium } from 'playwright';

import { startStack, uiDir } from './stack.ts';

/**
 * Log flood scenario (docs/performance.md section 8 and 11): a followed log stream at 5k lines/s for 10 s. Budgets: main thread
 * at most 30% busy, no long task over 50 ms. The ring buffer, ANSI parsing and search live in the Data Worker, so the main thread only
 * paints the viewport once per frame.
 */
const RATE = 5000;
const FAKE = `clusters=1,pods=200,churn=0,lograte=${RATE}`;
const FLOOD_MS = 10_000;
const BUDGETS = { busyFraction: 0.3, longTaskMs: 50, minRate: 3000 };

const INIT = `
(() => {
  const p = { longtasks: [] };
  window.__lf = p;
  try {
    new PerformanceObserver(list => { for (const e of list.getEntries()) p.longtasks.push([e.duration, e.startTime]); }).observe({ type: 'longtask', buffered: true });
  } catch {}
})();
`;

async function main() {
    const stack = await startStack(FAKE, 'logflood');
    const browser = await chromium.launch({ headless: true });
    let code = 0;
    try {
        const ctx = await browser.newContext({ viewport: { width: 1440, height: 900 } });
        await ctx.addInitScript(INIT);
        const page = await ctx.newPage();
        page.on('pageerror', e => console.error('pageerror', e.message));
        const cdp = await ctx.newCDPSession(page);
        await cdp.send('Performance.enable');

        await page.goto(stack.url);
        const dialog = page.getByRole('dialog', { name: 'Choose clusters' });
        const firstRow = page.locator('.scroller .row:not(.sk)').first();
        await Promise.race([dialog.waitFor({ timeout: 20000 }), firstRow.waitFor({ timeout: 20000 })]);
        if (await dialog.isVisible()) await dialog.getByRole('button', { name: 'Connect' }).click();
        await firstRow.waitFor({ timeout: 30000 });
        await firstRow.locator('.name').click();
        await page.getByTestId('drawer').waitFor();
        await page.getByRole('tab', { name: 'Logs' }).click();
        await page.locator('.lrow').first().waitFor({ timeout: 20000 });
        await page.waitForTimeout(1500); // initial tail and first paints settle

        const seqOf = () =>
            page.evaluate(() => {
                const rows = [...document.querySelectorAll('.lrow')];
                for (let i = rows.length - 1; i >= 0; i--) {
                    const m = /seq=(\d+)/.exec(rows[i].textContent ?? '');
                    if (m) return Number(m[1]);
                }
                return 0;
            });
        const taskSeconds = async () => {
            const { metrics } = await cdp.send('Performance.getMetrics');
            return metrics.find(m => m.name === 'TaskDuration')?.value ?? 0;
        };

        const lt0 = await page.evaluate(() => (window as unknown as { __lf: { longtasks: unknown[] } }).__lf.longtasks.length);
        const seq0 = await seqOf();
        const t0 = await taskSeconds();
        const w0 = Date.now();
        await page.waitForTimeout(FLOOD_MS);
        const elapsed = (Date.now() - w0) / 1000;
        const busy = ((await taskSeconds()) - t0) / elapsed;
        const seq1 = await seqOf();
        const longtasks = await page.evaluate(
            n => (window as unknown as { __lf: { longtasks: [number, number][] } }).__lf.longtasks.slice(n).map(x => x[0]),
            lt0
        );
        const rate = (seq1 - seq0) / elapsed;
        const longMax = Math.max(0, ...longtasks);
        const lastRows = await page.locator('.lrow').count();

        const result = {
            when: new Date().toISOString(),
            fake: FAKE,
            seconds: elapsed,
            linesPerSecond: Math.round(rate),
            mainThreadBusy: Math.round(busy * 1000) / 1000,
            longTasks: longtasks.length,
            longTaskMaxMs: Math.round(longMax),
            rowsRendered: lastRows,
            budgets: BUDGETS
        };
        mkdirSync(join(uiDir, 'test-results'), { recursive: true });
        writeFileSync(join(uiDir, 'test-results', 'logflood.json'), JSON.stringify(result, null, 2));
        console.log(JSON.stringify(result, null, 2));
        const failed: string[] = [];
        if (rate < BUDGETS.minRate)
            failed.push(`only ${Math.round(rate)} lines/s reached the viewer (need >= ${BUDGETS.minRate}; the scenario did not flood)`);
        if (busy > BUDGETS.busyFraction) failed.push(`main thread ${Math.round(busy * 100)}% busy > ${BUDGETS.busyFraction * 100}%`);
        if (longMax > BUDGETS.longTaskMs) failed.push(`long task ${Math.round(longMax)} ms > ${BUDGETS.longTaskMs} ms`);
        if (failed.length) {
            console.error('LOG FLOOD BUDGETS EXCEEDED:\n' + failed.join('\n'));
            code = 1;
        } else console.log('log flood budgets met');
    } catch (e) {
        console.error(e);
        code = 1;
    } finally {
        await browser.close();
        await stack.stop();
    }
    process.exit(code);
}

void main();
