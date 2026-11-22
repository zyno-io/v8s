/**
 * Renders the social preview image and the icons into public/.
 *
 *   npm run og        (after the screenshots exist; needs `npx playwright install chromium` once)
 *
 * Output (commit it; the build does not run this):
 *   public/og/home.png            1200×630 Open Graph / Twitter card
 *   public/icon-192.png, public/apple-touch-icon.png (180×180)
 *   public/favicon-32.png, public/favicon.ico
 *   (public/favicon.svg is the v8s logo itself, docs/public/logo.svg in the v8s repo)
 *
 * The card is drawn with the site's tokens (light theme), Inter from
 * @fontsource, and the app's real demo-mode screenshot (public/shots/).
 */
import { chromium } from '@playwright/test';
import { mkdirSync, readFileSync, writeFileSync } from 'node:fs';
import { dirname, join } from 'node:path';
import { fileURLToPath } from 'node:url';

const root = join(dirname(fileURLToPath(import.meta.url)), '..');
const pub = join(root, 'public');
const fontDir = join(root, 'node_modules/@fontsource/inter/files');
const b64 = (p) => readFileSync(p).toString('base64');

const logo = (rx = 14) =>
  `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 64 64"><rect width="64" height="64" rx="${rx}" fill="#4f46e5"/><path d="M14 20l12 26a2 2 0 0 0 3.6 0L40 24" fill="none" stroke="#fff" stroke-width="6" stroke-linecap="round" stroke-linejoin="round"/><circle cx="48" cy="19" r="5" fill="#fff"/></svg>`;
const logoSrc = (rx) => `data:image/svg+xml;base64,${Buffer.from(logo(rx)).toString('base64')}`;
const shotSrc = `data:image/webp;base64,${b64(join(pub, 'shots/command-light.webp'))}`;

const font = (w) =>
  `@font-face{font-family:Inter;font-weight:${w};src:url(data:font/woff2;base64,${b64(join(fontDir, `inter-latin-${w}-normal.woff2`))}) format('woff2')}`;

const css = `
${[400, 500, 600, 700].map(font).join('\n')}
:root{--bg:#f6f7f9;--surface:#fff;--border:#e3e5ea;--border-strong:#cfd3db;--text:#15171c;--text-2:#4f5562;--text-3:#646a78;
  --accent:#4f46e5;--accent-soft:rgba(79,70,229,.1)}
*{box-sizing:border-box;margin:0}
html,body{width:1200px;height:630px;overflow:hidden}
body{font-family:Inter,sans-serif;background:var(--bg);color:var(--text);position:relative;isolation:isolate;-webkit-font-smoothing:antialiased}
body::before{content:'';position:absolute;inset:0;z-index:-1;
  background-image:linear-gradient(to right,var(--border) 1px,transparent 1px),linear-gradient(to bottom,var(--border) 1px,transparent 1px);
  background-size:28px 28px;-webkit-mask-image:radial-gradient(ellipse 80% 75% at 25% 0%,#000 25%,transparent 80%);opacity:.75}
body::after{content:'';position:absolute;inset:0;z-index:-1;background:radial-gradient(ellipse 55% 70% at 20% 0%,var(--accent-soft),transparent 70%)}
.copy{position:absolute;left:72px;top:64px;bottom:56px;width:500px;display:flex;flex-direction:column}
.brand{display:flex;align-items:center;gap:14px}
.mark{width:48px;height:48px}
.name{font-size:34px;font-weight:700;letter-spacing:-.02em}
h1{margin-top:auto;font-size:62px;line-height:1.02;font-weight:700;letter-spacing:-.04em}
h1 span{color:var(--accent)}
.sub{margin-top:22px;font-size:23px;line-height:1.4;color:var(--text-2)}
.foot{margin-top:auto;padding-top:28px;display:flex;align-items:center;gap:14px;font-size:19px;color:var(--text-3);font-weight:500}
.dot{width:4px;height:4px;border-radius:50%;background:var(--border-strong)}
.shot{position:absolute;left:610px;top:72px;width:860px;border:1px solid var(--border-strong);border-radius:14px;overflow:hidden;
  box-shadow:0 30px 60px -16px rgba(16,24,40,.22),0 4px 10px -4px rgba(16,24,40,.08);background:#eceef2}
.shot img{display:block;width:100%}
`;

const card = `<!doctype html><html><head><meta charset="utf-8"><style>${css}</style></head><body>
<div class="copy">
  <div class="brand"><img class="mark" src="${logoSrc(14)}" alt=""><span class="name">v8s</span></div>
  <h1>Every cluster. Every row. <span>No lag.</span></h1>
  <p class="sub">A fast, multi-cluster Kubernetes manager for desktop, browser and phone.</p>
  <div class="foot">macOS<span class="dot"></span>Windows<span class="dot"></span>Linux<span class="dot"></span>Helm<span class="dot"></span>MIT</div>
</div>
<div class="shot"><img src="${shotSrc}" alt=""></div>
</body></html>`;

/** An ICO file holding one PNG image. */
function pngToIco(png, size) {
  const head = Buffer.alloc(22);
  head.writeUInt16LE(0, 0);
  head.writeUInt16LE(1, 2);
  head.writeUInt16LE(1, 4);
  head.writeUInt8(size >= 256 ? 0 : size, 6);
  head.writeUInt8(size >= 256 ? 0 : size, 7);
  head.writeUInt8(0, 8);
  head.writeUInt8(0, 9);
  head.writeUInt16LE(1, 10);
  head.writeUInt16LE(32, 12);
  head.writeUInt32LE(png.length, 14);
  head.writeUInt32LE(22, 18);
  return Buffer.concat([head, png]);
}

const browser = await chromium.launch();
try {
  const ctx = await browser.newContext({ viewport: { width: 1200, height: 630 }, deviceScaleFactor: 1, colorScheme: 'light' });
  const tab = await ctx.newPage();
  mkdirSync(join(pub, 'og'), { recursive: true });
  await tab.setContent(card, { waitUntil: 'load' });
  await tab.evaluate(() => document.fonts.ready);
  await tab.screenshot({ path: join(pub, 'og', 'home.png'), type: 'png' });
  console.log('wrote public/og/home.png');

  /** The logo at `size`; `rx` 0 gives the full-bleed square iOS masks itself. */
  const icon = async (size, rx = 14) => {
    await tab.setViewportSize({ width: size, height: size });
    await tab.setContent(
      `<html><body style="margin:0;width:${size}px;height:${size}px"><img src="${logoSrc(rx)}" width="${size}" height="${size}" style="display:block"></body></html>`,
      { waitUntil: 'load' },
    );
    return tab.screenshot({ type: 'png', omitBackground: true });
  };
  writeFileSync(join(pub, 'icon-192.png'), await icon(192));
  writeFileSync(join(pub, 'apple-touch-icon.png'), await icon(180, 0));
  const fav32 = await icon(32);
  writeFileSync(join(pub, 'favicon-32.png'), fav32);
  writeFileSync(join(pub, 'favicon.ico'), pngToIco(fav32, 32));
  console.log('wrote icons');
} finally {
  await browser.close();
}
