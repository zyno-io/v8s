/**
 * Local review captures for design work (not VRT): full-page desktop and
 * mobile shots of the built site in both themes into .impeccable/review/.
 *   npm run build && npx astro preview --host 127.0.0.1 --port 4410 &
 *   node scripts/review-capture.mjs [baseUrl]
 */
import { chromium } from '@playwright/test';
import { mkdirSync } from 'node:fs';

const base = process.argv[2] || 'http://127.0.0.1:4410';
const out = '.impeccable/review';
mkdirSync(out, { recursive: true });
const browser = await chromium.launch();
const errors = [];
try {
  for (const [name, w, h] of [['desktop', 1440, 900], ['mobile', 390, 844]]) {
    for (const scheme of ['light', 'dark']) {
      const ctx = await browser.newContext({ viewport: { width: w, height: h }, colorScheme: scheme, reducedMotion: 'reduce' });
      const page = await ctx.newPage();
      page.on('pageerror', (e) => errors.push(`${name}/${scheme}: ${e.message}`));
      page.on('console', (m) => m.type() === 'error' && errors.push(`${name}/${scheme}: ${m.text()}`));
      await page.goto(base + '/', { waitUntil: 'networkidle' });
      await page.evaluate(() => document.fonts.ready);
      await page.evaluate(async () => {
        for (let y = 0; y < document.body.scrollHeight; y += 700) { window.scrollTo(0, y); await new Promise((r) => setTimeout(r, 50)); }
        window.scrollTo(0, 0);
      });
      await page.waitForTimeout(400);
      const sfx = scheme === 'dark' ? '-dark' : '';
      await page.screenshot({ path: `${out}/${name}${sfx}.png`, fullPage: true });
      await page.screenshot({ path: `${out}/${name}-fold${sfx}.png` });
      const overflow = await page.evaluate(() => document.documentElement.scrollWidth - window.innerWidth);
      if (overflow > 0) errors.push(`${name}/${scheme}: horizontal overflow ${overflow}px`);
      await ctx.close();
    }
  }
} finally {
  await browser.close();
}
console.log(errors.length ? errors.join('\n') : 'no console errors, no overflow');
