import { test } from '@playwright/test';
import { mkdirSync, readdirSync, readFileSync } from 'node:fs';
import { fileURLToPath } from 'node:url';
import path from 'node:path';

/**
 * Captures one full-page screenshot per route per platform into
 * ./screenshots/<slug>-<platform>[-dark].png. Routes are auto-discovered from
 * the built ./dist, so new pages are picked up without editing this file.
 * This writes images only; nothing is uploaded. (pixelci-sales-site hands the
 * same folder to PixelCI; this site can do the same later by adding its
 * `visual` job to .github/workflows/www.yml.)
 *
 * The GitHub releases request is answered with an empty list, so captures are
 * deterministic and keep the server-rendered v0.1.0 state whatever GitHub says
 * today. The demo is seeded, and under reduced motion it skips its typed intro.
 */

const distDir = fileURLToPath(new URL('../dist', import.meta.url));
const outDir = 'screenshots';
mkdirSync(outDir, { recursive: true });

type Route = { url: string; slug: string };

function discoverRoutes(): Route[] {
  const routes: Route[] = [];
  for (const entry of readdirSync(distDir, { recursive: true, encoding: 'utf8' })) {
    const rel = String(entry).split(path.sep).join('/');
    if (!rel.endsWith('.html')) continue;
    if (rel.endsWith('404.html')) {
      // Hit a guaranteed-miss path so the preview server serves 404.html.
      routes.push({ url: '/__v8s_missing__', slug: '404' });
      continue;
    }
    // A redirect stub photographs whatever it forwards to, so it is not a page of ours to baseline.
    if (readFileSync(path.join(distDir, rel), 'utf8').includes('http-equiv="refresh"')) continue;
    const base = rel.replace(/(^|\/)index\.html$/, '$1').replace(/\.html$/, '');
    const url = '/' + base.replace(/\/+$/, '');
    const slug = base.replace(/\/+$/, '').replace(/\//g, '-') || 'index';
    routes.push({ url: url === '/' || url === '' ? '/' : url, slug });
  }
  return routes.sort((a, b) => a.slug.localeCompare(b.slug));
}

const routes = discoverRoutes();

/**
 * Both schemes are captured. The design system declares light and dark together
 * with `light-dark()`, resolved off `color-scheme` — which follows the OS unless
 * a reader pins one with the header toggle. So emulating `prefers-color-scheme`
 * is the honest way to photograph dark: it exercises the same path a
 * system-dark visitor takes, with no JavaScript and nothing to settle.
 *
 * Light is `<slug>-<platform>`; dark adds a `-dark` suffix.
 */
const THEMES = ['light', 'dark'] as const;

for (const { url, slug } of routes) {
  for (const theme of THEMES) {
    test(theme === 'light' ? slug : `${slug} · dark`, async ({ page }, testInfo) => {
      const platform = testInfo.project.name;

      // Capture the reduced-motion rendering: the demo's typed intro waits for it
      // to scroll into view, and under reduced motion it never runs. Set alongside the
      // scheme in one call, since emulateMedia resets omitted options.
      await page.emulateMedia({ reducedMotion: 'reduce', colorScheme: theme });

      await page.route('https://api.github.com/**', (route) =>
        route.fulfill({ status: 200, contentType: 'application/json', body: '[]' }),
      );
      await page.goto(url, { waitUntil: 'networkidle' });
      await page.evaluate(() => (document as any).fonts?.ready);
      // Walk the page once so lazy screenshots load before the full-page capture.
      await page.evaluate(async () => {
        for (let y = 0; y < document.body.scrollHeight; y += 600) {
          window.scrollTo(0, y);
          await new Promise((r) => setTimeout(r, 40));
        }
        window.scrollTo(0, 0);
      });
      await page.waitForTimeout(400); // let the release script settle the download state

      const suffix = theme === 'light' ? '' : `-${theme}`;
      await page.screenshot({
        path: path.join(outDir, `${slug}-${platform}${suffix}.png`),
        fullPage: true,
        animations: 'disabled',
      });
    });
  }
}
