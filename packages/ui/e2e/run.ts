import type { Locator, Page } from 'playwright';

import assert from 'node:assert/strict';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { chromium } from 'playwright';

import { startStack } from '../perf/stack.ts';

/**
 * Smoke test of the T1 flows against the fake agent: open deployments, open a drawer, edit replicas through YAML (review, apply),
 * scale through an action, create a ConfigMap, star a kind, and run `:deploy payments` in the command bar.
 * Run with `yarn workspace @v8s/ui test:e2e` (builds the UI first).
 */
const FAKE = 'clusters=2,pods=2000,churn=0,lograte=200';

let current = '';
const shotDir = process.env.E2E_SHOT_DIR ?? tmpdir();
const shot = (name: string) => join(shotDir, `e2e-${name}.png`);
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

/** A fresh tab shows the cluster selector only when the agent has no live selection (the first tab connected it already). */
async function connectIfAsked(page: Page) {
    const dialog = page.getByRole('dialog', { name: 'Choose clusters' });
    const asked = await dialog.waitFor({ timeout: 4000 }).then(
        () => true,
        () => false
    );
    if (!asked) return;
    for (const box of await dialog.locator('.crow input[type=checkbox]').all()) if (!(await box.isChecked())) await box.check();
    await dialog.getByRole('button', { name: 'Connect' }).click();
}

/** Opens a SmartSelect (the trigger carries the test id) and clicks the option with this exact text. */
async function pick(trigger: Locator, name: string) {
    await trigger.click();
    await trigger.page().getByRole('option', { name, exact: true }).click();
}

/** The option texts of a SmartSelect; closes it again. */
async function optionTexts(trigger: Locator): Promise<string[]> {
    await trigger.click();
    const texts = await trigger.page().getByRole('option').allTextContents();
    await trigger.page().keyboard.press('Escape');
    return texts.map(t => t.trim());
}

async function toast(page: Page, text: RegExp) {
    await page.getByRole('region', { name: 'Notifications' }).getByText(text).first().waitFor({ timeout: 10000 });
}

