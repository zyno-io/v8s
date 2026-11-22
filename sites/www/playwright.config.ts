import { defineConfig } from '@playwright/test';

/**
 * Visual-regression capture config.
 *
 * Three "platform" projects (desktop / tablet / mobile) each run the same spec,
 * which writes a full-page screenshot per route named `<slug>-<platform>[-dark].png`
 * into ./screenshots (not uploaded anywhere; see vrt/screenshots.spec.ts).
 *
 * Chromium only — VRT wants one deterministic engine, not cross-browser noise.
 * The webServer serves the built ./dist via `astro preview` (run `npm run vrt`,
 * which builds first; locally install the browser once with
 * `npx playwright install chromium`).
 */
export default defineConfig({
  testDir: './vrt',
  fullyParallel: true,
  forbidOnly: !!process.env.CI,
  retries: 0,
  workers: process.env.CI ? 2 : undefined,
  reporter: 'list',
  outputDir: 'test-results',
  use: {
    baseURL: 'http://127.0.0.1:4321',
    browserName: 'chromium',
    deviceScaleFactor: 1,
    // CI runs as root, where Chromium refuses to start with its sandbox on.
    launchOptions: { args: ['--no-sandbox'] },
  },
  projects: [
    { name: 'desktop', use: { viewport: { width: 1440, height: 900 } } },
    { name: 'tablet', use: { viewport: { width: 768, height: 1024 } } },
    { name: 'mobile', use: { viewport: { width: 390, height: 844 } } },
  ],
  webServer: {
    // Bind to 127.0.0.1 explicitly so Playwright's readiness check can't stall
    // on localhost resolving to IPv6 (::1) while the server listens on IPv4.
    command: 'npm run preview -- --host 127.0.0.1',
    url: 'http://127.0.0.1:4321',
    reuseExistingServer: !process.env.CI,
    timeout: 120_000,
  },
});
