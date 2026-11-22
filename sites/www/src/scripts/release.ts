/**
 * Download buttons that know the latest release and the visitor's computer.
 *
 * The HTML ships working without this: every link points at GitHub Releases
 * (the latest release page) and names the version in index.astro. This script then:
 *  1. reads the GitHub releases API (unauthenticated, cached 10 minutes per tab
 *     so reloads don't spend the 60/hour limit),
 *  2. ignores drafts, prefers the newest published non-prerelease, else the
 *     newest prerelease (labelled "Release candidate"),
 *  3. maps assets by suffix (v8s-<ver>-mac-arm64.dmg, v8s_<ver>_linux_amd64.tar.gz, ...),
 *  4. detects the OS (and the Mac's CPU through User-Agent Client Hints where the
 *     browser offers them; Safari doesn't, so Apple silicon is assumed with an
 *     Intel link beside it) and features that build,
 *  5. on a failed request or a rate limit keeps the GitHub links.
 */
import { cliBuilds, platforms, releaseApi, type PlatformId } from '../data/site';

interface Asset {
  name: string;
  browser_download_url: string;
}
interface Release {
  tag_name: string;
  draft: boolean;
  prerelease: boolean;
  html_url: string;
  published_at: string | null;
  assets: Asset[];
}
type Found = { kind: 'release' | 'candidate'; release: Release } | { kind: 'none' } | { kind: 'error' };
type Detected = { id: PlatformId; sure: boolean } | 'mobile' | null;

const CACHE_KEY = 'v8s-releases';
const CACHE_MS = 10 * 60 * 1000;

async function fetchReleases(): Promise<Release[]> {
  try {
    const hit = JSON.parse(sessionStorage.getItem(CACHE_KEY) || 'null');
    if (hit && Date.now() - hit.at < CACHE_MS && Array.isArray(hit.data)) return hit.data;
  } catch {
    /* no cache */
  }
  const ctl = new AbortController();
  const timer = setTimeout(() => ctl.abort(), 8000);
  try {
    const res = await fetch(releaseApi, { headers: { Accept: 'application/vnd.github+json' }, signal: ctl.signal });
    if (!res.ok) throw new Error(`GitHub API ${res.status}`);
    const data = (await res.json()) as Release[];
    if (!Array.isArray(data)) throw new Error('unexpected response');
    try {
      sessionStorage.setItem(CACHE_KEY, JSON.stringify({ at: Date.now(), data }));
    } catch {
      /* storage blocked */
    }
    return data;
  } finally {
    clearTimeout(timer);
  }
}

function pick(list: Release[]): Found {
  const published = list.filter((r) => !r.draft);
  const stable = published.find((r) => !r.prerelease);
  if (stable) return { kind: 'release', release: stable };
  const rc = published.find((r) => r.prerelease);
  if (rc) return { kind: 'candidate', release: rc };
  return { kind: 'none' };
}

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

const dateFmt = new Intl.DateTimeFormat('en-US', { month: 'short', day: 'numeric', year: 'numeric' });
const version = (r: Release) => r.tag_name.replace(/^v/, '');
const each = <T extends Element>(sel: string, fn: (el: T) => void) => document.querySelectorAll<T>(sel).forEach(fn);
const setText = (sel: string, text: string) => each<HTMLElement>(sel, (el) => (el.textContent = text));

/** The secondary link beside the featured button: the sibling builds a visitor may need instead. */
const alternates: Partial<Record<PlatformId, PlatformId[]>> = {
  'mac-arm64': ['mac-x64'],
  'mac-x64': ['mac-arm64'],
  'linux-appimage': ['linux-deb', 'linux-rpm'],
  'linux-deb': ['linux-appimage', 'linux-rpm'],
  'linux-rpm': ['linux-appimage', 'linux-deb'],
};

