# Packaging: desktop, hosted, and mobile

One Vue bundle and one Go binary produce every surface:

| Surface | UI | Agent | Identity |
| --- | --- | --- | --- |
| **Desktop app** (macOS, Windows, Linux) | Electron renderer, byte-identical to the hosted bundle | `v8s agent --desktop`, a child process of Electron main | the user's kubeconfig |
| **`v8s serve`** | the system browser on `http://127.0.0.1:<port>` | the same binary, serving the embedded UI | the user's kubeconfig |
| **Hosted web** (desktop and phone browsers) | browser | `v8s server` (self-hosted, multi-user) | each user's own token, forwarded to the API server or through an access gateway ([auth.md](auth.md)) |
| **Installable PWA** (later) | installed web app | `v8s server` | same as hosted |

## Desktop: Electron + Go agent

**Decision: Electron**, with the Go agent as a supervised child process. Tauri was the serious alternative; the reasons it lost:

- **Linux is a first-tier platform for Kubernetes operators**, and Tauri (and Wails) use WebKitGTK there. WebKitGTK has a long tail of GPU/compositing problems (blank windows with DMABUF on NVIDIA, software rendering on some distributions, old WebKit on LTS releases) that directly threaten the scroll budget and xterm.js's WebGL renderer. Electron pins one Chromium on all three OSes, so the budgets measured in CI are what ships.
- **Tauri's Rust core buys nothing here.** The heavy lifting is in the Go agent in every mode; the shell only spawns the agent and opens a window.
- **Memory**: Electron's baseline is ~150–250 MB versus Tauri's ~60–120 MB. With the agent holding 100–300 MB of cluster projections for a typical multi-cluster session, the difference is not what users notice; frame time is. The Electron total stays under the 350 MB idle budget ([performance.md §11](performance.md#11-performance-budgets)).
- **Shipping maturity**: electron-builder and electron-updater, macOS notarization (the agent is signed as a nested executable with the hardened runtime), Windows signing via Azure Artifact Signing, AppImage/deb/rpm, delta updates. Signing reuses the setup Signal24 already runs for sp2p: Azure Artifact Signing (same signing account and service principal, as GitHub environment secrets) for Windows, and the same Apple certificate and App Store Connect API key for macOS. The secrets are copied into this repo's release environment when the release workflow lands (P1), not before.
- **The UI stays WebKit-clean anyway**, because iOS Safari is WebKit. Electron is a consistency choice for desktop, not a Chromium lock-in.

**What would reverse it**: the Playwright WebKit project (WebKitGTK-based on Linux) meeting the scroll, terminal, and log budgets in CI. Because the shell is thin, switching later is cheap; that comparison stays in the nightly suite. Wails was also considered: same system-webview problem, plus it couples an agent crash to a window crash.

### Electron configuration

- `contextIsolation: true`, `sandbox: true`, `nodeIntegration: false`. The renderer has no Node access at all; it only speaks WebSocket to the agent, exactly like the hosted UI.
- **Main process** responsibilities only: spawn and supervise the agent (restart with backoff, crash reporting), auto-update, native menus and shortcuts, deep links (`v8s://c/<cluster>/...`), OS notifications, `safeStorage`/keychain access, native save dialogs for log and YAML downloads. *Status in v0.1.0: the main process spawns and supervises the agent (restart with backoff). Auto-update, `v8s://` deep links, OS notifications, and keychain access are not built; update by installing a newer installer.*
- **Agent handshake**: main spawns the agent with `--listen 127.0.0.1:0`; the agent prints `{port, token}` (256-bit random) on stdout; main hands both to the renderer through the preload bridge. Every WebSocket upgrade must present the token in `Sec-WebSocket-Protocol` (never in a URL) and an allowed `Origin`. This stops other local processes and web pages (via DNS rebinding) from using the user's cluster credentials. The agent exits when its stdin closes, so it never outlives Electron.
- UI and agent ship and update **together**; a protocol version check on connect refuses mismatches.
- Content Security Policy: `default-src 'self'`, `connect-src` limited to the agent's loopback origin; no remote content in the app window. External links open in the system browser.

### `v8s serve`

The agent can serve the embedded UI itself and open the system browser. Useful on Linux without a desktop shell, on remote dev boxes over an SSH tunnel, and for users who prefer a browser tab. Same token handshake as Electron, **without cookies**: the agent prints `http://localhost:<port>/#token=<token>`; the token is in the URL fragment (never sent to a server or logged), the page moves it to `sessionStorage`, clears the fragment, and presents it in `Sec-WebSocket-Protocol` on the data and stream sockets. The static UI assets carry no data, and every data path needs the token, so forwarded in-cluster pages on any loopback port have nothing to steal. Loopback-only bind by default; `Origin`/`Sec-Fetch-Site` checks on every endpoint.

## Hosted: `v8s server`

- **One binary, one Deployment** for the self-hosted install: OIDC login, cluster registry, groups, per-user state, per-user watches and problems, the passthrough, and the embedded UI.
- **Status**: the first cut stores per-user settings as JSON files under `dataDir` (one file per user, one replica); cluster records, strategies and group mappings come from the YAML config file (`v8s server --config`), with every secret read from an environment variable or a file. SQLite/Postgres and encrypted credential storage below are not built yet. The Helm chart is in `charts/v8s`.
- **State store (target)**: SQLite for single-replica installs, Postgres for HA. Stored: cluster records, identity strategies, and IdP-group mappings; the app-registration certificate and any impersonator credentials, encrypted under a KMS or file-provided key; users' groups, stars, bookmarks, layouts, and acknowledgements. Not stored: IdP tokens (session memory only), job state (memory only), and logs of user activity.
- **Scaling**: watches are per `(user, cluster)`, so horizontal scaling shards **users** across replicas (consistent hashing on user id; all of a user's sessions land on one replica). MVP is single replica; sharding is P5 and only needed past roughly 10 GB of active per-user state ([performance.md §10](performance.md#10-hosted-mode-fan-out-and-sharing)).
- **Helm chart** and a container image (distroless, non-root, read-only root filesystem). Flux-friendly: the chart's values cover OIDC, cluster records, and storage.
- **Origins**: the app on `v8s.example.com`; proxied pod HTTP previews (port-forward replacement) on a **separate registrable domain** (`<random-label>.v8s-preview.example.net`). A subdomain of the app's domain would be same-site, so SameSite cookies would not stop in-cluster content from making requests to the app, and that content could set cookies on the parent domain. Session cookies are `__Host-` prefixed (host-only). Previews share one registrable domain with each other, so each preview's token is also a `__Host-` cookie (a sibling preview cannot set or overwrite it), and installs that expose previews to several users should register the preview domain on the Public Suffix List so previews are cross-site to each other.
- **Self-hosted only.** No SaaS is planned, so there is no account or billing control plane and no in-cluster connector.

## Mobile web

The first round is the **hosted web app in a phone's browser**: no app install, no service worker. The phone is a first-class client for the jobs people actually do on a phone: *is anything broken, what, since when, and can I do the one safe thing that fixes it.* It is not a shrunken desktop. Responsive layouts, touch targets, reduced fan-out, and the mobile budgets all apply from P4.

### Installable PWA (later, P5)

Nothing in the first round blocks this; it is additive:

- Web app manifest (name, short name, `start_url: /`, `display: standalone`, theme colours from tokens for light and dark, 192/512 PNG + maskable icons, `shortcuts` for Problems and Search). Installs from Chrome/Edge/Android and via Add to Home Screen on iOS Safari (the recipe Signal24's CI Dashboard uses).
- **Service worker** (production builds only) precaches the **app shell**: HTML, hashed JS/CSS chunks for the shell and the Problems/Search/Detail routes, fonts, icons. Route chunks for heavy desktop features (terminal, YAML editor, graphs) are cached on first use, not precached, to keep the install small.
- **No offline cluster data.** API, auth, and WebSocket traffic are always network. Offline, the shell loads instantly and shows the last-known Problems snapshot (stored in IndexedDB, marked with its age and "offline") plus a reconnect state. A stale snapshot that says it is stale is useful on a train; a fake live view is not.
- **Update flow**: new service worker waits; a toast offers "Reload to update". Never auto-reload mid-action.
- **No web push.** v8s evaluates problems only while a client is open, so there is nothing to push from.

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
- **The server does the watching.** Cluster-side watches are the same as for desktop clients and run on the server, so the phone's battery and data plan never pay for watching clusters. They start when the phone opens v8s and stop shortly after it closes, so a phone check-in is a cold start: WatchList and projection keep it within the budget below.

### Mobile performance budgets

See [performance.md §Budgets](performance.md#11-performance-budgets) for the full table. The phone-specific ones, measured on a mid-range Android (Pixel 6a-class) with 4G throttling:

- Warm start in the phone browser (HTTP-cached assets) to interactive: **≤ 1.5 s**.
- First problems visible after a cold check-in: **≤ 2.0 s**; problems complete for 6 clusters of 10k pods: **≤ 6 s** p95 on 4G.
- Main-thread work per patch: **≤ 4 ms** p95.
- Initial JS for the shell + Problems route: **< 150 KB gzipped**. Terminal, CodeMirror, and graph code are separate chunks.
- Scroll of a 5,000-card list: ≤ 8 ms scripting per frame p95, zero layout shifts.
- Steady-state data on a quiet cluster set: **≤ 30 KB/min** with the problems subscription open.
- Memory: UI heap **≤ 50 MB** with the problems view and one list open.
