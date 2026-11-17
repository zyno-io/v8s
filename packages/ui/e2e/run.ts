import type { Page } from 'playwright';

import assert from 'node:assert/strict';
import { chromium } from 'playwright';

import { startStack } from '../perf/stack.ts';

/**
 * Smoke test of the T1 flows against the fake agent: open deployments, open a drawer, edit replicas through YAML (review, apply),
 * scale through an action, create a ConfigMap, star a kind, and run `:deploy payments` in the command bar.
 * Run with `yarn workspace @v8s/ui test:e2e` (builds the UI first).
 */
const FAKE = 'clusters=2,pods=2000,churn=0';

let current = '';
async function step<T>(name: string, fn: () => Promise<T>): Promise<T> {
    current = name;
    const t = Date.now();
    const r = await fn();
    console.log(`ok  ${name} (${Date.now() - t} ms)`);
    return r;
}

async function connect(page: Page, url: string) {
    await page.goto(url);
    const dialog = page.getByRole('dialog', { name: 'Choose clusters' });
    await dialog.waitFor({ timeout: 20000 });
    // The remembered selection is pre-checked; make sure at least the first cluster is checked and connect.
    const boxes = dialog.locator('.crow input[type=checkbox]');
    for (const box of await boxes.all()) if (!(await box.isChecked())) await box.check();
    await dialog.getByRole('button', { name: 'Connect' }).click();
    await page.locator('.scroller .row:not(.sk)').first().waitFor({ timeout: 30000 });
}

async function toast(page: Page, text: RegExp) {
    await page.getByRole('region', { name: 'Notifications' }).getByText(text).first().waitFor({ timeout: 10000 });
}

