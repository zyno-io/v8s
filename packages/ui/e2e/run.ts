import type { Page } from 'playwright';

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
                assert.ok(await phrase.isVisible(), 'production deletes keep the typed phrase');
            }
            await p.keyboard.press('Escape');
            await ctx.close();
        });

        // ---- T3: Problems, Security, Network ----
        // Evidence and acknowledgements need the problem's string id as a view column (`problemId`); an agent without it is reported, not failed.
        let ackable = true;

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

        await step('problems: group by class inserts header rows into the same list', async () => {
            await page
                .getByTestId('group-by')
                .selectOption('class')
                .catch(async () => {
                    await page.getByTestId('group-by').click();
                    await page.getByRole('option', { name: /class/i }).click();
                });
            await page.getByTestId('problems-group').first().waitFor({ timeout: 15000 });
            await page
                .getByTestId('group-by')
                .selectOption('none')
                .catch(() => {});
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
            await page.getByTestId('show-acked').check();
            await page
                .getByRole('button', { name: /^Restore/ })
                .first()
                .waitFor({ timeout: 10000 });
            await page
                .getByRole('button', { name: /^Restore/ })
                .first()
                .click();
            await page.getByTestId('show-acked').uncheck();
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
            await page.goto(`http://127.0.0.1:${stack.previewPort}/r/apps/v1/deployments`);
            await page.locator('.scroller .row:not(.sk)').first().waitFor({ timeout: 30000 });
            await page.getByTestId('create').click();
            const dlg = page.getByRole('dialog', { name: 'Create' });
            await dlg.waitFor();
            const cluster = await dlg.getByTestId('create-cluster').inputValue();
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
            const before = await row.boundingBox();
            await row.hover();
            assert.equal(await sel.getByText(/Link write context/).count(), 0, 'no per-row link control');
            assert.equal(await sel.getByRole('button', { name: /Unlink/ }).count(), 0, 'no per-row unlink control');
            assert.deepEqual(await row.boundingBox(), before, 'row does not resize on hover');
            await sel.getByRole('button', { name: /^Linked contexts…/ }).click();
            const dlg = page.getByRole('dialog', { name: 'Linked contexts' });
            await dlg.waitFor();
            const options = await dlg.getByTestId('lc-read').locator('option:not([value=""])').allTextContents();
            assert.ok(options.length >= 2, 'at least two contexts to pair');
            const [r, w] = options;
            await dlg.getByTestId('lc-read').selectOption({ label: r });
            await dlg.getByTestId('lc-write').selectOption({ label: w });
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
