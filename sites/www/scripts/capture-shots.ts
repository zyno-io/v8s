/**
 * Captures the real v8s UI (demo mode, synthetic clusters) for the marketing site.
 *
 * Rerun (Node >= 22.6, ImageMagick `magick` on PATH, the v8s repo checked out with deps installed and Playwright's chromium installed):
 *
 *   yarn install && yarn workspace @v8s/ui build-only          # at the repo root; the UI must be built; also needs Go to build the agent
 *   cd sites/www
 *   node --experimental-strip-types scripts/capture-shots.ts
 *
 * V8S_REPO defaults to the repo root (../.. from sites/www). The script starts the fake-mode agent and `vite preview`
 * through the v8s repo's own packages/ui/perf/stack.ts, writes <name>-<light|dark>.webp plus a .webp.json sidecar to
 * public/shots/, and stops everything on exit. It modifies nothing in the v8s repo. ONLY=name1,name2 limits the shots. V8S_FAKE overrides the fake config: the pods and overview shots use
 * V8S_FAKE=clusters=1,pods=4500,churn=0,lograte=20 (one cluster whose pod count fits its nodes, so the capacity numbers are plausible).
 */
import { execFileSync } from 'node:child_process';
import { mkdirSync, mkdtempSync, writeFileSync } from 'node:fs';
import { createRequire } from 'node:module';
import { tmpdir } from 'node:os';
import { dirname, join, resolve } from 'node:path';
import { fileURLToPath, pathToFileURL } from 'node:url';

const here = dirname(fileURLToPath(import.meta.url));
const repo = resolve(here, '..', process.env.V8S_REPO ?? '../..');
const ui = join(repo, 'packages/ui');
const outDir = resolve(here, '../public/shots');
const png = mkdtempSync(join(tmpdir(), 'v8s-shots-'));
mkdirSync(outDir, { recursive: true });

const { chromium } = createRequire(join(ui, 'package.json'))('playwright') as typeof import('playwright');
const { startStack } = (await import(pathToFileURL(join(ui, 'perf/stack.ts')).href)) as typeof import('../../../packages/ui/perf/stack.ts');

const FAKE = process.env.V8S_FAKE ?? 'clusters=3,pods=50000,churn=0,lograte=200'; // clusters: prod-us, prod-eu, staging
const sha = execFileSync('git', ['rev-parse', '--short', 'HEAD'], { cwd: repo }).toString().trim();
const only = process.env.ONLY?.split(',');

type Theme = 'light' | 'dark';
const stack = await startStack(FAKE, 'shots');
const browser = await chromium.launch();

const HIDE = `*{cursor:none!important} [aria-label="Notifications"]{display:none!important}`;

async function open(theme: Theme, phone: boolean) {
    const ctx = await browser.newContext(
        phone
            ? { viewport: { width: 390, height: 844 }, deviceScaleFactor: 3, isMobile: true, hasTouch: true, colorScheme: theme }
            : { viewport: { width: 1440, height: 900 }, deviceScaleFactor: 2, colorScheme: theme }
    );
    const page = await ctx.newPage();
    await page.goto(stack.url);
    await page.addStyleTag({ content: HIDE });
    const dialog = page.getByRole('dialog', { name: 'Choose clusters' });
    if (await dialog.waitFor({ timeout: 6000 }).then(() => true, () => false)) {
        for (const box of await dialog.locator('.crow input[type=checkbox]').all()) if (!(await box.isChecked())) await box.check();
        await dialog.getByRole('button', { name: 'Connect' }).click();
    }
    await page.locator('.scroller .row:not(.sk), .card').first().waitFor({ timeout: 40000 }).catch(() => {});
    return { ctx, page };
}

async function settle(page: import('playwright').Page, ms = 2500) {
    await page.waitForTimeout(ms);
    await page.getByText(/syncing|loading/i).first().waitFor({ state: 'hidden', timeout: 30000 }).catch(() => {});
    await page.waitForTimeout(500);
}
const nav = async (page: import('playwright').Page, path: string) => {
    await page.evaluate(h => {
        history.pushState({}, '', h);
        dispatchEvent(new PopStateEvent('popstate'));
    }, path);
    await settle(page, 3500);
};

function save(name: string, theme: Theme, buf: Buffer, w: number, h: number, dpr: number, view: string) {
    const base = join(png, `${name}-${theme}`);
    writeFileSync(base + '.png', buf);
    const webp = join(outDir, `${name}-${theme}.webp`);
    execFileSync('magick', [base + '.png', '-strip', '-quality', '90', webp]);
    writeFileSync(
        webp + '.json',
        JSON.stringify(
            {
                prompt: `Sourced, not generated: v8s UI captured in demo mode (v8s agent --fake ${FAKE}) from github.com/zyno-io/v8s at commit ${sha}, ${view}, ${theme}, ${w}x${h} @${dpr}x. Synthetic clusters; no real data.`,
                createdAt: new Date().toISOString()
            },
            null,
            2
        ) + '\n'
    );
    console.log('wrote', webp);
}