async function main() {
    const stack = await startStack(FAKE, 'e2e');
    const browser = await chromium.launch();
    const page = await browser.newPage({
        viewport: { width: 1440, height: 900 },
        colorScheme: process.env.E2E_COLOR_SCHEME === 'dark' ? 'dark' : 'light'
    });
    const errors: string[] = [];
    page.on('pageerror', e => errors.push(e.message));
    page.on('console', m => m.type() === 'error' && errors.push(m.text()));
    try {
        await step('connect clusters', () => connect(page, stack.url));

        await step('version tooltip on the logo; no update indicator in fake mode', async () => {
            assert.match((await page.locator('a.brand').getAttribute('title')) ?? '', /^v8s\b/);
            await page.waitForTimeout(500); // update.status has answered by now
            assert.equal(await page.getByTestId('update-indicator').count(), 0);
        });

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
            await page.getByTestId('drawer').getByText('Replicas', { exact: true }).waitFor();
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

        await step('events tab lists every event, live', async () => {
            await page.getByRole('tab', { name: 'Events' }).click();
            const pane = page.getByTestId('events-pane');
            await pane.locator('li[data-type="Normal"]').first().waitFor({ timeout: 15000 });
            if ((await page.getByText(/keeps Warning events only/).count()) > 0) throw new Error('the Warning-only note is still shown');
            await pane.getByText('from deployment-controller').first().waitFor();
            // Another scale lands as a new row without reopening the tab.
            await page.getByTestId('actions-menu').click();
            await page.getByRole('menuitem', { name: /Scale/ }).click();
            const dlg = page.getByRole('dialog', { name: /Scale/ });
            await dlg.getByLabel('Replicas').fill('6');
            await dlg.getByRole('button', { name: 'Scale', exact: true }).click();
            await toast(page, /Scaled/);
            await pane.getByText(/to 6$/).first().waitFor({ timeout: 10000 });
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

        await step('a click near the edge of another row switches the drawer to it', async () => {
            const second = page.locator('.scroller .row:not(.sk)').nth(1);
            const other = (await second.locator('.name').innerText()).trim();
            const box = await second.boundingBox();
            const nameBox = await second.locator('.name').boundingBox();
            assert.ok(box && nameBox, 'second row and its name have boxes');
            // 2 px below the row's top edge, just right of the name cell's text (left of the drawer): the row
            // itself, not a cell's text.
            await second.click({ position: { x: Math.round(nameBox.x - box.x + nameBox.width + 24), y: 2 } });
            await page.getByTestId('drawer').getByRole('heading', { name: other }).waitFor({ timeout: 5000 });
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

        await step('protected clusters: the lock toggle keeps rows stable; typed confirmation only while protected', async () => {
            const toggleAll = async (pressed: boolean) => {
                await page.locator('.cluster-btn').click();
                const sel = page.getByRole('dialog', { name: 'Choose clusters' });
                await sel.waitFor();
                const row = sel.locator('.lrow').first();
                const before = await row.boundingBox();
                const checkedBefore = await sel.locator('.crow input[type=checkbox]:checked').count();
                const locks = sel.getByTestId('protect-toggle');
                const n = await locks.count();
                assert.ok(n > 0, 'every context row has a lock toggle');
                for (let i = 0; i < n; i++) {
                    assert.equal(await locks.nth(i).getAttribute('aria-pressed'), String(!pressed));
                    await locks.nth(i).click();
                    assert.equal(await locks.nth(i).getAttribute('aria-pressed'), String(pressed));
                }
                assert.deepEqual(await row.boundingBox(), before, 'the row does not resize when protection toggles');
                assert.equal(
                    await sel.locator('.crow input[type=checkbox]:checked').count(),
                    checkedBefore,
                    'the lock does not toggle the row checkbox'
                );
                await sel.getByRole('button', { name: 'Cancel' }).click();
                await sel.waitFor({ state: 'hidden' });
            };
            const openDelete = async () => {
                await page.locator('.scroller .row:not(.sk)').first().locator('.name').click();
                await page.getByTestId('drawer').waitFor();
                await page.getByTestId('actions-menu').click();
                await page.locator('[data-action="delete"]').click();
                const dlg = page.getByRole('dialog', { name: /^Delete / });
                await dlg.waitFor();
                return dlg;
            };
            const closeDrawer = async () => {
                await page.getByTestId('drawer-close').click();
                await page.getByTestId('drawer').waitFor({ state: 'detached' });
            };

            await toggleAll(true);
            let dlg = await openDelete();
            assert.ok(await dlg.getByLabel('Confirmation text').isVisible(), 'protected: the object name is typed');
            await dlg.getByText(/Protected cluster/).waitFor();
            await dlg.getByRole('button', { name: 'Cancel' }).click();
            await page.getByTestId('drawer').getByRole('img', { name: 'Protected' }).first().waitFor();
            await closeDrawer();

            await toggleAll(false);
            dlg = await openDelete();
            assert.equal(await dlg.getByLabel('Confirmation text').count(), 0, 'unprotected: plain confirmation');
            await dlg.getByRole('button', { name: 'Cancel' }).click();
            await closeDrawer();
        });

        await step('create a ConfigMap', async () => {
            await page.getByTestId('create').click();
            const dlg = page.getByRole('dialog', { name: 'Create' });
            await pick(dlg.getByTestId('create-type'), 'ConfigMap');
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

        await step('smart select: keyboard, search, Esc stays inside a dialog, list stays on screen', async () => {
            await page
                .getByTestId('drawer-close')
                .click({ timeout: 2000 })
                .catch(() => {});
            await page.getByTestId('create').click();
            const dlg = page.getByRole('dialog', { name: 'Create' });
            const type = dlg.getByTestId('create-type');
            await type.focus();
            await page.keyboard.press('ArrowDown');
            const search = page.getByRole('combobox', { name: /search/i });
            await search.waitFor({ timeout: 5000 });
            assert.ok(await search.evaluate(el => el === document.activeElement), 'the search field has focus');
            // The list is a top-layer popover inside the modal dialog, fully inside the viewport.
            const box = await page.getByRole('listbox').boundingBox();
            const vp = page.viewportSize()!;
            assert.ok(
                box && box.x >= 0 && box.y >= 0 && box.x + box.width <= vp.width && box.y + box.height <= vp.height,
                'listbox is in the viewport'
            );
            const hit = await page.getByRole('listbox').evaluate(el => {
                const r = el.getBoundingClientRect();
                const top = document.elementFromPoint(r.x + r.width / 2, r.y + 10);
                return !!top && el.contains(top);
            });
            assert.ok(hit, 'the list is not covered by the dialog');
            await page.keyboard.type('sec');
            assert.deepEqual(await page.getByRole('option').allTextContents(), ['Secret']);
            await page.keyboard.press('Escape');
            await page.getByRole('listbox').waitFor({ state: 'detached' });
            assert.ok(await dlg.isVisible(), 'Esc closed the list, not the dialog');
            assert.ok(await type.evaluate(el => el === document.activeElement), 'focus returns to the trigger');
            await page.keyboard.press('Enter');
            await page.keyboard.type('dep');
            await page.keyboard.press('ArrowDown');
            await page.keyboard.press('Enter');
            assert.match((await type.textContent()) ?? '', /Deployment/);
            await page.keyboard.press('Escape');
            await dlg.waitFor({ state: 'hidden' });
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

        await step('namespace menu: the selection stays visible below the list whatever the search', async () => {
            await page.getByRole('button', { name: /Namespace: payments/ }).click();
            const menu = page.getByRole('dialog', { name: 'Namespace filter' });
            const search = menu.getByLabel('Search namespaces');
            const chip = menu.getByRole('button', { name: 'Remove payments' });
            await search.fill('no-such-namespace');
            await menu.getByText('No matches').waitFor();
            await chip.click();
            await page.getByRole('button', { name: /Namespace: all/ }).waitFor();
            assert.equal(await menu.getByTestId('selected-chips').count(), 0);
            assert.ok(await search.evaluate(el => el === document.activeElement), 'focus moves to the search field');
            await search.fill('payments');
            await menu.getByRole('checkbox').first().check();
            await chip.waitFor();
            await menu.getByRole('button', { name: 'Done' }).click();
            await page.getByRole('button', { name: /Namespace: payments/ }).waitFor();
        });

        await step('command bar: :ns +name adds to the filter, :ns -name removes', async () => {
            const run = async (cmd: string) => {
                await page.keyboard.press(':');
                await page.getByLabel('Command', { exact: true }).fill(cmd);
                await page.getByLabel('Command', { exact: true }).press('Enter');
            };
            await run('ns +kube-system');
            await page.getByRole('button', { name: /Namespace: 2 selected/ }).waitFor();
            await run('ns -payments');
            await page.getByRole('button', { name: /Namespace: kube-system/ }).waitFor();
            await run('ns payments');
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

        await step('overview: clicking an owner opens it in the drawer', async () => {
            await page.keyboard.press('Escape');
            await page.getByRole('link', { name: /^Pods/ }).first().click();
            await page.waitForURL(/\/pods/);
            const first = page.locator('.scroller .row:not(.sk)').first();
            await first.waitFor({ timeout: 30000 });
            await first.locator('.name').click();
            const drawer = page.getByTestId('drawer');
            const owner = drawer.getByTestId('owner-link').first();
            await owner.waitFor();
            const ownerName = (await owner.innerText()).split('/').pop()?.trim() ?? '';
            assert.ok(ownerName, 'owner link names its object');
            await owner.click();
            await drawer.getByRole('heading', { name: ownerName }).waitFor();
            await page.keyboard.press('Escape');
        });

        await step('yaml pane: Wrap and Pretty for a ConfigMap; Secret edit reveals first', async () => {
            const openByName = async (n: string) => {
                await page.keyboard.press(process.platform === 'darwin' ? 'Meta+k' : 'Control+k');
                const input = page.getByTestId('palette-input');
                await input.waitFor();
                await input.fill(n);
                await page.getByRole('option').filter({ hasText: n }).first().waitFor({ timeout: 10000 });
                await input.press('Enter');
                await page.getByTestId('drawer').waitFor();
                await page.getByRole('tab', { name: 'YAML' }).click();
                await page.locator('.cm-content').waitFor();
            };
            const pressed = (testid: string) => page.getByTestId(testid).getAttribute('aria-pressed');

            await openByName('json-config');
            assert.equal(await pressed('yaml-pretty'), 'false');
            assert.match(await page.locator('.cm-content').innerText(), /"retries":3/);
            await page.getByTestId('yaml-pretty').click();
            await page.waitForFunction(() => document.querySelector('.cm-content')?.textContent?.includes('"retries": 3'), null, { timeout: 10000 });
            const pretty = await page.locator('.cm-content').innerText();
            assert.match(pretty, /settings\.json: \|-/);
            assert.match(pretty, /app\.conf: \|\n/);
            // Edit opens the raw YAML, with a hint
            await page.getByTestId('edit-yaml').click();
            await page.getByTestId('edit-hint').waitFor();
            assert.match(await page.locator('.cm-content').innerText(), /"retries":3/);
            await page.getByRole('button', { name: 'Discard' }).click();
            // Wrap drives the editor's line wrapping, without recreating it
            assert.equal(await page.locator('.cm-editor .cm-lineWrapping').count(), 0);
            await page.getByTestId('yaml-wrap').click();
            await page.locator('.cm-editor .cm-lineWrapping').waitFor();
            assert.equal(await pressed('yaml-wrap'), 'true');
            await page.getByTestId('yaml-wrap').click();
            await page.waitForFunction(() => !document.querySelector('.cm-lineWrapping'));
            await page.getByTestId('yaml-pretty').click();
            await page.waitForFunction(() => document.querySelector('.cm-content')?.textContent?.includes('"retries":3'));
            await page.getByTestId('drawer-close').click();

            // Secrets are not in global search (names only come from lists): open it through the router by URL.
            await page.evaluate(() => {
                history.pushState(null, '', '/r/v1/secrets?c=prod-us&ns=team-0&n=json-secret&tab=yaml');
                window.dispatchEvent(new PopStateEvent('popstate'));
            });
            await page.getByTestId('drawer').waitFor();
            await page.locator('.cm-content').waitFor();
            const edit = page.getByTestId('edit-yaml');
            assert.equal((await edit.innerText()).trim(), 'Reveal and edit');
            // One toolbar row: the values tag, Decoded, Reveal and the edit button share one element.
            const sameToolbar = () =>
                page.evaluate(() => {
                    const tb = document.querySelector('[data-testid="edit-yaml"]')?.closest('.toolbar');
                    const q = (id: string) => document.querySelector(`[data-testid="${id}"]`);
                    const btn = q('reveal') ?? q('mask');
                    return (
                        !!tb &&
                        !!btn &&
                        tb.contains(btn) &&
                        tb.contains(q('yaml-decoded')) &&
                        tb.textContent!.includes('values ') &&
                        document.querySelectorAll('.drawer .toolbar').length === 1
                    );
                });
            assert.equal(await sameToolbar(), true);
            assert.equal(await pressed('yaml-decoded'), 'true');
            // Reveal and edit (the masked default) reveals, then opens the decoded document in the editor
            await edit.click();
            await page.getByTestId('edit-hint').filter({ hasText: 'Editing decoded values' }).waitFor({ timeout: 10000 });
            await page.waitForFunction(() => document.querySelector('.cm-content')?.textContent?.includes('stringData:'), null, { timeout: 10000 });
            await page.getByTestId('discard').click();
            await page.getByTestId('mask').click();
            await page.getByText('values masked').waitFor({ timeout: 10000 });
            // Reveal with the default (Decoded on) lands on readable string data
            await page.getByTestId('reveal').click();
            await page.getByText('values revealed').waitFor({ timeout: 10000 });
            await page.waitForFunction(() => document.querySelector('.cm-content')?.textContent?.includes('stringData:'), null, { timeout: 10000 });
            assert.match(await page.locator('.cm-content').innerText(), /hunter2/);
            assert.equal(await sameToolbar(), true);
            assert.equal((await edit.innerText()).trim(), 'Edit');
            // Decoded off shows the stored base64
            await page.getByTestId('yaml-decoded').click();
            await page.waitForFunction(() => !document.querySelector('.cm-content')?.textContent?.includes('stringData:'), null, { timeout: 10000 });
            // Edit opens the raw document, with no decoded hint
            await edit.click();
            await page.getByTestId('review').waitFor();
            assert.equal(await page.getByTestId('edit-hint').count(), 0);
            await page.getByRole('button', { name: 'Discard' }).click();
            // Decoded on: Edit opens decoded (text in stringData), JSON still compact
            await page.getByTestId('yaml-decoded').click();
            await page.getByTestId('edit-yaml').click();
            await page.getByTestId('edit-hint').waitFor();
            const decoded = await page.locator('.cm-content').innerText();
            assert.match(decoded, /stringData:/);
            assert.match(decoded, /"password":"hunter2"/);
            await page.getByRole('button', { name: 'Discard' }).click();
            await page.getByTestId('mask').click();
            await page.getByText('values masked').waitFor();
            await page.getByTestId('drawer-close').click();
        });

        // ---- pod usage (metrics.k8s.io through the fake agent) ----
        await step('pods: CPU/MEM usage shows; sorting %CPU/L desc orders numerically with n/a last; hover title says "requested"', async () => {
            await page.keyboard.press('Escape');
            await page.getByRole('link', { name: /^Pods/ }).first().click();
            await page.waitForURL(/\/pods/);
            const rows = page.locator('.scroller .row:not(.sk)');
            await rows.first().waitFor({ timeout: 30000 });
            // The usage block is one set of header buttons; its cells fill in once the first poll lands.
            for (const label of ['CPU', '%CPU/R', '%CPU/L', 'MEM', '%MEM/R', '%MEM/L'])
                await page.getByRole('button', { name: `Sort by ${label}`, exact: true }).waitFor();
            await page.locator('.scroller .row .ub').filter({ hasText: /\d/ }).first().waitFor({ timeout: 30000 });
            // The default-hidden columns are not on screen.
            assert.equal(await page.getByRole('button', { name: 'Sort by CPU req', exact: true }).count(), 0);

            const lim = page.getByRole('button', { name: 'Sort by %CPU/L', exact: true });
            await lim.click();
            await lim.click();
            await page.getByText('Sorted by %CPU/L (desc)').waitFor();
            // The rows re-sort a moment after the title changes: wait for the third sub-column of the rendered usage blocks to be in
            // descending order before reading it.
            await page.waitForFunction(() => {
                const v = [...document.querySelectorAll('.scroller .row:not(.sk) .ub')]
                    .map(e => e.children[2]?.textContent?.trim() ?? '')
                    .filter(c => /^\d+$/.test(c))
                    .map(Number);
                return v.length > 5 && v.every((x, i) => i === 0 || v[i - 1] >= x);
            });
            const cells = await page.evaluate(() =>
                [...document.querySelectorAll('.scroller .row:not(.sk) .ub')].map(e => e.children[2].textContent!.trim())
            );
            const nums = cells.filter(c => /^\d+$/.test(c)).map(Number);
            assert.ok(nums.length >= 5, `numeric %CPU/L cells: ${cells.join(' ')}`);
            for (let i = 1; i < nums.length; i++) assert.ok(nums[i - 1] >= nums[i], `%CPU/L not in descending numeric order: ${nums.join(' ')}`);
            const firstMissing = cells.findIndex(c => !/^\d+$/.test(c));
            if (firstMissing >= 0)
                assert.ok(
                    cells.slice(firstMissing).every(c => !/^\d+$/.test(c)),
                    `a number after n/a or a dash: ${cells.join(' ')}`
                );
            assert.ok(nums[0] >= 90, 'the busiest pod is at least 90 % of its limit in the fake');
            assert.ok((await page.locator('.scroller .row:not(.sk) .ub .b.hot').count()) > 0, 'hot pods get a red bar');
            const title = await rows.first().locator('.ub').getAttribute('title');
            assert.match(title ?? '', /requested/);
            assert.match(title ?? '', /limit/);
            await page.screenshot({ path: shot('pods-usage') });
            // Back to the default order for the steps that follow.
            await lim.click();
            await lim.click();

            // The memory request header explains its tint; pods far over their memory request (with a limit) are tinted.
            const memReq = page.getByRole('button', { name: 'Sort by %MEM/R', exact: true });
            assert.match((await memReq.getAttribute('title')) ?? '', /amber above 100 % of the request, red above 200 %/);
            await memReq.click();
            await memReq.click();
            await page.getByText('Sorted by %MEM/R (desc)').waitFor();
            await page.locator('.scroller .row:not(.sk) .ub .b.w, .scroller .row:not(.sk) .ub .b.d').first().waitFor();
            await page.screenshot({ path: shot('pods-mem-request') });
            // Leave the sort as the steps that follow expect it (%CPU/L ascending, where the reset above lands): one more click
            // returns %MEM/R to the default, then one on %CPU/L.
            await memReq.click();
            await lim.click();
        });

        // ---- node usage and the Overview capacity card ----
        await step('nodes: usage columns show, the hidden ones are not on screen, sorting %CPU and CPU alloc orders numerically', async () => {
            await page
                .getByRole('link', { name: /^Nodes/ })
                .first()
                .click();
            await page.waitForURL(/\/nodes/);
            const rows = page.locator('.scroller .row:not(.sk)');
            await rows.first().waitFor({ timeout: 30000 });
            for (const label of ['CPU', '%CPU', 'CPU alloc', 'MEM', '%MEM', 'MEM alloc'])
                await page.getByRole('button', { name: `Sort by ${label}`, exact: true }).waitFor();
            await page.locator('.scroller .row .ub').filter({ hasText: /\d/ }).first().waitFor({ timeout: 30000 });
            for (const hidden of ['OS image', 'Kernel', 'Runtime', 'Taints'])
                assert.equal(await page.getByRole('button', { name: `Sort by ${hidden}`, exact: true }).count(), 0, `${hidden} is hidden by default`);

            // The second sub-column is the percentage of allocatable: descending order, numeric, no-data last.
            const pct = page.getByRole('button', { name: 'Sort by %CPU', exact: true });
            await pct.click();
            await pct.click();
            await page.getByText('Sorted by %CPU (desc)').waitFor();
            // The rows re-sort a moment after the title changes: wait for the window to be in descending order.
            await page.waitForFunction(() => {
                const v = [...document.querySelectorAll('.scroller .row:not(.sk) .ub')]
                    .map(e => e.children[1]?.textContent?.trim() ?? '')
                    .filter(c => /^[\d.]+$/.test(c))
                    .map(Number);
                return v.length > 5 && v.every((x, i) => i === 0 || v[i - 1] >= x);
            });
            const col = (i: number) =>
                page.evaluate(n => [...document.querySelectorAll('.scroller .row:not(.sk) .ub')].map(e => e.children[n].textContent!.trim()), i);
            const pcts = (await col(1)).filter(c => /^[\d.]+$/.test(c)).map(Number);
            assert.ok(pcts.length >= 5, `numeric %CPU cells: ${pcts.join(' ')}`);
            for (let i = 1; i < pcts.length; i++) assert.ok(pcts[i - 1] >= pcts[i], `%CPU not in descending order: ${pcts.join(' ')}`);
            assert.match(
                (await rows.first().locator('.ub').getAttribute('title')) ?? '',
                /allocatable/,
                'the tooltip compares usage to the allocatable'
            );

            // CPU alloc is numeric now (it was a string): ascending puts the smallest first.
            const alloc = page.getByRole('button', { name: 'Sort by CPU alloc', exact: true });
            await alloc.click();
            await page.getByText('Sorted by CPU alloc').waitFor();
            await page.waitForFunction(() => {
                const v = [...document.querySelectorAll('.scroller .row:not(.sk) .ub')]
                    .map(e => e.children[2]?.textContent?.trim() ?? '')
                    .filter(c => /^\d+$/.test(c))
                    .map(Number);
                return v.length > 5 && v.every((x, i) => i === 0 || v[i - 1] <= x);
            });
            const cores = (await col(2)).filter(c => /^\d+$/.test(c)).map(Number);
            assert.ok(cores.length >= 5, `numeric CPU alloc cells: ${cores.join(' ')}`);
            for (let i = 1; i < cores.length; i++) assert.ok(cores[i - 1] <= cores[i], `CPU alloc not ascending: ${cores.join(' ')}`);
            await page.screenshot({ path: shot('nodes-usage') });
            await alloc.click(); // desc
            await alloc.click(); // cleared
        });

        await step('overview: the capacity card shows CPU and Memory % with requests and limits', async () => {
            await page
                .getByRole('link', { name: /^Overview/ })
                .first()
                .click();
            await page.waitForURL(/\/overview/);
            const card = page.getByTestId('overview-prod-us');
            await card.waitFor({ timeout: 20000 });
            const cpu = card.getByTestId('cap-cpu');
            const mem = card.getByTestId('cap-mem');
            await cpu.getByText(/requests \d+(\.\d)?% · limits \d+(\.\d)?%/).waitFor({ timeout: 30000 });
            await mem.getByText(/requests \d+(\.\d)?% · limits \d+(\.\d)?%/).waitFor();
            // Usage arrives with the first node poll (the fake has a metrics API): "N of M cores" / "N of M GiB".
            await cpu.getByText(/^[\d.]+ of [\d.]+ cores$/).waitFor({ timeout: 30000 });
            await mem.getByText(/^[\d.]+ of [\d.]+ GiB$/).waitFor();
            assert.match(await cpu.locator('.big').innerText(), /^\d+(\.\d)?%/);
            assert.match(await mem.locator('.big').innerText(), /^\d+(\.\d)?%/);
            await page.screenshot({ path: shot('overview-capacity') });
        });

        // ---- IP search ----
        const openPalette = async (text: string) => {
            await page.keyboard.press(process.platform === 'darwin' ? 'Meta+k' : 'Control+k');
            const input = page.getByTestId('palette-input');
            await input.waitFor();
            await input.fill(text);
            return input;
        };

        await step('IP search: a pod IP finds the pod, Enter opens the drawer', async () => {
            await page.keyboard.press('Escape');
            await page.getByRole('link', { name: /^Pods/ }).first().click();
            await page.waitForURL(/\/pods/);
            const rows = page.locator('.scroller .row:not(.sk)');
            await rows.first().waitFor({ timeout: 30000 });
            // The IP column is hidden until shown from the Columns menu.
            await page.getByRole('button', { name: 'Columns', exact: true }).click();
            await page.getByRole('dialog', { name: 'Columns' }).getByLabel('IP', { exact: true }).check();
            await page.keyboard.press('Escape');
            await page.getByRole('dialog', { name: 'Columns' }).waitFor({ state: 'hidden' });
            let podName = '';
            let podIP = '';
            for (const row of await rows.all()) {
                const cells = await row.locator('[role=cell]').allInnerTexts();
                const ip = cells.map(c => c.trim()).find(c => /^\d+\.\d+\.\d+\.\d+$/.test(c));
                if (ip) {
                    podIP = ip;
                    podName = (await row.locator('.name').first().innerText()).trim();
                    break;
                }
            }
            assert.ok(podIP, 'no pod row shows an IP');
            const input = await openPalette(podIP);
            const hit = page.getByTestId('palette-ip-hit').filter({ hasText: podName }).first();
            await hit.waitFor({ timeout: 10000 });
            await input.press('Enter');
            await page.getByTestId('drawer').waitFor();
            await page.keyboard.press('Escape');
        });

        await step('IP search: a prefix shows "Show all", which opens the filtered list', async () => {
            await openPalette('10.96.0.');
            const all = page.getByTestId('palette-ip-showall').first();
            await all.waitFor({ timeout: 10000 });
            await all.click();
            await page.waitForURL(/\/r\/v1\/services/);
            assert.equal(await page.locator('#pods-filter').inputValue(), '10.96.0.');
            await page.locator('.scroller .row:not(.sk)').first().waitFor({ timeout: 10000 });
        });

        await step('IP search: an address held by a Service and contained in a MetalLB pool and a NAD range', async () => {
            await page.getByRole('link', { name: /^Pods/ }).first().click();
            await page.waitForURL(/\/pods/);
            await openPalette('192.168.100.10');
            // the on-demand sources start with this query; the palette re-asks while they load
            const svc = page.getByTestId('palette-ip-hit').filter({ hasText: 'ingress-nginx-controller' }).first();
            await svc.waitFor({ timeout: 15000 });
            const pool = page.getByTestId('palette-ip-hit').filter({ hasText: 'public' }).filter({ hasText: 'contains' }).first();
            await pool.waitFor({ timeout: 15000 });
            assert.match(await pool.innerText(), /192\.168\.100\.0\/24/);
            const input = page.getByTestId('palette-input');
            await input.fill('192.168.50.7');
            await page.getByTestId('palette-ip-hit').filter({ hasText: 'macvlan-conf' }).first().waitFor({ timeout: 15000 });
            await page.keyboard.press('Escape');
        });

        // ---- T2: logs, shell, drain, Flux, Helm ----
        const openFromTable = async (command: string, urlPart: string, name: string | RegExp, filter?: string) => {
            await page.keyboard.press(':');
            await page.getByLabel('Command', { exact: true }).fill(command);
            await page.getByLabel('Command', { exact: true }).press('Enter');
            await page.waitForURL(url => url.pathname.includes(urlPart));
            if (filter) await page.locator('#pods-filter').fill(filter);
            const row = page.locator('.scroller .row:not(.sk)').filter({ hasText: name }).first();
            await row.waitFor({ timeout: 30000 });
            await row.locator('.name').click();
            await page.getByTestId('drawer').waitFor();
        };

        await step('logs: open, follow, search next/prev', async () => {
            await page.getByRole('link', { name: /^Pods/ }).first().click();
            await page.waitForURL(/\/pods/);
            const first = page.locator('.scroller .row:not(.sk)').first();
            await first.locator('.name').click();
            await page.getByTestId('drawer').waitFor();
            await page.getByRole('tab', { name: 'Logs' }).click();
            await page.getByTestId('logs-pane').waitFor();
            await page.locator('.lrow').first().waitFor({ timeout: 20000 });
            // follow keeps the newest line in view: seq grows
            const seqOf = () =>
                page.evaluate(() => {
                    const rows = [...document.querySelectorAll('.lrow')];
                    for (let i = rows.length - 1; i >= 0; i--) {
                        const m = /seq=(\d+)/.exec(rows[i].textContent ?? '');
                        if (m) return Number(m[1]);
                    }
                    return 0;
                });
            const a = await seqOf();
            await page.waitForFunction(
                n => [...document.querySelectorAll('.lrow')].some(r => Number(/seq=(\d+)/.exec(r.textContent ?? '')?.[1] ?? 0) > n),
                a,
                {
                    timeout: 10000
                }
            );
            await page.getByTestId('logs-search').fill('handled GET');
            await page
                .waitForFunction(
                    () => /\d+\s*\/\s*\d+|\d+ match/i.test(document.querySelector('[data-testid=logs-count]')?.textContent ?? ''),
                    null,
                    { timeout: 10000 }
                )
                .catch(async () => {
                    await page.getByTestId('logs-next').click();
                });
            await page.getByTestId('logs-next').click();
            await page.getByTestId('logs-prev').click();
            await page.locator('.lrow mark').first().waitFor({ timeout: 10000 });
        });

        await step('shell: connect and run echo hi', async () => {
            await page.getByRole('tab', { name: 'Shell' }).click();
            await page.getByTestId('shell-connect').click();
            const term = page.getByTestId('terminal');
            await term.waitFor({ timeout: 20000 });
            const text = () => term.evaluate(el => (el as unknown as { v8sText?: string }).v8sText ?? '');
            await page.waitForFunction(
                () => /echo shell/.test((document.querySelector('[data-testid=terminal]') as unknown as { v8sText?: string })?.v8sText ?? ''),
                null,
                { timeout: 20000 }
            );
            await term.click();
            await page.keyboard.type('echo hi');
            await page.keyboard.press('Enter');
            await page.waitForFunction(
                () => /echo: echo hi/.test((document.querySelector('[data-testid=terminal]') as unknown as { v8sText?: string })?.v8sText ?? ''),
                null,
                { timeout: 10000 }
            );
            assert.match(await text(), /echo: echo hi/);
        });

        await step('drain: pre-flight on node-3 shows the PDB block', async () => {
            await page.getByTestId('drawer-close').click();
            await openFromTable('nodes', '/r/v1/nodes', /prod-us\s*node-3(?!\d)/, 'node-3');
            await page.getByTestId('actions-menu').click();
            await page.getByRole('menuitem', { name: /Drain/ }).click();
            const dlg = page.getByTestId('drain-dialog');
            await dlg.waitFor();
            const blocked = dlg.getByTestId('drain-section-blocked');
            await blocked.waitFor({ timeout: 15000 });
            assert.match(await blocked.innerText(), /PodDisruptionBudget|PDB/i);
            await dlg
                .getByRole('button', { name: /Cancel|Close/ })
                .first()
                .click();
            await dlg.waitFor({ state: 'detached' });
        });

        await step('flux: reconcile shows Reconciling… then settles', async () => {
            await page
                .getByTestId('drawer-close')
                .click()
                .catch(() => {});
            await openFromTable('ks', 'kustomizations', /./);
            await page.getByTestId('actions-menu').click();
            await page.locator('[data-action="flux.reconcile"]').click();
            await page.locator('.scroller').getByText('Reconciling…').first().waitFor({ timeout: 10000 });
            await page.waitForFunction(() => !document.querySelector('.scroller')?.textContent?.includes('Reconciling…'), null, { timeout: 20000 });
        });

        await step('helm: rollback runs as a job to completion', async () => {
            await page
                .getByTestId('drawer-close')
                .click()
                .catch(() => {});
            await openFromTable('releases', 'helm.sh/v1/releases', 'podinfo');
            await page.getByTestId('helm-drawer').waitFor();
            await page.getByTestId('helm-action-rollback').click();
            const dlg = page.getByTestId('helm-action-dialog');
            await dlg.waitFor();
            await dlg.getByTestId('helm-start').click();
            await page.getByTestId('helm-job').waitFor({ timeout: 15000 });
            await page.getByTestId('helm-job').getByText(/done/i).first().waitFor({ timeout: 30000 });
        });

        await step('hosted: a 401 from /api/me sends the browser to /auth/login with the current path as return', async () => {
            const ctx = await browser.newContext();
            const p = await ctx.newPage();
            let target = '';
            await p.route('**/api/me', r => r.fulfill({ status: 401, body: '' }));
            await p.route('**/auth/login**', r => {
                target = r.request().url();
                return r.fulfill({ status: 200, contentType: 'text/html', body: '<p>login</p>' });
            });
            await p.goto(`http://127.0.0.1:${stack.previewPort}/r/apps/v1/deployments?x=1`);
            await p.waitForURL(/\/auth\/login/);
            assert.equal(new URL(target).searchParams.get('return'), '/r/apps/v1/deployments?x=1');
            await ctx.close();
        });

        await step('hosted: after logout the page waits for an explicit sign in (no silent re-login)', async () => {
            const ctx = await browser.newContext();
            const p = await ctx.newPage();
            let hits = 0;
            await p.route('**/api/me', r => {
                hits++;
                return r.fulfill({ status: 401, body: '' });
            });
            await p.goto(`http://127.0.0.1:${stack.previewPort}/?signedout=1`);
            await p.getByTestId('sign-in').waitFor();
            assert.equal(hits, 0, 'no /api/me probe, so no redirect to the IdP');
            await ctx.close();
        });

        await step('phone 390x844: lands on Problems with the bottom nav; pods are 72px cards; detail is full screen; logs work', async () => {
            const ctx = await browser.newContext({ viewport: { width: 390, height: 844 }, hasTouch: true, isMobile: true });
            const p = await ctx.newPage();
            const perr: string[] = [];
            p.on('pageerror', e => perr.push(e.message));
            await p.goto(stack.url);
            await connectIfAsked(p);
            await p.waitForURL(/\/problems/);
            await p.getByRole('navigation', { name: 'Main' }).waitFor();
            for (const n of ['Problems', 'Clusters', 'Search', 'Starred', 'More'])
                await p.getByRole('navigation', { name: 'Main' }).getByText(n, { exact: true }).waitFor();
            await p.screenshot({ path: shot('phone-problems') });

            await p.goto(`http://127.0.0.1:${stack.previewPort}/pods`);
            await p.locator('.pcard:not(.sk)').first().waitFor({ timeout: 30000 });
            const h = await p
                .locator('.pcard:not(.sk)')
                .first()
                .evaluate(el => el.getBoundingClientRect().height);
            assert.equal(Math.round(h), 72, 'phone cards are 72px');
            await p.screenshot({ path: shot('phone-pods') });
            // The More sheet lists the resource types (its nav must not collapse to nothing).
            await p.getByRole('navigation', { name: 'Main' }).getByText('More', { exact: true }).click();
            const sheetLink = p.getByTestId('bottom-sheet').getByRole('link', { name: /^Deployments/ });
            await sheetLink.waitFor();
            assert.ok(((await sheetLink.boundingBox())?.height ?? 0) > 20, 'the More sheet lists resource types');
            await p.keyboard.press('Escape');
            await p.getByTestId('bottom-sheet').waitFor({ state: 'hidden' });
            await p.locator('.pcard:not(.sk)').first().click();
            const drawer = p.getByTestId('drawer');
            await drawer.waitFor();
            const box = await drawer.boundingBox();
            assert.ok(box && box.width >= 389 && box.x <= 1, 'the detail page covers the screen');
            await p.screenshot({ path: shot('phone-detail') });
            await drawer.getByRole('tab', { name: 'Logs' }).click();
            await p.getByTestId('logs-pane').waitFor();
            await p.locator('.lrow').first().waitFor({ timeout: 20000 });
            await p.screenshot({ path: shot('phone-logs') });
            assert.deepEqual(perr, [], 'no page errors on the phone');
            await ctx.close();
        });

        await step('phone: the pod card shows cpu and memory', async () => {
            const ctx = await browser.newContext({ viewport: { width: 390, height: 844 }, hasTouch: true, isMobile: true });
            const p = await ctx.newPage();
            await p.goto(stack.url);
            await connectIfAsked(p);
            await p.goto(`http://127.0.0.1:${stack.previewPort}/pods`);
            await p.locator('.pcard:not(.sk)').first().waitFor({ timeout: 30000 });
            const line = p.locator('.pcard .ucard').first();
            await line.waitFor({ timeout: 30000 });
            assert.match(await line.innerText(), /^cpu \d+m · mem \d+Mi$/);
            await p.screenshot({ path: shot('phone-pods-usage') });
            await ctx.close();
        });

        await step('phone: destructive confirms need a hold, and node shell / port-forward / cross-cluster bulk are not offered', async () => {
            const ctx = await browser.newContext({ viewport: { width: 390, height: 844 }, hasTouch: true, isMobile: true });
            const p = await ctx.newPage();
            await p.goto(stack.url);
            await connectIfAsked(p);
            await p.goto(`http://127.0.0.1:${stack.previewPort}/pods`);
            await p.locator('.pcard:not(.sk)').first().click();
            await p.getByTestId('drawer').waitFor();
            await p.getByTestId('actions-menu').click();
            assert.equal(await p.locator('[data-action="portforward"]').count(), 0, 'no port-forward on a phone');
            await p.locator('[data-action="delete"]').click();
            const hold = p.getByTestId('hold-confirm');
            const phrase = p.getByLabel('Confirmation text');
            await hold.or(phrase).waitFor();
            if (await hold.count()) {
                const b = (await hold.boundingBox())!;
                await p.mouse.move(b.x + b.width / 2, b.y + b.height / 2);
                await p.mouse.down();
                await p.waitForTimeout(300);
                await p.mouse.up();
                assert.ok(await hold.isVisible(), 'a short press does not confirm');
            } else {
                assert.ok(await phrase.isVisible(), 'destructive deletes keep the typed phrase');
            }
            await p.keyboard.press('Escape');
            await ctx.close();
        });

        // ---- T3: Problems, Security, Network ----
        // Evidence and acknowledgements need the problem's string id as a view column (`problemId`); an agent without it is reported, not failed.
        let ackable = true;

        await step(
            'pods: Enter in the filter moves focus to the results, arrows move the row focus, Esc clears the filter and stays on the list',
            async () => {
                for (let i = 0; i < 3 && (await page.locator('dialog[open]').count()); i++) await page.keyboard.press('Escape');
                await page
                    .getByTestId('drawer-close')
                    .click({ timeout: 2000 })
                    .catch(() => {});
                await page.getByRole('link', { name: /^Pods/ }).first().click();
                await page.waitForURL(/\/pods/);
                const rows = page.locator('.scroller .row:not(.sk)');
                const names = rows.locator('.name');
                await rows.first().waitFor({ timeout: 30000 });
                const all = (await names.allInnerTexts()).map(n => n.trim());
                const first0 = all[0];
                // A filter that keeps at least three rows but not the first one, so a focus left on the old first row would be visible.
                // Start from the 15th row but try every row: the fake clusters share pod names, and an earlier step leaves the list
                // sorted by usage, so the rows can come in same-named pairs where no prefix of one name matches three.
                let filterText = '';
                const start = Math.min(14, all.length - 1);
                for (const target of [...all.slice(start), ...all.slice(1, start)]) {
                    for (let n = target.length; n >= 3 && !filterText; n--) {
                        const p = target.slice(0, n);
                        if (all.filter(x => x.includes(p)).length >= 3 && !first0.includes(p)) filterText = p;
                    }
                    if (filterText) break;
                }
                assert.ok(filterText, 'found a filter that excludes the first row and keeps three');
                const filter = page.locator('#pods-filter');
                await filter.fill(filterText);
                const onlyMatches = () =>
                    page.waitForFunction(t => {
                        const ns = [...document.querySelectorAll('.scroller .row:not(.sk) .name')];
                        return ns.length >= 3 && ns.every(n => n.textContent?.includes(t));
                    }, filterText);
                await onlyMatches();
                await filter.press('Enter');
                assert.ok(await page.locator('.scroller').evaluate(el => el === document.activeElement), 'Enter moves focus to the list');
                await page.waitForFunction(() => document.querySelector('.scroller .row:not(.sk)')?.classList.contains('focus'));
                await page.keyboard.press('ArrowDown');
                await page.keyboard.press('ArrowDown');
                assert.ok(await rows.nth(2).evaluate(el => el.classList.contains('focus')), 'two ArrowDowns focus the third row');
                await page.keyboard.press('Escape');
                assert.equal(await filter.inputValue(), '');
                assert.ok(await page.locator('.scroller').evaluate(el => el === document.activeElement), 'the list keeps focus after Esc');
                // The unfiltered first row (not the old first match) has the row focus.
                await page.waitForFunction(n => {
                    const r = document.querySelector('.scroller .row:not(.sk)');
                    return r?.querySelector('.name')?.textContent?.trim() === n && r.classList.contains('focus');
                }, first0);
                assert.equal(await page.locator('.scroller .row.focus').count(), 1, 'exactly one row has the row focus');
            }
        );

        await step('problems: page loads with family chips and live counts', async () => {
            // The Helm step leaves its dialog and drawer open.
            for (let i = 0; i < 3 && (await page.locator('dialog[open]').count()); i++) await page.keyboard.press('Escape');
            await page
                .getByTestId('drawer-close')
                .click({ timeout: 2000 })
                .catch(() => {});
            await page.getByTestId('nav-problems').click();
            await page.waitForURL(/\/problems/);
            await page.getByTestId('problem-row').first().waitFor({ timeout: 30000 });
            await page.getByTestId('chip-pods').waitFor();
            assert.match((await page.getByTestId('chip-pods').innerText()).replace(/\s+/g, ' '), /Pods\s*\d+/);
            assert.match(await page.getByTestId('nav-problems-count').innerText(), /\d/);
            assert.match(await page.getByTestId('topbar-problems').innerText(), /\d/);
            await page.screenshot({ path: shot('problems') });
        });

        await step('problems: Enter in the search moves focus to the list, ArrowDown moves the row focus, Esc clears the search', async () => {
            const objs = page.getByTestId('problem-row').locator('.oname b');
            const all = (await objs.allInnerTexts()).map(n => n.trim());
            const first0 = all[0];
            const target = all.find(n => n !== first0 && !first0.includes(n)) ?? first0;
            const search = page.locator('#pods-filter');
            await search.fill(target);
            await page.waitForFunction(t => {
                const ns = [...document.querySelectorAll('[data-testid="problem-row"] .oname b')];
                return ns.length > 0 && ns.every(n => n.textContent?.includes(t));
            }, target);
            await search.press('Enter');
            assert.ok(await page.locator('.scroller').evaluate(el => el === document.activeElement), 'Enter moves focus to the list');
            const rowsLoc = page.getByTestId('problem-row');
            await page.waitForFunction(() => document.querySelector('[data-testid="problem-row"]')?.classList.contains('focus'));
            if ((await rowsLoc.count()) > 1) {
                await page.keyboard.press('ArrowDown');
                assert.ok(await rowsLoc.nth(1).evaluate(el => el.classList.contains('focus')), 'ArrowDown moves the row focus');
            }
            await page.keyboard.press('Escape');
            assert.equal(await search.inputValue(), '');
            assert.ok(await page.locator('.scroller').evaluate(el => el === document.activeElement), 'the list keeps focus after Esc');
            await page.waitForFunction(n => {
                const r = document.querySelector('[data-testid="problem-row"]');
                return r?.querySelector('.oname b')?.textContent?.trim() === n && r.classList.contains('focus');
            }, first0);
        });

        await step('problems: group by class inserts header rows into the same list', async () => {
            await pick(page.getByTestId('group-by'), 'Group: class');
            await page.getByTestId('problems-group').first().waitFor({ timeout: 15000 });
            await pick(page.getByTestId('group-by'), 'No grouping');
        });

        await step('problems: expand a crashloop rollup and open logs from its evidence', async () => {
            await page.getByTestId('chip-pods').click(); // only the pods family
            const row = page.getByTestId('problem-row').filter({ hasText: 'pod.crashloop' }).first();
            await row.waitFor({ timeout: 15000 });
            await row
                .getByRole('button', { name: /Show (evidence|\d+ clusters)/ })
                .first()
                .click();
            const ev = page.getByTestId('problem-evidence');
            await ev.waitFor({ timeout: 15000 });
            ackable = !/does not expose problem ids/.test(await ev.innerText());
            await ev
                .getByRole('button', { name: /^Logs$/ })
                .first()
                .click();
            await page.waitForURL(/tab=logs/);
            await page.getByTestId('logs-pane').waitFor();
            await page.getByTestId('drawer-close').click();
            // The drawer opened on the pods page: back to Problems, where the family filter is still on.
            await page.getByTestId('nav-problems').click();
            await page.getByTestId('chip-pods').click(); // clear the family filter again
        });

        await step('problems: acknowledge hides a problem, Show acknowledged restores it', async () => {
            if (!ackable) {
                console.log('skip: the agent does not expose the problemId column (acknowledge needs it)');
                return;
            }
            const before = Number((await page.getByTestId('nav-problems-count').innerText()).replace(/\D/g, ''));
            const row = page.getByTestId('problem-row').first();
            await row.getByRole('button', { name: 'Acknowledge' }).first().click();
            await page.waitForFunction(
                n => Number(document.querySelector('[data-testid=nav-problems-count]')?.textContent?.replace(/\D/g, '')) < n,
                before,
                { timeout: 10000 }
            );
            await page.getByTestId('show-acked').click();
            assert.equal(await page.getByTestId('show-acked').getAttribute('aria-pressed'), 'true');
            await page
                .getByRole('button', { name: /^Restore/ })
                .first()
                .waitFor({ timeout: 10000 });
            await page
                .getByRole('button', { name: /^Restore/ })
                .first()
                .click();
            await page.getByTestId('show-acked').click();
            await page.waitForFunction(
                n => Number(document.querySelector('[data-testid=nav-problems-count]')?.textContent?.replace(/\D/g, '')) >= n,
                before,
                { timeout: 10000 }
            );
        });

        await step('security: Vulnerabilities list loads and an image opens a windowed CVE table', async () => {
            await page.getByRole('link', { name: 'Vulnerabilities' }).first().click();
            await page.waitForURL(/\/security/);
            const list = page.getByTestId('vuln-images');
            await list.locator('.row').first().waitFor({ timeout: 30000 });
            await list.locator('.row').first().click();
            await page.getByTestId('image-panel').waitFor();
            await page
                .getByTestId('image-panel')
                .getByRole('button', { name: /^Open report of/ })
                .first()
                .click();
            await page.getByTestId('cve-table').waitFor({ timeout: 20000 });
            await page.getByTestId('cve-table').getByText(/CVE-/).first().waitFor({ timeout: 20000 });
            await page.screenshot({ path: shot('cve-table') });
        });

        await step('security: Kyverno policy mode switch (Audit to Enforce) updates the policy', async () => {
            await page.getByRole('link', { name: 'Policies' }).first().click();
            await page.getByTestId('policies-page').waitFor();
            const row = page.getByTestId('policies-page').locator('.row:not(.sk)').first();
            await row.waitFor({ timeout: 30000 });
            await row.locator('.name').click();
            await page.getByTestId('policy-panel').waitFor();
            const mode = page.getByTestId('policy-mode');
            const was = (await mode.innerText()).trim();
            const target = /enforce/i.test(was) ? 'mode-audit' : 'mode-enforce';
            const btn = page.getByTestId(target);
            if (await btn.isDisabled()) {
                console.log('skip: the first policy is read-only (legacy)');
                return;
            }
            await btn.click();
            const dlg = page.getByRole('dialog');
            await dlg.waitFor();
            const phrase = dlg.getByLabel('Confirmation text');
            if (await phrase.count()) await phrase.fill(await dlg.locator('b.mono').innerText());
            await dlg
                .getByRole('button', { name: /Audit|Enforce|Switch|Set/ })
                .last()
                .click();
            await page.waitForFunction(w => document.querySelector('[data-testid=policy-mode]')?.textContent?.trim() !== w, was, { timeout: 15000 });
        });

        await step('network: the drops view lists the fake drop; the flows tab shows flows and a drop', async () => {
            await page.getByRole('link', { name: 'Drops' }).first().click();
            await page.getByTestId('drops-view').waitFor();
            await page.getByTestId('drops-row').first().waitFor({ timeout: 30000 });
            assert.match(await page.getByTestId('drops-list').innerText(), /POLICY_DENIED/);
            await page.screenshot({ path: shot('drops') });
        });

        await step('network: the Flows tab of a workload shows flows and a dropped flow', async () => {
            // The fake relay speaks for shop/frontend, shop/backend and shop/probe; the fake cluster has no such workloads, so create them.
            await page.evaluate(() => localStorage.removeItem('v8s:namespaces')); // the restored namespace filter would empty the deployments list
            await page.goto(`http://127.0.0.1:${stack.previewPort}/r/apps/v1/deployments`);
            await page.locator('.scroller .row:not(.sk)').first().waitFor({ timeout: 30000 });
            await page.getByTestId('create').click();
            const dlg = page.getByRole('dialog', { name: 'Create' });
            await dlg.waitFor();
            const cluster = (await dlg.getByTestId('create-cluster').getAttribute('data-value')) ?? '';
            await dlg.locator('.cm-content').waitFor();
            await dlg.locator('.cm-content').click();
            await page.keyboard.press(process.platform === 'darwin' ? 'Meta+a' : 'Control+a');
            await page.keyboard.insertText(
                [
                    'apiVersion: v1\nkind: Namespace\nmetadata:\n  name: shop',
                    'apiVersion: apps/v1\nkind: Deployment\nmetadata:\n  name: backend\n  namespace: shop\nspec:\n  replicas: 1\n  selector:\n    matchLabels:\n      app: backend\n  template:\n    metadata:\n      labels:\n        app: backend\n    spec:\n      containers:\n        - name: c\n          image: nginx:1.25\n'
                ].join('\n---\n')
            );
            await dlg.getByTestId('dry-run').click();
            await dlg.getByTestId('dry-ok').waitFor({ timeout: 15000 });
            await dlg.getByTestId('create-submit').click();
            await dlg.waitFor({ state: 'hidden', timeout: 20000 });
            await page.goto(`http://127.0.0.1:${stack.previewPort}/r/apps/v1/deployments?c=${cluster}&ns=shop&n=backend&tab=flows`);
            const pane = page.getByTestId('flows-pane');
            await pane.waitFor({ timeout: 20000 });
            await pane.locator('.frow, [role=row]').first().waitFor({ timeout: 20000 });
            await page.waitForFunction(
                () => /DROPPED|POLICY_DENIED|Dropped/.test(document.querySelector('[data-testid=flows-pane]')?.textContent ?? ''),
                null,
                { timeout: 30000 }
            );
            await page.screenshot({ path: shot('flows') });
        });

        await step('linked contexts: no hover controls; link and unlink a pair in the dialog', async () => {
            await page.locator('.cluster-btn').click();
            const sel = page.getByRole('dialog', { name: 'Choose clusters' });
            await sel.waitFor();
            const row = sel.locator('.lrow').first();
            // Let the panel's open animation finish: a box read mid-animation moves even though nothing resizes.
            await sel.evaluate(el => Promise.all(el.getAnimations({ subtree: true }).map(a => a.finished)));
            const size = async () => {
                const b = await row.boundingBox();
                return b && { width: b.width, height: b.height };
            };
            const before = await size();
            await row.hover();
            assert.equal(await sel.getByText(/Link write context/).count(), 0, 'no per-row link control');
            assert.equal(await sel.getByRole('button', { name: /Unlink/ }).count(), 0, 'no per-row unlink control');
            assert.deepEqual(await size(), before, 'row does not resize on hover');
            await sel.getByRole('button', { name: /^Linked contexts…/ }).click();
            const dlg = page.getByRole('dialog', { name: 'Linked contexts' });
            await dlg.waitFor();
            const options = (await optionTexts(dlg.getByTestId('lc-read'))).filter(t => t !== 'Choose…');
            assert.ok(options.length >= 2, 'at least two contexts to pair');
            const [r, w] = options;
            await pick(dlg.getByTestId('lc-read'), r);
            await pick(dlg.getByTestId('lc-write'), w);
            await dlg.getByTestId('lc-link').click();
            const item = dlg.getByTestId('lc-current').locator('li', { hasText: r });
            await item.waitFor({ timeout: 10000 });
            assert.match((await item.textContent()) ?? '', new RegExp(w));
            // The write context leaves the selector as its own cluster.
            await sel.getByRole('checkbox', { name: `Select ${w}`, exact: true }).waitFor({ state: 'detached', timeout: 10000 });
            await page.screenshot({ path: shot('linked-dialog') });
            await item.getByRole('button', { name: 'Unlink' }).click();
            await item.waitFor({ state: 'detached', timeout: 10000 });
            // Escape closes only the dialog: the selector stays open and app shortcuts do not fire behind it.
            await dlg.getByRole('button', { name: 'Done' }).focus();
            await page.keyboard.press(':');
            assert.equal(await page.getByRole('dialog', { name: 'Command bar' }).count(), 0, 'no command bar behind the dialog');
            await page.keyboard.press('Escape');
            await dlg.waitFor({ state: 'hidden' });
            assert.ok(await sel.isVisible(), 'Escape in the dialog keeps the selector open');
            await sel.getByRole('button', { name: /^Linked contexts…/ }).click();
            await dlg.waitFor();
            await dlg.getByRole('button', { name: 'Done' }).click();
            await dlg.waitFor({ state: 'hidden' });
            await sel.waitFor();
            await sel.getByRole('button', { name: 'Cancel' }).click();
            await sel.waitFor({ state: 'hidden' });
        });

        await step('sidebar: unpin, overlay by hover and click, Escape, pin back, resize persists', async () => {
            await page.getByRole('link', { name: /^Pods/ }).first().click();
            await page.waitForURL(/\/pods/);
            await page.locator('.scroller .row:not(.sk)').first().waitFor({ timeout: 20000 });
            const panel = page.getByTestId('sidebar-panel');
            const width = async (sel: string) => Math.round((await page.locator(sel).first().boundingBox())!.width);
            const reload = async () => {
                await page.reload();
                const dialog = page.getByRole('dialog', { name: 'Choose clusters' });
                const firstRow = page.locator('.scroller .row:not(.sk)').first();
                await Promise.race([dialog.waitFor({ timeout: 20000 }), firstRow.waitFor({ timeout: 20000 })]);
                if (await dialog.isVisible()) await dialog.getByRole('button', { name: 'Connect' }).click();
                await firstRow.waitFor({ timeout: 20000 });
            };
            const panelWidth = async () => Math.round((await panel.boundingBox())!.width);
            const shots = process.env.SIDEBAR_SHOTS;
            const snap = async (name: string) =>
                shots && (await page.waitForTimeout(250), await page.screenshot({ path: join(shots, `${name}.png`) }));

            // Pinned by default.
            await panel.waitFor();
            assert.equal(await panelWidth(), 232);
            const pinnedContent = await width('.content');
            await snap('pinned');

            // Unpin: the sidebar leaves the layout and the content grows by its width.
            await page.getByTestId('sidebar-pin').click();
            await panel.waitFor({ state: 'detached' });
            assert.equal(await width('.content'), pinnedContent + 232, 'content takes the full width');
            await snap('unpinned');

            // Hover the left edge: the overlay floats over the content (content width unchanged).
            await page.mouse.move(2, 400);
            await panel.waitFor({ timeout: 3000 });
            assert.equal(await width('.content'), pinnedContent + 232, 'overlay does not push the content');
            assert.match((await panel.getAttribute('class')) ?? '', /overlay/);
            await snap('overlay');
            // Leaving the overlay closes it.
            await page.mouse.move(900, 400);
            await panel.waitFor({ state: 'detached', timeout: 3000 });

            // The toggle opens it; Escape closes it.
            await page.getByTestId('sidebar-toggle').click();
            await panel.waitFor();
            await page.keyboard.press('Escape');
            await panel.waitFor({ state: 'detached' });

            // Clicking outside closes it.
            await page.getByTestId('sidebar-toggle').click();
            await panel.waitFor();
            await page.mouse.click(900, 23);
            await panel.waitFor({ state: 'detached' });

            // Following a link closes it.
            await page.getByTestId('sidebar-toggle').click();
            await panel.getByRole('link', { name: /^Deployments/ }).click();
            await page.waitForURL(/\/r\/apps\/v1\/deployments/);
            await panel.waitFor({ state: 'detached' });

            // Unpinned survives a reload, even without the browser-side first-paint cache (settings are the source of truth).
            await page.evaluate(() => localStorage.removeItem('v8s:sidebar'));
            await page.evaluate(() => localStorage.removeItem('v8s:namespaces')); // the restored namespace filter would empty the deployments list
            await page.mouse.move(900, 400); // a pointer resting on the left edge would open the overlay
            await reload();
            await page.getByTestId('sidebar-toggle').waitFor();
            await panel.waitFor({ state: 'detached', timeout: 3000 }); // still unpinned after reload

            // Pin back from the overlay.
            await page.getByTestId('sidebar-toggle').click();
            await page.getByTestId('sidebar-pin').click();
            await page.getByTestId('sidebar-toggle').waitFor({ state: 'detached' });
            assert.equal(await width('.content'), pinnedContent, 'content narrows again');

            // Drag-resize: +100px. The separator exposes the width.
            const handle = page.getByTestId('sidebar-resizer');
            assert.equal(await handle.getAttribute('role'), 'separator');
            assert.equal(await handle.getAttribute('aria-valuenow'), '232');
            const hb = (await handle.boundingBox())!;
            await page.mouse.move(hb.x + hb.width / 2, 400);
            await page.mouse.down();
            await page.mouse.move(hb.x + hb.width / 2 + 100, 400, { steps: 5 });
            await page.mouse.up();
            await page.waitForFunction(() => document.querySelector('[data-testid=sidebar-resizer]')?.getAttribute('aria-valuenow') === '332');
            assert.equal(await panelWidth(), 332);
            assert.equal(await width('.content'), pinnedContent - 100, 'content reflows');
            await snap('resized');

            // Keyboard: arrows move by 16px, and the width is clamped.
            await handle.focus();
            await page.keyboard.press('ArrowRight');
            assert.equal(await panelWidth(), 348);
            await page.keyboard.press('End');
            assert.equal(await panelWidth(), 420);
            await page.keyboard.press('Home');
            assert.equal(await panelWidth(), 180);
            await page.keyboard.press('ArrowLeft');
            assert.equal(await panelWidth(), 180, 'not below the minimum');
            await page.keyboard.press('ArrowRight');
            await page.keyboard.press('ArrowRight'); // 212
            await page.waitForTimeout(600); // the keyboard width is saved after a short pause

            // The width persists across a reload (settings, not the cache).
            await page.evaluate(() => localStorage.removeItem('v8s:sidebar'));
            await page.evaluate(() => localStorage.removeItem('v8s:namespaces')); // the restored namespace filter would empty the deployments list
            await reload();
            await page.waitForFunction(() => document.querySelector('[data-testid=sidebar-resizer]')?.getAttribute('aria-valuenow') === '212');

            // Double-click resets to the default, and `[` toggles pinned.
            await page.getByTestId('sidebar-resizer').dblclick();
            await page.waitForFunction(() => document.querySelector('[data-testid=sidebar-resizer]')?.getAttribute('aria-valuenow') === '232');
            await page.locator('h1').click();
            await page.keyboard.press('[');
            await panel.waitFor({ state: 'detached' });
            await page.keyboard.press('[');
            await panel.waitFor();
            assert.equal(await width('.content'), pinnedContent);

            // A drawer opened beside the pinned sidebar is on top of it.
            await page.mouse.move(900, 400);
            await page
                .locator('.scroller .row:not(.sk)')
                .first()
                .click({ position: { x: 300, y: 10 } });
            const drawer = page.getByTestId('drawer');
            await drawer.waitFor();
            const db = (await drawer.boundingBox())!;
            const inside = await page.evaluate(
                ([x, y]) => !!document.elementFromPoint(x!, y!)?.closest('[data-testid=drawer]'),
                [db.x + 3, db.y + 200]
            );
            assert.ok(inside, 'the drawer is above the pinned sidebar near its left edge');
            await page.keyboard.press('Escape');
            await drawer.waitFor({ state: 'hidden' });
        });

        await step('agent restart: reconnects by itself, no reload, the open view recovers', async () => {
            await page.getByRole('link', { name: /^Pods/ }).first().click();
            await page.waitForURL(/\/pods/);
            await page.keyboard.press(':');
            await page.getByLabel('Command', { exact: true }).fill('ns all');
            await page.getByLabel('Command', { exact: true }).press('Enter');
            await page.keyboard.press(':');
            await page.getByLabel('Command', { exact: true }).fill('configmaps');
            await page.getByLabel('Command', { exact: true }).press('Enter');
            await page.waitForURL(/\/r\/v1\/configmaps/);
            await page.getByPlaceholder('Filter by name').fill('e2e-config');
            const created = page.locator('.scroller .row:not(.sk)', { hasText: 'e2e-config' });
            await created.first().waitFor({ timeout: 20000 });
            await page.evaluate(() => ((window as unknown as { __noReload: boolean }).__noReload = true));
            const seen = errors.length;
            await stack.restartAgent(); // new process on the same port: every socket drops, in-memory state (and that ConfigMap) is gone
            // The same view resubscribes and shows the new agent's snapshot: the old agent's ConfigMap disappears.
            await created.first().waitFor({ state: 'detached', timeout: 30000 });
            await page.getByText(/No configmaps match/).waitFor({ timeout: 30000 }); // the fresh snapshot, not stale rows
            // A view opened after the restart works too.
            await page.getByRole('link', { name: /^Services/ }).click();
            await page.waitForURL(/\/r\/v1\/services/);
            await page.locator('.scroller .row:not(.sk)').first().waitFor({ timeout: 30000 });
            await page.getByTestId('agent-reconnecting').waitFor({ state: 'hidden', timeout: 30000 });
            assert.equal(await page.getByTestId('agent-unreachable').count(), 0);
            assert.equal(await page.evaluate(() => (window as unknown as { __noReload?: boolean }).__noReload), true, 'the page did not reload');
            // Only the failed reconnect attempts' socket errors are expected; anything else still fails the run.
            const expected = /WebSocket|ERR_CONNECTION|Failed to load resource/i;
            errors.splice(seen, errors.length - seen, ...errors.slice(seen).filter(m => !expected.test(m)));
        });

        assert.deepEqual(errors, [], 'no console or page errors');
        console.log('e2e: all flows passed');
    } catch (e) {
        console.error(`e2e failed at "${current}":`, e);
        await page.screenshot({ path: process.env.E2E_SHOT ?? shot('failure') }).catch(() => {});
        process.exitCode = 1;
    } finally {
        await browser.close();
        await stack.stop();
    }
}

void main();
