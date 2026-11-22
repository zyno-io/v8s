/**
 * Site-wide links and release plumbing. One place to edit when the domain,
 * the docs or the release asset names change.
 */
const repo = 'https://github.com/zyno-io/v8s';
const docs = 'https://docs.v8s.io';

export const site = {
  name: 'v8s',
  tagline: 'Visual k8s: a fast, multi-cluster Kubernetes manager',
  maker: 'Signal24 LLC dba Zyno Consulting',
  repo,
  releases: `${repo}/releases`,
  latest: `${repo}/releases/latest`,
  licenceText: `${repo}/blob/main/LICENSE`,
  changelog: `${repo}/blob/main/CHANGELOG.md`,
  issues: `${repo}/issues`,
  docs: `${docs}/`,
  consulting: 'https://zyno.consulting',
  privacy: 'https://zyno.consulting/privacy/',
  terms: 'https://zyno.consulting/terms/',
  image: 'ghcr.io/zyno-io/v8s',
  chart: 'oci://ghcr.io/zyno-io/charts/v8s',
} as const;

/** Docs pages the site links to (VitePress, clean URLs). */
export const doc = {
  start: `${docs}/guide/getting-started`,
  install: `${docs}/guide/install`,
  clusters: `${docs}/guide/clusters`,
  linked: `${docs}/guide/clusters#linked-read-and-write-contexts`,
  navigation: `${docs}/guide/navigation`,
  resources: `${docs}/guide/resources`,
  operations: `${docs}/guide/operations`,
  problems: `${docs}/guide/problems`,
  flux: `${docs}/guide/flux`,
  helm: `${docs}/guide/helm`,
  security: `${docs}/guide/security`,
  network: `${docs}/guide/network`,
  hosted: `${docs}/guide/hosted`,
  mobile: `${docs}/guide/mobile`,
  performance: `${docs}/guide/performance`,
  cli: `${docs}/guide/cli`,
  faq: `${docs}/guide/faq`,
  troubleshooting: `${docs}/guide/troubleshooting`,
  demo: `${docs}/guide/getting-started#try-it-without-a-cluster-demo-mode`,
} as const;

/**
 * Release links, fixed at build time for the version in src/data/release.ts (the newest CHANGELOG section). The
 * site is published from the release tag, so these always name assets that exist; nothing is looked up in the browser.
 */
export const releasePage = (version: string) => `${repo}/releases/tag/v${version}`;
export const assetUrl = (version: string, name: string) => `${repo}/releases/download/v${version}/${name}`;
/** Desktop builds are `v8s-<version>-<suffix>`; CLI archives are `v8s_<version>_<suffix>` (release.yml). */
export const platformFile = (version: string, p: Platform) => `v8s-${version}-${p.suffix}`;
export const cliFile = (version: string, c: { suffix: string }) => `v8s_${version}_${c.suffix}`;

export type PlatformId = 'mac-arm64' | 'mac-x64' | 'win-x64' | 'linux-appimage' | 'linux-deb' | 'linux-rpm';

export interface Platform {
  id: PlatformId;
  /** Font Awesome brand icon. */
  icon: string;
  name: string;
  /** Short label for the primary button: "Download for {short}". */
  short: string;
  detail: string;
  /** Release asset name suffix (`v8s-<version>-<suffix>`). */
  suffix: string;
  /** The kind of file, for the alternate-build links ("Package (.deb)"). */
  file: string;
  /** How this build is signed (CHANGELOG 0.1.0, release.yml). */
  trust: string;
}

export const platforms: Platform[] = [
  {
    id: 'mac-arm64',
    icon: 'fa-apple',
    name: 'macOS · Apple silicon',
    short: 'macOS',
    detail: 'M1 and later',
    suffix: 'mac-arm64.dmg',
    file: 'Disk image (.dmg)',
    trust: 'Signed and notarized',
  },
  {
    id: 'mac-x64',
    icon: 'fa-apple',
    name: 'macOS · Intel',
    short: 'macOS (Intel)',
    detail: 'Intel Macs',
    suffix: 'mac-x64.dmg',
    file: 'Disk image (.dmg)',
    trust: 'Signed and notarized',
  },
  {
    id: 'win-x64',
    icon: 'fa-windows',
    name: 'Windows · x64',
    short: 'Windows',
    detail: 'Installer',
    suffix: 'win-x64.exe',
    file: 'Installer (.exe)',
    trust: 'Signed installer',
  },
  {
    id: 'linux-appimage',
    icon: 'fa-linux',
    name: 'Linux · AppImage',
    short: 'Linux',
    detail: 'x86_64, any distribution',
    suffix: 'linux-x86_64.AppImage',
    file: 'AppImage',
    trust: 'SHA-256 checksum',
  },
  {
    id: 'linux-deb',
    icon: 'fa-ubuntu',
    name: 'Linux · deb',
    short: 'Debian / Ubuntu',
    detail: 'amd64, Debian and Ubuntu',
    suffix: 'linux-amd64.deb',
    file: 'Package (.deb)',
    trust: 'SHA-256 checksum',
  },
  {
    id: 'linux-rpm',
    icon: 'fa-fedora',
    name: 'Linux · rpm',
    short: 'Fedora / RHEL',
    detail: 'x86_64, Fedora and RHEL',
    suffix: 'linux-x86_64.rpm',
    file: 'Package (.rpm)',
    trust: 'SHA-256 checksum',
  },
];

/** CLI archives (`v8s_<version>_<suffix>`): `v8s serve`, `v8s server`, the agent. */
export const cliBuilds = [
  { id: 'darwin_arm64', label: 'macOS · arm64', suffix: 'darwin_arm64.tar.gz' },
  { id: 'darwin_amd64', label: 'macOS · amd64', suffix: 'darwin_amd64.tar.gz' },
  { id: 'linux_amd64', label: 'Linux · amd64', suffix: 'linux_amd64.tar.gz' },
  { id: 'linux_arm64', label: 'Linux · arm64', suffix: 'linux_arm64.tar.gz' },
  { id: 'windows_amd64', label: 'Windows · amd64', suffix: 'windows_amd64.zip' },
] as const;
