import type { Page } from 'playwright';

import assert from 'node:assert/strict';
import { chromium } from 'playwright';

import { startStack } from '../perf/stack.ts';

/**
 * Per-browser memory against the fake agent: the cluster selection (stored as a group name on an exact match), the last visited page,
 * namespace filters per cluster set, and the single-cluster auto-connect.
 * Run with `yarn workspace @v8s/ui test:e2e` (builds the UI first).
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
const firstRow = (page: Page) => page.locator('.scroller .row:not(.sk)').first();
const stored = (page: Page, key: string) => page.evaluate(k => localStorage.getItem(k), key);

/** Checks exactly the clusters at `idx` in the open selector. */
async function check(page: Page, idx: number[]) {
    const boxes = await dialogOf(page).locator('.crow input[type=checkbox]').all();
    for (let i = 0; i < boxes.length; i++) await boxes[i].setChecked(idx.includes(i));
}
const checkedIdx = async (page: Page) => {
    const boxes = await dialogOf(page).locator('.crow input[type=checkbox]').all();
    const out: number[] = [];
    for (let i = 0; i < boxes.length; i++) if (await boxes[i].isChecked()) out.push(i);
    return out;
};

async function main() {
    const browser = await chromium.launch();
    const errors: string[] = [];
    try {
        const stack = await startStack('clusters=3,pods=300,churn=0', 'remember');
        try {
            const ctx = await browser.newContext({ viewport: { width: 1440, height: 900 } });
            const page = await ctx.newPage();
            page.on('pageerror', e => errors.push(e.message));

            await step('connect two clusters, reload: the same two are pre-checked and nothing is connected', async () => {
                await page.goto(stack.url);
                await dialogOf(page).waitFor({ timeout: 20000 });
                await check(page, [0, 2]);
                await dialogOf(page).getByRole('button', { name: 'Connect' }).click();
                await firstRow(page).waitFor({ timeout: 30000 });
                assert.deepEqual(JSON.parse((await stored(page, 'v8s:selection'))!), { ids: JSON.parse((await stored(page, 'v8s:selection'))!).ids });
                // The agent keeps its selection per process: restart it so the reload starts with nothing connected (localStorage must supply the pre-check).
                await stack.restartAgent();
                await page.reload();
                await dialogOf(page).waitFor({ timeout: 20000 });
                assert.deepEqual(await checkedIdx(page), [0, 2]);
                assert.equal(await page.locator('.scroller .row:not(.sk)').count(), 0);
                await dialogOf(page).getByText('remembered from last time').waitFor();
            });

            await step('an exact group match is stored as the group name', async () => {
                await dialogOf(page).getByRole('button', { name: '+ New group' }).click();
                await dialogOf(page).getByLabel('New group name').fill('duo');
                await dialogOf(page)
                    .getByRole('button', { name: /Create from 2 checked/ })
                    .click();
                await dialogOf(page).locator('.chip', { hasText: 'duo' }).waitFor();
                await dialogOf(page).getByRole('button', { name: 'Connect' }).click();
                await firstRow(page).waitFor({ timeout: 30000 });
                assert.deepEqual(JSON.parse((await stored(page, 'v8s:selection'))!), { group: 'duo' });
            });

            await step('namespace filter is restored when switching cluster sets and back', async () => {
                await page.goto(new URL('/pods', stack.url).href + new URL(stack.url).hash);
                await firstRow(page).waitFor({ timeout: 30000 });
                await page.getByRole('button', { name: /Namespace: all/ }).click();
                const opt = page.getByRole('dialog', { name: 'Namespace filter' }).locator('label.opt').first();
                const ns = (await opt.innerText()).trim();
                await opt.locator('input').check();
                await page.keyboard.press('Escape');
                await page.getByRole('button', { name: new RegExp(`Namespace: ${ns}`) }).waitFor();
                assert.ok(JSON.parse((await stored(page, 'v8s:namespaces'))!).some((e: { key: string }) => e.key === 'g:duo'));
                // Switch to a different set (one cluster): no filter.
                await page.locator('.cluster-btn').click();
                await dialogOf(page).waitFor();
                await check(page, [1]);
                await dialogOf(page).getByRole('button', { name: 'Connect' }).click();
                await page.getByRole('button', { name: /Namespace: all/ }).waitFor({ timeout: 20000 });
                // Back to the group: the filter returns.
                await page.locator('.cluster-btn').click();
                await dialogOf(page).waitFor();
                await dialogOf(page).locator('.chip', { hasText: 'duo' }).click();
                await dialogOf(page).getByRole('button', { name: 'Connect' }).click();
                await page.getByRole('button', { name: new RegExp(`Namespace: ${ns}`) }).waitFor({ timeout: 20000 });
            });

            await step('opening / in a fresh page load lands on the last page; in-app / still works', async () => {
                await page.getByRole('link', { name: /^Deployments/ }).click();
                await page.waitForURL(/\/r\/apps\/v1\/deployments/);
                const hash = new URL(stack.url).hash;
                await page.goto(new URL('/', stack.url).href + hash);
                await page.waitForURL(/\/r\/apps\/v1\/deployments/, { timeout: 20000 });
                // In-app navigation to / is not restored again: it goes to the default page, Pods.
                await page.evaluate(() => {
                    type App = { __vue_app__: { config: { globalProperties: { $router: { push(p: string): unknown } } } } };
                    return (document.querySelector('#app') as unknown as App).__vue_app__.config.globalProperties.$router.push('/');
                });
                await page.waitForURL(/\/pods/, { timeout: 20000 });
            });

            await ctx.close();

            await step('storage unavailable: the app still boots and connects', async () => {
                await stack.restartAgent();
                const c2 = await browser.newContext();
                await c2.addInitScript(() => {
                    Object.defineProperty(window, 'localStorage', {
                        get() {
                            throw new Error('denied');
                        }
                    });
                });
                const p = await c2.newPage();
                await p.goto(stack.url);
                await dialogOf(p).waitFor({ timeout: 20000 });
                await dialogOf(p).getByRole('button', { name: 'Connect' }).click();
                await firstRow(p).waitFor({ timeout: 30000 });
                await c2.close();
            });
        } finally {
            await stack.stop();
        }

        const single = await startStack('clusters=1,pods=100,churn=0', 'remember');
        try {
            await step('a single cluster connects without the selector', async () => {
                const p = await browser.newPage({ viewport: { width: 1440, height: 900 } });
                await p.goto(single.url);
                await firstRow(p).waitFor({ timeout: 30000 });
                assert.equal(await dialogOf(p).count(), 0);
                await p.close();
            });
        } finally {
            await single.stop();
        }
        assert.deepEqual(errors, [], 'page errors');
        console.log('remember: all steps passed');
    } catch (e) {
        console.error(`FAILED at "${current}"`, e);
        process.exitCode = 1;
    } finally {
        await browser.close();
    }
}

void main();