async function main() {
    const stack = await startStack(FAKE, 'e2e');
    const browser = await chromium.launch();
    const page = await browser.newPage({ viewport: { width: 1440, height: 900 } });
    const errors: string[] = [];
    page.on('pageerror', e => errors.push(e.message));
    page.on('console', m => m.type() === 'error' && errors.push(m.text()));
    try {
        await step('connect clusters', () => connect(page, stack.url));

        await step('open deployments from the sidebar', async () => {
            await page.getByRole('link', { name: /^Deployments/ }).click();
            await page.waitForURL(/\/r\/apps\/v1\/deployments/);
            await page.locator('.scroller .row:not(.sk)').first().waitFor({ timeout: 20000 });
            assert.match(await page.locator('h1').innerText(), /Deployments/);
        });

        let name = '';
        await step('open a drawer (URL reflects the object)', async () => {
            const first = page.locator('.scroller .row:not(.sk)').first();
            name = (await first.locator('.name').innerText()).trim();
            await first.locator('.name').click();
            await page.getByTestId('drawer').waitFor();
            assert.match(page.url(), /[?&]n=/);
            await page.getByTestId('drawer').getByRole('heading', { name }).waitFor();
            await page.getByRole('tab', { name: 'Overview' }).waitFor();
            await page.getByTestId('drawer').getByText('Replicas').waitFor();
        });

        await step('edit replicas via YAML, review, apply', async () => {
            await page.getByRole('tab', { name: 'YAML' }).click();
            await page.locator('.cm-content').waitFor();
            await page.getByTestId('edit-yaml').click();
            const text = await page.locator('.cm-content').innerText();
            const m = text.match(/replicas:\s*(\d+)/);
            assert.ok(m, 'replicas line present');
            const next = Number(m![1]) + 1;
            await page.locator('.cm-content').click();
            await page.keyboard.press(process.platform === 'darwin' ? 'Meta+a' : 'Control+a');
            await page.keyboard.insertText(text.replace(/replicas:\s*\d+/, `replicas: ${next}`));
            await page.getByTestId('review').click();
            await page.getByTestId('review-pane').getByText('Server dry run passed.').waitFor({ timeout: 10000 });
            await page.getByTestId('review-pane').getByText(/Live/).first().waitFor();
            await page.getByTestId('apply').click();
            await toast(page, /Applied/);
            // The live object now shows the new value.
            await page.waitForFunction(n => document.querySelector('.cm-content')?.textContent?.includes(`replicas: ${n}`), next, { timeout: 10000 });
        });

        await step('scale through the action menu', async () => {
            await page.getByTestId('actions-menu').click();
            await page.getByRole('menuitem', { name: /Scale/ }).click();
            const dlg = page.getByRole('dialog', { name: /Scale/ });
            await dlg.getByLabel('Replicas').fill('5');
            await dlg.getByRole('button', { name: 'Scale', exact: true }).click();
            await toast(page, /Scaled/);
        });

        await step('conflict: server changes under an open edit, Overwrite applies mine', async () => {
            await page.getByRole('tab', { name: 'YAML' }).click();
            await page.getByTestId('edit-yaml').click();
            const text = await page.locator('.cm-content').innerText();
            // Another change lands while the editor is open (a scale through the action menu).
            await page.getByTestId('actions-menu').click();
            await page.getByRole('menuitem', { name: /Scale/ }).click();
            const dlg = page.getByRole('dialog', { name: /Scale/ });
            await dlg.getByLabel('Replicas').fill('7');
            await dlg.getByRole('button', { name: 'Scale', exact: true }).click();
            await page.getByTestId('changed-notice').waitFor();
            await page.locator('.cm-content').click();
            await page.keyboard.press(process.platform === 'darwin' ? 'Meta+a' : 'Control+a');
            await page.keyboard.insertText(text.replace(/replicas:\s*\d+/, 'replicas: 8'));
            await page.getByTestId('review').click();
            await page
                .getByTestId('review-pane')
                .getByText(/dry run/)
                .first()
                .waitFor({ timeout: 10000 });
            await page.getByTestId('apply').click();
            await page.getByTestId('conflict-notice').waitFor({ timeout: 10000 });
            await page.getByTestId('conflict-overwrite').click();
            await toast(page, /Applied/);
            await page.waitForFunction(() => document.querySelector('.cm-content')?.textContent?.includes('replicas: 8'), null, { timeout: 10000 });
        });

        await step('close the drawer', async () => {
            await page.getByTestId('drawer-close').click();
            await page.getByTestId('drawer').waitFor({ state: 'detached' });
            assert.doesNotMatch(page.url(), /[?&]n=/);
        });

        await step('bulk delete asks for typed confirmation', async () => {
            const rows = page.locator('.scroller .row:not(.sk)');
            await rows.nth(0).locator('input.cb').check();
            await rows.nth(1).locator('input.cb').check();
            await page.getByRole('toolbar', { name: 'Bulk actions' }).getByRole('button', { name: 'Delete' }).click();
            const dlg = page.getByRole('dialog', { name: /Delete 2/ });
            const confirm = dlg.getByRole('button', { name: 'Delete' });
            assert.equal(await confirm.isDisabled(), true);
            await dlg.getByLabel('Confirmation text').fill('2');
            assert.equal(await confirm.isDisabled(), false);
            await dlg.getByRole('button', { name: 'Cancel' }).click();
            await page.getByRole('toolbar', { name: 'Bulk actions' }).getByRole('button', { name: 'Clear' }).click();
        });

        await step('create a ConfigMap', async () => {
            await page.getByTestId('create').click();
            const dlg = page.getByRole('dialog', { name: 'Create' });
            await dlg.getByTestId('create-type').selectOption('v1/configmaps');
            await dlg.locator('.cm-content').waitFor();
            const text = (await dlg.locator('.cm-content').innerText()).replace(/name: my-config/, 'name: e2e-config');
            await dlg.locator('.cm-content').click();
            await page.keyboard.press(process.platform === 'darwin' ? 'Meta+a' : 'Control+a');
            await page.keyboard.insertText(text);
            await dlg.getByTestId('dry-run').click();
            await dlg.getByTestId('dry-ok').waitFor();
            await dlg.getByTestId('create-submit').click();
            await toast(page, /Created e2e-config/);
        });

        await step('star a kind from the table header', async () => {
            const title = (await page.locator('h1').innerText()).trim();
            await page.getByTestId('star').click();
            await page.waitForFunction(() => !!document.querySelector('[data-testid=star][aria-pressed=true]'));
            const side = page.getByRole('navigation', { name: 'Resources' });
            // Starred is the first sidebar section; the item and its unstar button both appear there.
            await side
                .locator('.navrow')
                .first()
                .getByRole('link', { name: new RegExp(`^${title}`) })
                .waitFor();
            // Persisted through settings.set: a reload shows the remembered selection pre-checked with Connect.
            await page.reload();
            const dialog = page.getByRole('dialog', { name: 'Choose clusters' });
            const firstRow = page.locator('.scroller .row:not(.sk)').first();
            // The agent keeps its selection per process, so the page may already be connected.
            await Promise.race([dialog.waitFor({ timeout: 20000 }), firstRow.waitFor({ timeout: 20000 })]);
            if (await dialog.isVisible()) await dialog.getByRole('button', { name: 'Connect' }).click();
            await firstRow.waitFor({ timeout: 20000 });
            await side
                .locator('.navrow')
                .first()
                .getByRole('link', { name: new RegExp(`^${title}`) })
                .waitFor();
        });

        await step('bookmark the current view and open it from the sidebar', async () => {
            await page.getByTestId('bookmark-add').click();
            await page.getByLabel('Bookmark name').fill('my configmaps');
            await page.getByRole('button', { name: 'Save' }).click();
            await page.getByRole('link', { name: 'my configmaps' }).waitFor();
            await page.getByRole('link', { name: /^Pods/ }).first().click();
            await page.waitForURL(/\/pods/);
            await page.getByRole('link', { name: 'my configmaps' }).click();
            await page.waitForURL(/\/r\/v1\/configmaps/);
        });

        await step('command bar: :unstar removes the star', async () => {
            await page.keyboard.press(':');
            await page.getByLabel('Command', { exact: true }).fill('unstar');
            await page.getByLabel('Command', { exact: true }).press('Enter');
            await page.waitForFunction(() => !document.querySelector('[data-testid=star][aria-pressed=true]'));
        });

        await step('command bar: :deploy payments', async () => {
            await page.getByRole('link', { name: /^Pods/ }).first().click();
            await page.waitForURL(/\/pods/);
            await page.keyboard.press(':');
            await page.getByLabel('Command', { exact: true }).fill('deploy payments');
            await page.getByLabel('Command', { exact: true }).press('Enter');
            await page.waitForURL(/\/r\/apps\/v1\/deployments/);
            await page.getByRole('button', { name: /Namespace: payments/ }).waitFor();
        });

        await step('global search opens the object drawer', async () => {
            await page.keyboard.press(process.platform === 'darwin' ? 'Meta+k' : 'Control+k');
            const input = page.getByTestId('palette-input');
            await input.waitFor();
            await input.fill(name.slice(0, 6));
            const opt = page.getByRole('option').filter({ hasText: name }).first();
            await opt.waitFor({ timeout: 10000 });
            await input.press('Enter');
            await page.getByTestId('drawer').waitFor();
        });

        assert.deepEqual(errors, [], 'no console or page errors');
        console.log('e2e: all flows passed');
    } catch (e) {
        console.error(`e2e failed at "${current}":`, e);
        await page.screenshot({ path: process.env.E2E_SHOT ?? 'e2e-failure.png' }).catch(() => {});
        process.exitCode = 1;
    } finally {
        await browser.close();
        await stack.stop();
    }
}

void main();
