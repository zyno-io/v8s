/**
 * Features the download for the visitor's computer. Every link and version is in the HTML already, fixed at build
 * time from the CHANGELOG (src/data/release.ts); nothing is fetched. This script only:
 *  1. detects the OS (and the Mac's CPU through User-Agent Client Hints where the browser offers them; Safari
 *     doesn't, so Apple silicon is assumed with an Intel link beside it),
 *  2. points the hero button at that build (taking the URL from its row in the download list) and marks the row,
 *  3. offers the sibling builds a visitor may need instead.
 * Without JS, or on an unknown OS, the hero button leads to the download list.
 */
import { platforms, type PlatformId } from '../data/site';

type Detected = { id: PlatformId; sure: boolean } | 'mobile' | null;

async function detect(): Promise<Detected> {
  const nav = navigator as Navigator & {
    userAgentData?: {
      platform?: string;
      mobile?: boolean;
      getHighEntropyValues?: (hints: string[]) => Promise<{ architecture?: string }>;
    };
  };
  const ua = nav.userAgent || '';
  const plat = nav.userAgentData?.platform || nav.platform || '';
  if (nav.userAgentData?.mobile || /Android|iPhone|iPad|iPod/i.test(ua)) return 'mobile';
  if (/Mac/i.test(plat) || /Macintosh/.test(ua)) {
    if (nav.maxTouchPoints > 1) return 'mobile'; // iPadOS asking for the desktop site
    try {
      const hints = await nav.userAgentData?.getHighEntropyValues?.(['architecture']);
      if (hints?.architecture === 'x86') return { id: 'mac-x64', sure: true };
      if (hints?.architecture === 'arm') return { id: 'mac-arm64', sure: true };
    } catch {
      /* hints refused */
    }
    return { id: 'mac-arm64', sure: false };
  }
  if (/Win/i.test(plat) || /Windows/.test(ua)) return { id: 'win-x64', sure: true };
  if (/CrOS/.test(ua)) return null;
  if (/Linux|X11/i.test(plat) || /Linux|X11/.test(ua)) {
    if (/aarch64|arm/i.test(plat + ua)) return null;
    if (/Fedora|Red Hat/i.test(ua)) return { id: 'linux-rpm', sure: true };
    if (/Ubuntu|Debian/i.test(ua)) return { id: 'linux-deb', sure: true };
    return { id: 'linux-appimage', sure: true };
  }
  return null;
}

/** The secondary link beside the featured button: the sibling builds a visitor may need instead. */
const alternates: Partial<Record<PlatformId, PlatformId[]>> = {
  'mac-arm64': ['mac-x64'],
  'mac-x64': ['mac-arm64'],
  'linux-appimage': ['linux-deb', 'linux-rpm'],
  'linux-deb': ['linux-appimage', 'linux-rpm'],
  'linux-rpm': ['linux-appimage', 'linux-deb'],
};

/** The build-time download URL of a platform, from its row in the download list. */
const urlOf = (id: PlatformId) => document.querySelector<HTMLAnchorElement>(`[data-dl-row="${id}"] [data-dl-link]`)?.href;

function render(mine: Detected) {
  const featured = mine && mine !== 'mobile' ? platforms.find((p) => p.id === mine.id) : undefined;
  const sure = mine && mine !== 'mobile' ? mine.sure : true;
  if (featured) document.documentElement.dataset.platform = featured.id;
  document.querySelectorAll<HTMLElement>('[data-dl-row]').forEach((row) => row.classList.toggle('is-mine', row.dataset.dlRow === featured?.id));

  const href = featured && urlOf(featured.id);
  document.querySelectorAll<HTMLAnchorElement>('[data-dl-primary]').forEach((a) => {
    const label = a.querySelector<HTMLElement>('[data-dl-label]');
    const icon = a.querySelector<HTMLElement>('[data-dl-icon]');
    if (mine === 'mobile') {
      if (label) label.textContent = 'All downloads';
    } else if (featured && href) {
      a.href = href;
      a.setAttribute('download', '');
      if (label) label.textContent = `Download for ${featured.short}`;
      if (icon) icon.className = `fa-brands ${featured.icon}`;
    }
  });

  document.querySelectorAll<HTMLElement>('[data-dl-alt]').forEach((el) => {
    el.replaceChildren();
    const alts = featured ? alternates[featured.id] : undefined;
    if (mine === 'mobile') {
      el.textContent = 'v8s runs on your desktop, or on a phone through your team’s hosted v8s.';
    } else if (featured && alts) {
      const lead = document.createElement('span');
      lead.textContent = featured.id === 'mac-arm64' && !sure ? 'Intel Mac? ' : 'Also: ';
      el.append(lead);
      alts.forEach((id, i) => {
        const p = platforms.find((x) => x.id === id)!;
        const link = document.createElement('a');
        link.href = urlOf(id) ?? '#download';
        link.setAttribute('download', '');
        link.textContent = id === 'mac-x64' ? 'Intel build' : id === 'mac-arm64' ? 'Apple silicon build' : p.file;
        if (i) el.append(' · ');
        el.append(link);
      });
    }
    el.hidden = !el.textContent;
  });
}

detect()
  .catch(() => null)
  .then(render);
