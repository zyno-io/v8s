import { chromium } from 'playwright';

import { startStack } from './stack.ts';

const out = process.env.SHOTS!;
const stack = await startStack('clusters=3,pods=50000,churn=200');
const browser = await chromium.launch();
const ctx = await browser.newContext({ viewport: { width: 1440, height: 900 }, colorScheme: 'dark' });
const page = await ctx.newPage();
page.on('pageerror', e => console.log('pageerror', e.message));
await page.goto(stack.url);
const dialog = page.getByRole('dialog', { name: 'Choose clusters' });
await dialog.waitFor();
await dialog.getByRole('button', { name: 'Connect' }).click();
await page.locator('.row:not(.sk)').first().waitFor();
await page.waitForTimeout(2500);
await page.keyboard.press('j');
await page.keyboard.press('x');
await page.keyboard.press('j');
await page.keyboard.press('x');
await page.keyboard.press('Enter');
await page.waitForTimeout(300);
await page.screenshot({ path: out + '/d-dark-drawer.png' });
await page.keyboard.press('Escape'); // drawer is last in chain; filter empty, selection clears first
await page.keyboard.press('Escape');
await page.keyboard.press('?');
await page.waitForTimeout(200);
await page.screenshot({ path: out + '/e-shortcuts.png' });
await page.keyboard.press('Escape');
await page.keyboard.press(':');
await page.keyboard.type('d');
await page.waitForTimeout(200);
await page.screenshot({ path: out + '/f-cmd-deploy.png' });
await page.keyboard.press('Enter');
await page.waitForTimeout(200);
await page.screenshot({ path: out + '/g-cmd-unavail.png' });
await browser.close();
await stack.stop();
process.exit(0);
