# Packaging: desktop, hosted, and mobile

One Vue bundle and one Go binary produce every surface:

| Surface | UI | Agent | Identity |
| --- | --- | --- | --- |
| **Desktop app** (macOS, Windows, Linux) | Electron renderer, byte-identical to the hosted bundle | `v8s agent --desktop`, a child process of Electron main | the user's kubeconfig |
| **`v8s serve`** | the system browser on `http://127.0.0.1:<port>` | the same binary, serving the embedded UI | the user's kubeconfig |
| **Hosted web + PWA** | browser / installed PWA | `v8s server` (multi-tenant) | forwarded OIDC identity ([auth.md](auth.md)) |
| **Cluster connector** (P5) | n/a | `v8s connector` in a private cluster, dialling out to `v8s server` | relays the user's identity |

## Desktop: Electron + Go agent

**Decision: Electron**, with the Go agent as a supervised child process. Tauri was the serious alternative; the reasons it lost:

- **Linux is a first-tier platform for Kubernetes operators**, and Tauri (and Wails) use WebKitGTK there. WebKitGTK has a long tail of GPU/compositing problems (blank windows with DMABUF on NVIDIA, software rendering on some distributions, old WebKit on LTS releases) that directly threaten the scroll budget and xterm.js's WebGL renderer. Electron pins one Chromium on all three OSes, so the budgets measured in CI are what ships.
- **Tauri's Rust core buys nothing here.** The heavy lifting is in the Go agent in every mode; the shell only spawns the agent and opens a window.
- **Memory**: Electron's baseline is ~150–250 MB versus Tauri's ~60–120 MB. With the agent holding 100–300 MB of cluster projections for a typical multi-cluster session, the difference is not what users notice; frame time is. The Electron total stays under the 350 MB idle budget ([performance.md §11](performance.md#11-performance-budgets)).
- **Shipping maturity**: electron-builder and electron-updater, macOS notarization (the agent is signed as a nested executable with the hardened runtime), Windows signing via Azure Trusted Signing, AppImage/deb/rpm, delta updates.
- **The UI stays WebKit-clean anyway**, because the iOS PWA runs on WebKit. Electron is a consistency choice for desktop, not a Chromium lock-in.

**What would reverse it**: the Playwright WebKit project (WebKitGTK-based on Linux) meeting the scroll, terminal, and log budgets in CI. Because the shell is thin, switching later is cheap; that comparison stays in the nightly suite. Wails was also considered: same system-webview problem, plus it couples an agent crash to a window crash.

### Electron configuration

- `contextIsolation: true`, `sandbox: true`, `nodeIntegration: false`. The renderer has no Node access at all; it only speaks WebSocket to the agent, exactly like the hosted UI.
- **Main process** responsibilities only: spawn and supervise the agent (restart with backoff, crash reporting), auto-update, native menus and shortcuts, deep links (`v8s://c/<cluster>/...`), OS notifications, `safeStorage`/keychain access, native save dialogs for log and YAML downloads.
- **Agent handshake**: main spawns the agent with `--listen 127.0.0.1:0`; the agent prints `{port, token}` (256-bit random) on stdout; main hands both to the renderer through the preload bridge. Every WebSocket upgrade must present the token in `Sec-WebSocket-Protocol` (never in a URL) and an allowed `Origin`. This stops other local processes and web pages (via DNS rebinding) from using the user's cluster credentials. The agent exits when its stdin closes, so it never outlives Electron.
- UI and agent ship and update **together**; a protocol version check on connect refuses mismatches.
- Content Security Policy: `default-src 'self'`, `connect-src` limited to the agent's loopback origin; no remote content in the app window. External links open in the system browser.

### `v8s serve`

The agent can serve the embedded UI itself and open the system browser. Useful on Linux without a desktop shell, on remote dev boxes over an SSH tunnel, and for users who prefer a browser tab. Same token handshake, printed as a one-time URL on `http://localhost:<port>` that sets a host-only, HttpOnly, SameSite=strict cookie and redirects. Port-forward "Open in browser" links use `127.0.0.1`, a different cookie host, so forwarded in-cluster pages never receive that cookie ([operations.md §Port-forward](operations.md#port-forward)). Loopback-only bind by default; `Origin`/`Sec-Fetch-Site` checks on every endpoint.

## Hosted: `v8s server`

- **One binary, one Deployment** for the open-source self-hosted install: OIDC login, cluster registry, groups, per-user state, audit, shared caches, problems, the passthrough, and the embedded UI.
- **State store**: SQLite for single-replica installs, Postgres for HA. Stored: cluster records and identity strategies, encrypted credentials (impersonator tokens, IdP refresh tokens) under a KMS or file-provided key, users' groups/stars/bookmarks/layouts/acknowledgements, audit log, long-running job records (drains, Helm actions).
- **Scaling**: caches are per cluster, so horizontal scaling shards **clusters** across replicas (consistent hashing on cluster id), with sessions routed to the replica that owns each cluster's cache, or proxied between replicas. MVP is single replica; sharding is P5.
- **Helm chart** and a container image (distroless, non-root, read-only root filesystem). Flux-friendly: the chart's values cover OIDC, cluster records (or a CRD-based cluster registry, an open question), and storage.
- **Origins**: the app on `v8s.example.com`; proxied pod HTTP previews (port-forward replacement) on a **separate registrable domain** (`<random-label>.v8s-preview.example.net`). A subdomain of the app's domain would be same-site, so SameSite cookies would not stop in-cluster content from making requests to the app, and that content could set cookies on the parent domain. Session cookies are `__Host-` prefixed (host-only).
- A future Signal24 SaaS control plane (accounts, billing) would be a separate TS Server Foundation service that mints JWTs `v8s server` validates. It stays out of the data path.

## Mobile (PWA)

The hosted web app is an installable **PWA** and the phone is a first-class client for the jobs people actually do on a phone: *is anything broken, what, since when, and can I do the one safe thing that fixes it.* It is not a shrunken desktop.

### Installability and offline

- Web app manifest (name, short name, `start_url: /`, `display: standalone`, theme colours from tokens for light and dark, 192/512 PNG + maskable icons, `shortcuts` for Problems and Search). Installs from Chrome/Edge/Android and via Add to Home Screen on iOS Safari. Same recipe as the CI Dashboard.
- **Service worker** (production builds only) precaches the **app shell**: HTML, hashed JS/CSS chunks for the shell and the Problems/Search/Detail routes, fonts, icons. Route chunks for heavy desktop features (terminal, YAML editor, graphs) are cached on first use, not precached, to keep the install small.
- **No offline cluster data.** API, auth, and WebSocket traffic are always network. Offline, the shell loads instantly and shows the last-known Problems snapshot (stored in IndexedDB, marked with its age and "offline") plus a reconnect state. A stale snapshot that says it is stale is useful on a train; a fake live view is not.
- **Update flow**: new service worker waits; a toast offers "Reload to update". Never auto-reload mid-action.
- **Web push** (P5): notifications for new critical problems in chosen cluster groups, via the Push API (supported for installed PWAs on iOS 16.4+ and everywhere on Android). Off by default.

### What works great on a phone

| Feature | Phone | Notes |
| --- | --- | --- |
| Problems (cross-cluster) | **Primary** | Default landing tab. Grouped by workload, severity filter, acknowledge, snooze |
| Cluster selection and groups | Yes | Group chips are the fast path on a phone |
| Search (objects by name across selected clusters) | Yes | Agent-side search, windowed results |
| Object detail: status, conditions, events, related | Yes | Full-screen page |
| Logs | Yes, tail mode | Last 500 lines, follow in 1 s batches, 5k-line ring buffer, search; multi-pod merge is desktop-primary |
| Flux: status, suspend/resume, reconcile | Yes | One-tap reconcile is the most common phone action |
| Helm: release status, history, rollback | Yes | Rollback with hold-to-confirm |
| Scale, restart, delete pod, trigger CronJob | Yes | Hold-to-confirm |
| Cordon / uncordon | Yes | |
| Security summaries (vulns per image, policy fails) | Yes, summary | Full CVE tables are desktop-primary but work |
| Pod shell | Secondary | Simplified terminal with accessory keys; warned as desktop-best |
| YAML edit | Secondary | Read is fine; editing works but is not designed for |
| Drain | Yes, with pre-flight | The pre-flight summary (PDB blocks, unmanaged pods) is shown in full before the confirm |
| Node shell | **Desktop only** | Hidden on phones (`pointer: coarse` and width < 640px) |
| Port-forward | **Desktop only** | Meaningless on a phone (hosted HTTP preview excepted) |
| Dense multi-cluster tables (all pods across 5 clusters) | Desktop-primary | Works as cards, but the phone UI steers to filtered views |
| Hubble service map, Flux graphs | Desktop-primary | Phone shows lists (drops, dependencies) instead of graphs |
| Bulk actions across clusters | Desktop only | Too easy to fat-finger |

### Reduced fan-out on small screens

The phone talks to the same agent as the desktop browser. The agent does the watching; the phone decides how much it **subscribes** to (details in [performance.md §11](performance.md#mobile-specifics)).

- **Problems-first.** On launch the phone subscribes to the problems summary (cluster × class counts, ~1 KB) and the problems window. It opens no list subscriptions until the user navigates to one.
- **Small windows, few columns.** ± 60 rows around the viewport and the 4 fields a card shows, instead of ± 300 rows and 10 columns.
- **Slow tick.** One patch per subscription per second (2 s on `saveData` or 2g/3g), versus 100–250 ms on desktop.
- **Disabled classes are filtered in the agent**, so a phone never receives problems it would hide.
- **Background = unsubscribe.** On `visibilitychange: hidden` ticks pause; after 30 s every subscription is dropped. iOS kills backgrounded sockets anyway; because sync is state-based, resuming costs one frame per view.
- **The agent does not shrink.** Cluster-side watches are the same as for desktop clients. That is deliberate: they feed the problems engine, and the phone gets instant answers because the agent is already warm. The phone's battery never pays for watching clusters.

### Mobile performance budgets

See [performance.md §Budgets](performance.md#11-performance-budgets) for the full table. The phone-specific ones, measured on a mid-range Android (Pixel 6a-class) with 4G throttling:

- App shell from installed PWA to interactive (warm start, cached shell): **≤ 1.5 s**.
- Problems list with live data after launch (warm agent): **< 2.0 s**.
- Main-thread work per patch: **≤ 4 ms** p95.
- Initial JS for the shell + Problems route: **< 150 KB gzipped**. Terminal, CodeMirror, and graph code are separate chunks.
- Scroll of a 5,000-card list: ≤ 8 ms scripting per frame p95, zero layout shifts.
- Steady-state data on a quiet cluster set: **≤ 30 KB/min** with the problems subscription open.
- Memory: UI heap **≤ 50 MB** with the problems view and one list open.