try {
    for (const theme of ['light', 'dark'] as Theme[]) {
        const { ctx, page } = await open(theme, false);
        const want = (n: string) => !only || only.includes(n);
        const shot = async (name: string, view: string, clip?: { x: number; y: number; width: number; height: number }) =>
            save(name, theme, await page.screenshot(clip ? { clip } : undefined), clip?.width ?? 1440, clip?.height ?? 900, 2, view);

        if (want('problems')) {
            await nav(page, '/problems');
            await page.getByTestId('chip-pods').click();
            await settle(page, 1500);
            await shot('problems', 'the Problems view across 3 clusters filtered to pod problems, severity tags and Deployment workload rollups');
        }
        if (want('pods')) {
            await nav(page, '/pods');
            await page.keyboard.press('['); // unpin the sidebar: the usage block gets the full width
            await page.getByText(/^Running\s*[\d,]+/).first().click();
            await settle(page, 5000);
            await page.mouse.move(1300, 850);
            await settle(page, 1500);
            await shot('pods', 'the Pods table, Running pods of one cluster in the default order (status, then name), with the sidebar unpinned and the CPU and memory usage block');
            await page.keyboard.press('['); // pin the sidebar again for the shots that follow
        }
        if (want('overview')) {
            await nav(page, '/overview');
            await settle(page, 6000);
            // The first cluster's card: counts, then CPU and Memory capacity.
            await shot('overview', 'the Overview card of one cluster with CPU and Memory capacity (usage, requests and limits committed)', { x: 252, y: 100, width: 1170, height: 346 });
        }
        if (want('command')) {
            await nav(page, '/pods');
            await page.locator('body').click({ position: { x: 700, y: 600 } });
            await page.keyboard.type(':po team-1');
            await settle(page, 1000);
            await shot('command', 'the : command bar with ":po team-1" and its completion list over the Pods table');
        }
        if (want('drawer')) {
            await nav(page, '/r/apps/v1/deployments');
            await page.getByText(/^Healthy\s*[\d,]+/).first().click();
            await settle(page, 1500);
            await page.locator('.scroller .row:not(.sk) .name').first().click();
            await page.getByTestId('drawer').getByText('Replicas', { exact: true }).waitFor();
            await settle(page, 1000);
            await shot('drawer', 'a Deployment detail drawer (Overview tab) over the Deployments table');
        }
        if (want('flux')) {
            await nav(page, '/flux/graph');
            await page.getByText('Failing only + neighbours').click();
            await settle(page, 1500);
            await page.getByRole('button', { name: 'Fit' }).click();
            await settle(page, 1000);
            await shot('flux', 'the Flux dependency graph for prod-eu, failing objects plus neighbours');
        }
        if (want('hubble')) {
            await nav(page, '/network/drops');
            await shot('hubble', 'the Hubble network drops view across clusters (the fake relay has no service-map traffic in a selectable namespace)');
        }
        if (want('clusters')) {
            await nav(page, '/pods');
            const trigger = page.getByRole('button', { name: /clusters/ }).first();
            await trigger.click();
            const dlg = page.getByRole('dialog', { name: 'Choose clusters' });
            if ((await dlg.locator('.chip').count()) === 0) {
                // Groups are settings; creating them through the UI (memory-only in demo mode) gives the chips a realistic look.
                const mk = async (name: string, names: string[]) => {
                    for (const box of await dlg.locator('.crow input[type=checkbox]').all()) await box.uncheck();
                    for (const n of names) await dlg.getByLabel(`Select ${n}`).check();
                    await dlg.getByRole('button', { name: '+ New group' }).click();
                    await dlg.getByLabel('New group name').fill(name);
                    await dlg.getByRole('button', { name: /Create from/ }).click();
                    await page.waitForTimeout(500);
                };
                await mk('production', ['prod-us', 'prod-eu']);
                await mk('non-prod', ['staging']);
                for (const box of await dlg.locator('.crow input[type=checkbox]').all()) if (!(await box.isChecked())) await box.check();
            }
            // Protect the production clusters so the selector shows its lock column in use.
            for (const n of ['prod-us', 'prod-eu']) {
                const lock = dlg.getByRole('button', { name: `Protect ${n}` });
                if ((await lock.getAttribute('aria-pressed')) !== 'true') await lock.click();
            }
            await page.mouse.move(1300, 800);
            await page.waitForTimeout(300);
            await shot('clusters', 'the Choose clusters selector dialog with group chips, prod-us and prod-eu protected');
            const box = (await dlg.boundingBox())!;
            save('clusters-menu', theme, await dlg.screenshot(), Math.round(box.width), Math.round(box.height), 2, 'the Choose clusters selector panel only, prod-us and prod-eu protected');
        }
        await ctx.close();
    }

    if (!only || only.includes('phone')) {
        for (const theme of ['light', 'dark'] as Theme[]) {
            const { ctx, page } = await open(theme, true);
            await settle(page, 4000);
            save('phone', theme, await page.screenshot(), 390, 844, 3, 'the phone layout, Problems-first');
            await ctx.close();
        }
    }
} finally {
    await browser.close();
    await stack.stop();
}
