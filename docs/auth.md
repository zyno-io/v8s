# Authentication and identity

Goal: **every Kubernetes request v8s makes carries the real end user's identity**, so cluster RBAC, admission, and the API server audit log all see the person, not "v8s". That is easy on the desktop (the user's kubeconfig already is their identity) and the hard part of the hosted design.

## Desktop

The desktop agent is a Kubernetes client on the user's machine. It uses the user's kubeconfig exactly as `kubectl` would.

- **Sources**: `$KUBECONFIG` (all paths, merged), `~/.kube/config`, extra files or directories added in settings, and **imported** kubeconfigs (copied into a v8s-managed file for offline use; the original is never rewritten). Files are watched; a new context appears without a restart.
- **Credential types**: client certificates, bearer tokens, token files, basic auth (legacy), and **exec credential plugins** (`client.authentication.k8s.io/v1`): `kubelogin` (OIDC), `aws eks get-token`, `gke-gcloud-auth-plugin`, `kubelogin` for Azure, Teleport `tsh`, `pinniped`, and anything else that speaks the protocol. The agent runs plugins itself with the user's environment and `PATH` (a GUI app on macOS does not inherit the shell's `PATH`; the agent resolves the login shell's `PATH` once at start, as VS Code does).
- **Login-shell environment**: plugins are run with an environment captured once from the user's login shell (`$SHELL -ilc 'env -0'`, 5 s timeout), because apps launched from the macOS GUI otherwise lack the `PATH` entries for `aws`, `gke-gcloud-auth-plugin`, and `kubelogin`.
- **Browser-based plugins** (e.g. `kubelogin` opening a browser for OIDC): the agent shows "Waiting for sign-in for `prod-eu` in your browser" on that cluster only; other clusters keep working. The first authentication per identical plugin command is **serialized**, so five contexts sharing one IdP open one browser login, not five. The returned `ExecCredential` is cached until `expirationTimestamp`.
- **Plugins that prompt on stdin** (MFA codes typed into a terminal) are out of scope for v1; v8s detects them (plugin waiting on stdin with `interactiveMode: Always`) and suggests running the plugin once in a terminal to warm its cache.
- **Built-in OIDC login** for clusters that trust an OIDC issuer but whose kubeconfig has no plugin: v8s can act as the client itself (authorization code + PKCE, loopback redirect, refresh token in the OS keychain). This is also how v8s writes a working kubeconfig for a teammate ("Copy kubeconfig for this cluster" emits an `exec` entry pointing at `v8s auth token --cluster X`).
- **Secrets at rest**: anything v8s itself stores (OIDC refresh tokens, imported tokens) goes in the OS keychain (macOS Keychain, Windows Credential Manager, libsecret), never in plain settings files.
- **Proxy**: honours `proxy-url` in kubeconfig and `HTTPS_PROXY`/`NO_PROXY`; supports SOCKS5 for bastion setups.

## Gateway-fronted clusters (Warpgate)

