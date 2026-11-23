import type { Locator, Page } from 'playwright';

import assert from 'node:assert/strict';
import { chromium } from 'playwright';

import { startStack } from '../perf/stack.ts';

/**
 * Elevating a cluster against the fake agent (rbac=1: the read context may not list Secrets, its linked write context may): a list
 * the agent refused loads on its own once the cluster is elevated, the status chip shows the switch at once, and reverting empties
 * the list again. Elevating once more with a slow API server (listdelay): until the rows arrive the page says it is loading, never
 * "No secrets.". Run with `yarn workspace @v8s/ui test:e2e` (builds the UI first).
 */
let current = '';
async function step<T>(name: string, fn: () => Promise<T>): Promise<T> {
    current = name;
    const t = Date.now();
    const r = await fn();
    console.log(`ok  ${name} (${Date.now() - t} ms)`);
    return r;
}

const dialogOf = (page: Page) => page.getByRole('dialog', { name: 'Choose clusters' });
const rows = (page: Page) => page.locator('.scroller .row:not(.sk)');

async function pick(trigger: Locator, name: string) {
    await trigger.click();
    await trigger.page().getByRole('option', { name, exact: true }).click();
}

async function main() {
    const browser = await chromium.launch();
    const errors: string[] = [];
    try {
        const stack = await startStack('clusters=2,pods=200,churn=0,rbac=1,listdelay=2500', 'elevate');
        try {
            const page = await browser.newPage({ viewport: { width: 1440, height: 900 } });
            page.on('pageerror', e => errors.push(e.message));

            await step('link prod-eu to prod-eu-admin and connect prod-eu alone', async () => {
                await page.goto(stack.url);
                const sel = dialogOf(page);
                await sel.waitFor({ timeout: 20000 });
                await sel.getByRole('button', { name: /^Linked contexts…/ }).click();
                const dlg = page.getByRole('dialog', { name: 'Linked contexts' });
                await dlg.waitFor();
                await pick(dlg.getByTestId('lc-read'), 'prod-eu');
                await pick(dlg.getByTestId('lc-write'), 'prod-eu-admin');
                await dlg.getByTestId('lc-link').click();
                await dlg.getByTestId('lc-current').locator('li', { hasText: 'prod-eu' }).waitFor({ timeout: 10000 });
                await dlg.getByRole('button', { name: 'Done' }).click();
                for (const box of await sel.locator('.crow input[type=checkbox]').all()) await box.setChecked(false);
                await sel.getByRole('checkbox', { name: 'Select prod-eu', exact: true }).check();
                await sel.getByRole('button', { name: 'Connect' }).click();
                await rows(page).first().waitFor({ timeout: 30000 });
            });

            await step('Secrets are refused: the page says not allowed', async () => {
                await page.evaluate(() => {
                    type App = { __vue_app__: { config: { globalProperties: { $router: { push(p: string): unknown } } } } };
                    return (document.querySelector('#app') as unknown as App).__vue_app__.config.globalProperties.$router.push('/r/v1/secrets');
                });
                await page.waitForURL(/\/r\/v1\/secrets/);
                const card = page.getByTestId('unavailable');
                await card.waitFor({ timeout: 20000 });
                assert.match((await card.textContent()) ?? '', /not allowed to list/);
            });

            await step('elevating: the chip is busy at once, then Secrets load without leaving the page', async () => {
                await page.evaluate(() => {
                    const w = window as unknown as { __busy: boolean };
                    w.__busy = false;
                    new MutationObserver(list => {
                        for (const m of list) {
                            const el = m.target as HTMLElement;
                            if (
                                el.matches?.('[data-elevate-menu="prod-eu"]') &&
                                el.getAttribute('aria-busy') === 'true' &&
                                /elevating/.test(el.textContent ?? '')
                            )
                                w.__busy = true;
                        }
                    }).observe(document.body, { subtree: true, attributes: true, attributeFilter: ['aria-busy'] });
                });
                await page.getByTestId('sync-prod-eu').click();
                await page.getByTestId('elevate-item').click();
                await page.getByTestId('elevated-tag').filter({ hasText: 'prod-eu-admin' }).waitFor({ timeout: 20000 });
                await rows(page).first().waitFor({ timeout: 20000 });
                assert.equal(
                    await page.evaluate(() => (window as unknown as { __busy: boolean }).__busy),
                    true,
                    'chip showed a busy "elevating…" state'
                );
                assert.equal(await page.getByTestId('unavailable').count(), 0);
            });

            await step('reverting: the chip returns and the list empties', async () => {
                await page.getByTestId('elevated-tag').click();
                await page.getByTestId('elevate-item').click();
                await page.getByTestId('sync-prod-eu').waitFor({ timeout: 20000 });
                await page.waitForFunction(() => document.querySelectorAll('.scroller .row:not(.sk)').length === 0, undefined, { timeout: 20000 });
            });

            await step('elevating again: Secrets say loading, never "No secrets."', async () => {
                await page.evaluate(() => {
                    const w = window as unknown as { __noRows: boolean; __loadingText: boolean; __loadingChip: boolean };
                    w.__noRows = w.__loadingText = w.__loadingChip = false;
                    let started = false;
                    const tick = () => {
                        const menu = document.querySelector('[data-elevate-menu="prod-eu"]');
                        const chip = menu?.textContent ?? '';
                        // Only judge once the cluster runs as its write context: before that "No secrets." is the true answer.
                        if (!started && /prod-eu-admin/.test(chip)) started = true;
                        if (!started) return;
                        const text = document.querySelector('[data-testid="table-empty"]')?.textContent ?? '';
                        if (/^\s*No secrets\.\s*$/.test(text)) w.__noRows = true;
                        if (/Loading secrets from prod-eu-admin/.test(text)) w.__loadingText = true;
                        if (chip.includes('loading secrets…')) w.__loadingChip = true;
                    };
                    new MutationObserver(tick).observe(document.body, { subtree: true, childList: true, characterData: true, attributes: true });
                });
                await page.getByTestId('sync-prod-eu').click();
                await page.getByTestId('elevate-item').click();
                await rows(page).first().waitFor({ timeout: 30000 });
                const seen = await page.evaluate(() => {
                    const w = window as unknown as { __noRows: boolean; __loadingText: boolean; __loadingChip: boolean };
                    return { __noRows: w.__noRows, __loadingText: w.__loadingText, __loadingChip: w.__loadingChip };
                });
                assert.equal(seen.__noRows, false, 'the empty state said "No secrets." while the list was loading');
                assert.equal(seen.__loadingText, true, 'the empty state said "Loading secrets from prod-eu-admin"');
                assert.equal(seen.__loadingChip, true, 'the cluster chip said "loading secrets…"');
            });
        } finally {
            await stack.stop();
        }
        assert.deepEqual(errors, [], 'page errors');
        console.log('elevate: all steps passed');
    } catch (e) {
        console.error(`FAILED at "${current}"`, e);
        process.exitCode = 1;
    } finally {
        await browser.close();
    }
}

void main();
