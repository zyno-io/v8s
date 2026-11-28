# v8s marketing site

The public site for **v8s**, the fast, multi-cluster Kubernetes manager, at [v8s.io](https://v8s.io). It lives in
the v8s repo at `sites/www/`. A static [Astro](https://astro.build) site built the same way as the Siple site (Astro,
npm, Playwright VRT, GitHub Actions → ZynoHosting), in the PixelCI design system the v8s app itself uses, with v8s indigo. See `DESIGN.md` and `PRODUCT.md`.

```bash
npm ci          # or npm install; run everything here in sites/www/
npm run dev      # http://localhost:4321
npm run build    # static build → dist/
npm run preview  # serve dist/
npm run vrt      # build, then capture screenshots/ (local only, nothing is uploaded)
npm run og       # regenerate public/og/home.png and the icons
```

## Pages

| Route | Notes |
| --- | --- |
| `/` | Hero with the download card and the **interactive demo**; why it's fast; CPU and memory at a glance; multi-cluster; Problems; Flux and Helm; security and network; safe edits; three ways to run; trust; downloads; footer with the Zyno Consulting band |
| `/404` | Not found (`noindex`) |
| `/sitemap.xml` | Hand-listed (`src/pages/sitemap.xml.ts`); add new pages there |
| `/release.json` | `{"version", "date", "url"}` of the newest release (`src/pages/release.json.ts`), read by the v8s update check; see below |

## The interactive demo

`src/components/demo/Demo.astro` and `src/scripts/demo/` are a working replica of the v8s UI that runs entirely in
the page on seeded synthetic data (`src/scripts/demo/data.ts`: three clusters, ~50,000 pods, deployments, nodes, Helm
releases, Kustomizations, problems; `usage.ts` makes synthetic CPU and memory as a pure function of the pod index and a
4-second tick, so nothing is stored per pod). The Overview (`:ov`, first item under Cluster) shows one card per cluster derived from the same data, and its counts and chips jump to the filtered tables. The Pods and Nodes tables carry the usage block and sort by it; its percentage cells have the app's row-height bars (green below 70 %, amber from 70 %, red from 90 %; a request percentage is barred only without a limit, and a memory request next to a limit is tinted amber above 100 % and red above 200 %), with the explanation in each header's tooltip, and `:ip 10.20.`
filters pods by address prefix. Row keys follow the app's k9s-style set where the demo has the thing to act on: `y` opens the YAML tab, `c` / `n` copy the name / namespace (with a toast), and `Shift+J` jumps to the owner. The demo models Deployments but no ReplicaSets, so a pod jumps straight to its Deployment (the app goes Pod → ReplicaSet → Deployment). Not in the demo: delete, restart, port-forward, open node (they change or need a live cluster), and bare `:ns` (the demo has no Namespaces list). The table shows a single focus ring: the scroller's own ring is dropped while a row is focused. No network requests. Contract with the page:

- Any element with `data-demo-cmd=":pb"` runs that command in the demo (scrolls it into view and focuses it); the
  section "Run" chips and the header's `:` button use this. `window.v8sDemo.run(cmd)` does the same.
- `:` anywhere outside a text field opens the demo's command bar. Other single-key shortcuts work only while focus is
  inside the demo.
- It says "synthetic data" in its own top bar, and the page calls it a replica. Keep both.

When the app's UI changes, update the demo to match (compare against fresh screenshots).

## Content sources: read before editing copy

- **Product claims** come only from the v8s repo (the repo root, `../..`): `README.md`, `CHANGELOG.md`, `docs/`. Performance
  figures are the **design budgets** in `docs/performance.md` and are labeled as budgets, never as measurements.
  No user counts, logos, testimonials or benchmarks.
- Things v8s does **not** do (offline mode, web push, auto-update, Prometheus, Argo CD, hosted port-forward…) are listed
  at the end of the v8s CHANGELOG (0.1.0 "Not in this release"); don't claim them.
- **Zyno Consulting band** (`src/components/SiteFooter.astro`) uses only claims on zyno.consulting itself
  (`../zyno-consulting-web/src`) and never implies v8s needs it.
- **Screenshots** in `public/shots/` are real captures of the app in demo mode (`v8s agent --fake`, synthetic
  clusters), made by `scripts/capture-shots.ts` (see its header for the rerun command), never retouched. Each has a
  `.json` sidecar with its provenance.

## Downloads and release state

The version, date and every download link are fixed at build time: `src/data/release.ts` reads the newest released
section of the repo's `CHANGELOG.md` (`## X.Y.Z — YYYY-MM-DD`), and `src/data/site.ts` turns it into GitHub release asset
URLs (`v8s-<version>-<suffix>` for desktop builds, `v8s_<version>_<suffix>` for CLI archives, `checksums.txt`). Nothing
is fetched in the browser. Production is deployed by `release.yml` after the GitHub release is published (see Deploy), so the links always name
assets that exist.

`src/scripts/download.ts` only features the visitor's build: it detects the OS from the user agent and the Mac's CPU
from `navigator.userAgentData` where the browser offers it (Safari doesn't, so Apple silicon is featured with an Intel
link beside it), points the hero button at that row's link, and lists the sibling builds. Without JS the hero button
leads to the download list. Platform names and signing notes live in `src/data/site.ts`.

## release.json

`/release.json` (`src/pages/release.json.ts`, built to `dist/release.json`) is `{"version": "0.8.0", "date": "2026-11-22",
"url": "<release page>"}`, taken from the same CHANGELOG section as the downloads. The v8s agent
(`agent/internal/updatecheck`) fetches `https://v8s.io/release.json` every 15 minutes (with `If-None-Match`) to offer
updates, instead of calling the rate-limited GitHub API. The shape is a contract with shipped agents: `version` is required and
must be a stable semver, written without the `v` (agents ignore prereleases and anything unparseable); keep it when adding fields, and don't make it
dynamic or cached for long. The agent ignores `url` and builds the GitHub release link itself.

## Search and social previews

`BaseLayout` sets the canonical URL, Open Graph and Twitter tags, icons and JSON-LD (Organization; the home page adds
WebSite and SoftwareApplication from `src/data/seo.ts`). `scripts/og.mjs` renders `public/og/home.png` and the PNG
icons from the v8s logo; `public/favicon.svg` is the logo itself. Rerun and commit when the headline or a screenshot
changes.

## Deploy

The domain is `v8s.io`; it is named in `astro.config.mjs`, `public/robots.txt`, `src/data/seo.ts`,
`src/pages/sitemap.xml.ts` and `.github/workflows/www.yml`.

The `Website` GitHub Actions workflow (`.github/workflows/www.yml`) builds `dist/` and publishes it with the
ZynoHosting CLI:

- `www-staging.v8s.io` (alpha cluster, `--environment alpha`) deploys automatically on every push to `main` that
  touches `sites/www/**` or the workflow.
- `v8s.io` (production) deploys only by `workflow_dispatch` with target `production`, from `main` or a stable release tag
  (`vX.Y.Z`; the build reads the new version from `CHANGELOG.md`). `release.yml`'s publish job dispatches it as its last step on
  a stable tag, after the GitHub release and its assets exist (release candidates such as `v0.8.0-rc.1` never deploy). A manual run
  (Actions > Website > Run workflow) works too. The deploy job first reads the version from the built `dist/release.json` and fails
  unless `gh release view v<version>` finds a published (not draft, not prerelease) release, so a version can never be announced
  before it is released.
- Pull requests only build.

`release.yml` ignores pushes that touch only `sites/www/**` and the workflow, so site edits don't rebuild the app.

Each deploy job reads a per-site **deploy token** (`ZYNOHOSTING_DEPLOY_TOKEN`) from its GitHub environment
(`www-staging`, `www-production`). Create or rotate one with
`zynohosting deploy-tokens create <site> --production --github zyno-io/v8s --github-environment <env>`
(plus `--environment alpha` for staging); the CLI stores it as the environment secret without printing it. Both
ZynoHosting sites must exist first.

The deploy jobs install the CLI from `deploy/package-lock.json` (`npm ci --ignore-scripts`, Node 24 per
`deploy/.nvmrc`), so its whole dependency tree is pinned. To upgrade, bump the version in `deploy/package.json`
and run `npm install --package-lock-only --ignore-scripts` there.

VRT captures (`npm run vrt`) run locally only. Re-capturing the app screenshots (`scripts/capture-shots.ts`) is also a
local step.