The primary deployment v8s is designed for puts every cluster behind an **identity-aware access gateway**, specifically [Warpgate](https://github.com/warp-tech/warpgate). Teleport and similar gateways fit the same model. The gateway:

- authenticates the person with OIDC (the gateway hands out a kubeconfig whose exec profile is `kubelogin`);
- exposes one Kubernetes endpoint per **target**, where a target is a cluster × role (for example an everyday `operator` role and a just-in-time `cluster-admin` role that needs an approved ticket);
- re-evaluates the person's grant for that target **on every request** and records the session;
- connects upstream with its own short-lived credential for a **fixed upstream identity per target** (for example a five-minute client certificate for `gateway-operator`), so the API server authorizes the target's role and the gateway attributes the request to the person.

**Rule: the user's token goes all the way through.** Every request v8s makes to a gateway-fronted cluster (lists, watches, gets, writes, exec, logs, port-forward) carries **that user's own gateway token** to the target's URL. v8s never holds a gateway or upstream credential of its own for such a cluster. It never reads cluster data under any identity other than the user's, and it never bypasses the gateway. That rule is why hosted mode uses per-user watches for these clusters (see [performance.md §10](performance.md#10-hosted-mode-fan-out-and-sharing)).

### Desktop

The gateway's kubeconfig works as-is: v8s runs the `kubelogin` exec plugin like any other ([Desktop](#desktop)). All targets share one exec profile, so the serialized first authentication means **one browser sign-in covers every target**.

### Hosted

The v8s server needs, for each signed-in user, the same token the `kubelogin` profile would obtain. It acquires it **server-side**, refreshes it server-side, and never sends it to the browser.

- **Gateway trusts the organization's IdP** (for example Entra ID): v8s signs users in with the same IdP and obtains a token for the gateway's client (OIDC client ID / audience) via on-behalf-of or RFC 8693 token exchange, or by requesting that audience directly if v8s is registered as a client permitted to do so.
- **Gateway is its own issuer**: v8s signs users in **through the gateway's OIDC** (v8s becomes a client of the gateway), so the login itself produces the token the gateway accepts.

Which of the two applies is [open question Q2](architecture.md#open-questions).

### Access profiles: one cluster, several targets

- v8s treats a **cluster** as one thing with several **access profiles** (one per target or context). Contexts are grouped automatically by the **`kube-system` namespace UID**, a stable cluster identifier readable through any profile that can `get namespaces`. An explicit mapping in settings is the fallback.
- **Watches always run on the default (least-privileged) profile.** Elevated profiles are used **per action** ("Run as cluster-admin" in the action menu and confirm dialog) or for an explicitly elevated, time-boxed session with a visible banner. They are never used for background watches.
- **JIT tickets**: if the gateway rejects an elevated request because no ticket is active or it has expired, v8s shows "Ticket required for `<target>`" with a link to the gateway's ticket page. It does not retry in a loop.
- **RBAC-aware UI** evaluates the profile's upstream identity: `SelfSubjectReview` returns the fixed target identity, which is exactly what the API server authorizes. Buttons are enabled or disabled per profile, and an action allowed only on the elevated profile says so.

### Attribution

- The gateway records the person, target, ticket decision, and requests.
- v8s's own audit log records the person, cluster, profile (target), and action.
- v8s sends a `User-Agent` of `v8s/<version> (session <id>)` so the two logs can be correlated where the gateway records request metadata.
- The Kubernetes audit log sees only the fixed upstream identity. That is the gateway's design, not something v8s can or should work around.

### Gateway effects on performance and features (verify in P0)

- Every watch is one long-lived request through the gateway. A grant revoked mid-watch is the gateway's to enforce. As a backstop, v8s caps watch duration (`timeoutSeconds` ≤ 30 min), so every watch is re-authorized at least that often.
- Protobuf and metadata-only watches reduce what the gateway proxies and records. Projection does not (it happens after the bytes arrive). Watch volume through the gateway is therefore a budget to measure, alongside gateway CPU and the size of its session recordings.
- P0 must confirm, through Warpgate's Kubernetes target support:
  - watch and WatchList;
  - the protobuf content type;
  - `as=Table` requests;
  - exec/attach/port-forward upgrades (WebSocket `v5.channel.k8s.io` and the SPDY fallback);
  - how session recording treats long watch streams.

  Any gap becomes a fallback recorded in [performance.md](performance.md) (for example JSON instead of protobuf).

## Hosted

### Login to v8s

Users sign in to v8s with **OIDC** (Keycloak, Okta, Entra ID, Google, Dex, GitLab, GitHub via Dex). No local passwords. The v8s server keeps the IdP refresh token server-side (encrypted at rest) **only for the lifetime of the v8s session**: it is deleted at logout and at session expiry, and sessions have an absolute lifetime (default 12 h). The browser holds only a host-only `__Host-` session cookie (HttpOnly, Secure, SameSite=strict). v8s maps IdP claims to:

- a **username** (configurable claim, default `email` or `preferred_username`, with an optional prefix such as `oidc:`), and
- **groups** (configurable claim, default `groups`, optional prefix).

The mapping must match what each cluster expects, because that string is what RBAC bindings name. Mapping is configured per cluster (defaulting to a global mapping).

### Forwarding identity to clusters

Each cluster record has one **identity strategy**. Gateway-fronted clusters use **gateway pass-through** ([above](#gateway-fronted-clusters-warpgate)), the expected deployment. Clusters reached directly use one of these four, in order of preference:

#### 1. OIDC pass-through (preferred)

The API server trusts the same IdP. v8s sends the user's own IdP-issued token as the bearer token.

- **When it works**: self-managed clusters with `--oidc-*` flags or, better, **Structured Authentication Configuration** (`AuthenticationConfiguration`, GA since Kubernetes 1.34), which allows several JWT issuers and audiences and CEL claim mappings; EKS with an associated OIDC identity provider; AKS with Entra ID integration (via token exchange below); GKE via Identity Service or Workforce Identity Federation; Rancher-, Pinniped-, or Teleport-fronted clusters with their own variants.
- **Audience: one per cluster, by default.** v8s performs an **RFC 8693 token exchange** (Keycloak, Okta, and others) or an on-behalf-of flow (Entra) at the IdP for a short-lived token whose `aud` is that cluster alone. A token handed to cluster `dev` is then useless against cluster `prod`, so neither a `dev` operator reading request logs nor a compromised connector can replay it elsewhere.
- **Never forward the v8s login ID token.** Its audience is v8s itself.
- **Shared audience is an explicit opt-in** ("these clusters are one trust domain"), for IdPs without token exchange. It uses a **separate OIDC client registered for that trust domain** (not the v8s login client): v8s obtains that client's token for the user via the IdP's refresh flow, and every cluster in the domain trusts that client's audience. The cluster settings screen states the consequence: any cluster in the domain can replay a user's token against the others.
- **Refresh**: the server refreshes tokens before expiry using the stored refresh token; a failed refresh marks the user's clusters `auth expired` and prompts re-login.
- **Risk profile**: best of the four. v8s holds no cluster credential. It does hold IdP refresh tokens for users with live sessions, so a server compromise yields the ability to mint tokens for those users until their sessions expire or the refresh tokens are revoked at the IdP. Mitigations: refresh tokens encrypted with a KMS-held key, deleted at logout/expiry, IdP-side refresh-token rotation and short absolute lifetimes, and per-cluster audiences so stolen access tokens are narrow.

#### 2. Cloud-native token exchange

For managed clusters whose API server trusts the cloud IAM, not the IdP:

- **EKS**: v8s exchanges the user's IdP token at AWS STS (`AssumeRoleWithWebIdentity`, with the IdP registered as an IAM OIDC provider), setting the **role session name to the user's identity**, and presents an EKS token for that session. EKS access entries map the role to Kubernetes groups; the API server audit log shows the session name. Per-user RBAC needs per-group roles or access-entry mapping by session tag; this is coarser than true OIDC, and the doc for operators says so.
- **AKS (Entra ID)**: on-behalf-of flow: exchange the user's Entra token for one with the AKS server application audience. The API server sees the real Entra user and groups. This is genuinely per-user.
- **GKE**: Workforce Identity Federation: exchange the IdP token at Google STS for a federated access token; GKE RBAC sees the workforce principal. Per-user.

#### 3. Impersonation

For any cluster that can't trust the IdP, v8s authenticates as a dedicated identity (a ServiceAccount with a bound, short-lived token from the TokenRequest API, or a client certificate) that holds `impersonate` permission, and sets `Impersonate-User` and `Impersonate-Group` headers with the mapped user identity.

- **When**: on-prem clusters without OIDC, clusters where adding an issuer is not possible, mixed fleets.
- **The honest risk**: classic RBAC can scope `impersonate` with `resourceNames`, but user names are unbounded, so an impersonator that can impersonate "any user" can impersonate whoever holds cluster-admin. **Classic impersonation is admin-equivalent if the credential leaks.** v8s mitigates, and says so in the cluster setup screen:
  - `impersonate` on `groups` must be limited by `resourceNames` to an explicit allowlist; v8s refuses to save a cluster whose impersonator can impersonate `system:masters` or any `system:` group, and refuses `impersonate` on `serviceaccounts`. These are checked with **`SelfSubjectAccessReview` probes** (impersonate `groups/system:masters`, `groups/system:nodes`, a random `system:` group, `serviceaccounts`, and a random user name), at save time and on every connect. Rules reviews are not authoritative and are not used for this check.
  - v8s only ever sends usernames produced by its own claim mapping, with a mandatory prefix (default `v8s:`), and hard-denies any mapped name or group starting with `system:` in code, so a v8s *bug* cannot impersonate `admin`; only a stolen credential could.
  - Where the cluster supports **constrained impersonation** (KEP-5284: alpha 1.35, beta 1.36, GA targeted 1.38), v8s uses it to limit which users and which actions the impersonator may impersonate. That removes the admin-equivalence and becomes the recommended setup once GA.
  - `userextras` impersonation carries the IdP `sub` and the v8s session ID (header `Impersonate-Extra-v8s.dev%2Fsession`: extra keys are percent-encoded, which client-go does automatically), so the API server audit log ties every request to a v8s session.
- **Risk profile**: the impersonator credential is a high-value secret. It never leaves the server, is encrypted at rest with a KMS-held key, is a bound token rotated hourly where possible, and its use is limited to requests carrying a valid user session.

#### 4. Shared credential (read-only kiosk)

A single read-only ServiceAccount for everyone. Only for wallboards and demos. v8s marks such clusters `shared identity` everywhere in the UI and disables every mutation, shell, and port-forward regardless of what the credential allows.

### Reachability

Hosted v8s is **self-hosted only** (no SaaS is planned). It reaches clusters through the access gateway, or over private networking for clusters reached directly. No in-cluster connector is planned.

### Per-request flow (hosted)

```
browser ──(session cookie, WS)──▶ v8s server
    session → user (sub, username, groups, IdP tokens)
    cluster → strategy
        gateway:           https://<gateway>/<target>   Authorization: Bearer <this user's gateway token>
                           (the gateway authorizes the person per request, records it, and connects upstream
                            as the target's fixed identity)
        oidc-passthrough:  Authorization: Bearer <token exchanged for this cluster's audience>
        cloud-exchange:    Authorization: Bearer <EKS/AKS/GKE token for this user>
        impersonation:     Authorization: Bearer <impersonator token>
                           Impersonate-User: <mapped username>
                           Impersonate-Group: <mapped groups…>
                           Impersonate-Extra-v8s.dev%2Fsession: <session id>
    ──▶ kube-apiserver (RBAC evaluates the real user)
```

**Gateway-fronted clusters: everything is per user.** Lists, watches, problems inputs, and writes all run as the user through the gateway (see [performance.md §10](performance.md#10-hosted-mode-fan-out-and-sharing) for the cost model).

**Clusters reached directly** may opt in to a read optimization:

- **List and problem reads of tier-0 kinds** come from a **shared per-cluster cache**. A hub ServiceAccount that cannot read Secrets reads it. Results are filtered per user by SubjectAccessReview, fail-closed. This never widens what a user sees.
- **Everything else** goes to the API server **as the user** through the strategy above, rate-limited per user and per cluster. That covers Secrets (including Helm release metadata), full-object GETs, logs, exec, port-forward, Hubble flows, and every write.

## Desktop vs hosted summary

| | Desktop | Hosted |
| --- | --- | --- |
| Who authenticates to clusters | the user's kubeconfig credential (for gateway-fronted clusters, the gateway's `kubelogin` profile) | the user's own gateway token, forwarded per request (gateway-fronted); otherwise the user's identity via one of the four strategies |
| Where credentials live | user's machine, OS keychain for v8s-held secrets | v8s server only, encrypted; never in the browser |
| Exec plugins | yes, natively | no (no user machine); replaced by strategies 1–3 |
| Cluster list | user's kubeconfig contexts | admin-configured cluster records, filtered to clusters the user has any access to |
| Step-up for destructive actions | optional OS-level confirm | optional IdP re-auth (`max_age`/`prompt=login`) for production groups |

## RBAC awareness

Independent of strategy, the agent first resolves the user's identity **as the API server sees it** with `SelfSubjectReview` (exact username, groups, and extras after the cluster's own prefixes and claim mappings), then asks what the user can do (`SelfSubjectRulesReview` per namespace on first visit, `SelfSubjectAccessReview` for specific actions) and caches results for 60 s per user per cluster. The UI disables what the user cannot do and says why. In hosted mode this also decides which clusters appear in a user's selector, using **cheap signals only** (proving that a user has no access anywhere would need a SubjectAccessReview per namespace on webhook-authorizer clusters):

- a cluster-wide `list` on any tier-0 kind (a handful of SubjectAccessReviews), or
- a namespace configured for the user's groups on that cluster by an admin, or the user's own saved namespaces, confirmed by one SubjectAccessReview each, or
- an admin pin for a group.

Namespace-scoped users therefore see their clusters as long as an admin has mapped their groups to namespaces (or they have added a namespace themselves), without a sweep. Clusters with no positive signal are hidden.

On desktop, a namespace-scoped user's namespaces cannot be discovered from the API (rules reviews take a namespace as input). v8s uses the kubeconfig context's namespace plus a per-cluster namespace list in settings, and offers to add namespaces as the user navigates to them.