function render(found: Found, mine: Detected) {
  const featured = mine && mine !== 'mobile' ? platforms.find((p) => p.id === mine.id) : undefined;
  const release = found.kind === 'release' || found.kind === 'candidate' ? found.release : undefined;
  const assetFor = (suffix: string) =>
    release?.assets.find((a) => a.name.endsWith(`-${suffix}`) || a.name.endsWith(`_${suffix}`));

  document.documentElement.dataset.release = found.kind;
  if (featured) document.documentElement.dataset.platform = featured.id;

  if (release) {
    const v = version(release);
    const when = release.published_at ? dateFmt.format(new Date(release.published_at)) : '';
    setText('[data-dl-ver]', v);
    setText('[data-dl-status-text]', `${found.kind === 'candidate' ? 'Release candidate ' : 'v'}${v}${when ? ` · ${when}` : ''}`);
    each<HTMLAnchorElement>('[data-dl-notes]', (a) => (a.href = release.html_url));
    const sums = release.assets.find((a) => a.name === 'checksums.txt');
    each<HTMLAnchorElement>('[data-dl-sums]', (a) => (a.href = sums ? sums.browser_download_url : release.html_url));
  } else if (found.kind === 'error') {
    setText('[data-dl-status-text]', 'Latest release on GitHub');
  }

  const wire = (row: HTMLElement, asset: Asset | undefined) => {
    row.classList.toggle('is-mine', !!featured && row.dataset.dlRow === featured.id);
    const link = row.querySelector<HTMLAnchorElement>('[data-dl-link]') ?? (row as HTMLAnchorElement);
    const file = row.querySelector<HTMLElement>('[data-dl-file]');
    if (asset) {
      link.href = asset.browser_download_url;
      link.setAttribute('download', '');
      if (file) file.textContent = asset.name;
    } else if (release) {
      link.href = release.html_url;
    }
  };
  for (const p of platforms) each<HTMLElement>(`[data-dl-row="${p.id}"]`, (row) => wire(row, assetFor(p.suffix)));
  for (const c of cliBuilds) each<HTMLElement>(`[data-cli-row="${c.id}"]`, (row) => wire(row, assetFor(c.suffix)));

  each<HTMLAnchorElement>('[data-dl-primary]', (a) => {
    const label = a.querySelector<HTMLElement>('[data-dl-label]');
    const icon = a.querySelector<HTMLElement>('[data-dl-icon]');
    if (mine === 'mobile') {
      a.href = '#download';
      if (label) label.textContent = 'All downloads';
      return;
    }
    const asset = featured && assetFor(featured.suffix);
    if (featured && asset) {
      a.href = asset.browser_download_url;
      a.setAttribute('download', '');
      if (label) label.textContent = `Download for ${featured.short}`;
      if (icon) icon.className = `fa-brands ${featured.icon}`;
    } else if (release) {
      a.href = release.html_url;
    }
  });

  each<HTMLElement>('[data-dl-alt]', (el) => {
    el.replaceChildren();
    const alts = featured ? alternates[featured.id] : undefined;
    if (mine === 'mobile') {
      el.textContent = 'v8s runs on your desktop, or on a phone through your team’s hosted v8s.';
    } else if (featured && alts) {
      const lead = document.createElement('span');
      lead.textContent = featured.id === 'mac-arm64' && mine && mine !== 'mobile' && !mine.sure ? 'Intel Mac? ' : 'Also: ';
      el.append(lead);
      alts.forEach((id, i) => {
        const p = platforms.find((x) => x.id === id)!;
        const asset = assetFor(p.suffix);
        const link = document.createElement('a');
        link.href = asset?.browser_download_url ?? (release?.html_url || '#download');
        if (asset) link.setAttribute('download', '');
        link.textContent = id === 'mac-x64' ? 'Intel build' : id === 'mac-arm64' ? 'Apple silicon build' : p.file;
        if (i) el.append(' · ');
        el.append(link);
      });
    } else {
      el.textContent = '';
    }
    el.hidden = !el.textContent;
  });
}

(async () => {
  const minePromise = detect().catch(() => null);
  let found: Found;
  try {
    found = pick(await fetchReleases());
  } catch {
    found = { kind: 'error' };
  }
  render(found, await minePromise);
})();
