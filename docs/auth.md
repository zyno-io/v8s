# Authentication and identity

**Rule: the user's own credential goes all the way through.** Every request v8s makes (lists, watches, gets, writes, exec, logs, port-forward) carries the signed-in person's own credential to whatever endpoint the cluster is configured with. That endpoint may be an API server directly, or an access gateway such as [Warpgate](https://github.com/warp-tech/warpgate), which authorizes and records the person and then forwards to the cluster. v8s never reads cluster data under an identity of its own, unless an admin of a hosted install explicitly configures one for a specific cluster (impersonation or a read-only kiosk, below).

That is easy on the desktop (the user's kubeconfig already is their identity) and the main constraint on the hosted design.

## Desktop

The desktop agent is a Kubernetes client on the user's machine. It uses the user's kubeconfig exactly as `kubectl` would.

- **Sources**: `$KUBECONFIG` (all paths, merged), `~/.kube/config`, extra files or directories added in settings, and **imported** kubeconfigs (copied into a v8s-managed file for offline use; the original is never rewritten). Files are watched; a new context appears without a restart.
- **Credential types**: client certificates, bearer tokens, token files, basic auth (legacy), and **exec credential plugins** (`client.authentication.k8s.io/v1`): `kubelogin` (OIDC), `aws eks get-token`, `gke-gcloud-auth-plugin`, `kubelogin` for Azure, Teleport `tsh`, `pinniped`, and anything else that speaks the protocol. Real kubeconfigs mix all of these; v8s handles each context independently.
- **Login-shell environment**: plugins are run with an environment captured once from the user's login shell (`$SHELL -ilc 'env -0'`, 5 s timeout), because apps launched from the macOS GUI otherwise lack the `PATH` entries for `aws`, `gke-gcloud-auth-plugin`, and `kubelogin`.
- **Browser-based plugins** (e.g. `kubelogin` opening a browser for OIDC): the agent shows "Waiting for sign-in for `prod-eu` in your browser" on that cluster only; other clusters keep working. The first authentication per identical plugin command is **serialized**, so many contexts sharing one IdP client (for example every Warpgate target plus every cluster that trusts the same Entra ID app) open one browser sign-in, not one per context. The returned `ExecCredential` is cached until `expirationTimestamp`.
- **Plugins that prompt on stdin** (MFA codes typed into a terminal) are out of scope for v1; v8s detects them and suggests running the plugin once in a terminal to warm its cache.
- **Secrets at rest**: anything v8s itself stores (imported tokens) goes in the OS keychain (macOS Keychain, Windows Credential Manager, libsecret), never in plain settings files.
- **Proxy**: honours `proxy-url` in kubeconfig and `HTTPS_PROXY`/`NO_PROXY`; supports SOCKS5 for bastion setups.

## Access gateways (Warpgate)

To v8s a gateway is **transparent**: it is a Kubernetes endpoint with OIDC authentication, configured like any other cluster. v8s has no gateway-specific code path. A few gateway behaviours still shape the design:

- **One endpoint per target**, where a target is a cluster × role (for example an everyday `operator` role and a `cluster-admin` role). Several contexts therefore reach one cluster; v8s models them as **access profiles** of one cluster (below).
- **Just-in-time elevation lives in the gateway.** Warpgate's elevated targets are time-boxed sessions granted by a ticket in Warpgate. v8s implements no JIT, approval, or time-boxing of its own. It uses the elevated target like any other profile, and when Warpgate rejects a request because no ticket is active, v8s says so and links to Warpgate.
- **Fixed upstream identity per target.** Warpgate connects upstream with its own short-lived credential for the target's role. `SelfSubjectReview` through the target returns that identity, which is exactly what the API server authorizes, so RBAC-aware UI works per profile. The person is attributed in Warpgate's records, not in the Kubernetes audit log; that is Warpgate's design.
- **Per-request authorization and recording.** Warpgate checks the grant on every request and records sessions, including request content. v8s therefore keeps its traffic lean: protobuf for built-in kinds, metadata-only and Table-mode watches where they suffice, no polling, and no connections while no v8s client is open ([performance.md §10](performance.md#10-hosted-mode-fan-out-and-sharing)). Long-lived watches are re-established at least every 30 minutes (`timeoutSeconds`), so each is re-authorized at least that often even if a gateway does not cut revoked streams.
- **Verify in P0** through Warpgate's (experimental) Kubernetes target support: watch and WatchList, the protobuf content type, `as=Table` requests, exec/attach/port-forward upgrades (WebSocket `v5.channel.k8s.io` and the SPDY fallback), and recording volume for long watch streams. Any gap becomes a recorded fallback (for example JSON instead of protobuf).

## Access profiles: one cluster, several contexts

- A **cluster** in v8s can have several **access profiles**, one per kubeconfig context or gateway target that reaches it (typically `operator` and `cluster-admin`).
- **Grouping is explicit and pinned.** v8s never merges contexts on its own, because a wrong merge would send an elevated action to the wrong cluster, and the `kube-system` namespace UID is neither unique (clusters restored from one etcd snapshot share it) nor trustworthy (any endpoint can report any UID). v8s **suggests** a grouping when two contexts the user has already connected report the same UID, or when their server URLs differ only in a target name; the user confirms once, seeing both endpoints, and the grouping is pinned. Contexts the user has not selected are never probed.
- **The default profile is the one the user picks**, never auto-promoted when a profile is added. Watches, views, and problems always run on the default profile.
- **Elevated profiles are used per action** ("Run as `cluster-admin`" in the action menu and confirm dialog) or by switching the cluster's active profile while a gateway session is active, shown by a warning-tone profile tag wherever the cluster appears. The confirm dialog shows the elevated endpoint URL.
- **Elevated writes carry the uid and resourceVersion the user saw** (`preconditions.uid` for deletes, `metadata.uid` and `resourceVersion` for updates and patches). This is defence in depth only: objects untouched since a shared etcd snapshot have identical uids and resourceVersions in both clusters, and creates (including node-shell pods) carry no precondition. **The real control is the user-confirmed grouping**, plus the elevated endpoint and target object shown in every elevated confirm.
- **Elevated permissions are evaluated lazily**: v8s does not probe an elevated profile until the user uses it, and shows elevated-only actions as "may require an active gateway session".
- Grouped profiles share the cluster's problem IDs, acknowledgements, and bookmarks; ungrouped contexts are separate clusters.

## Hosted

### Login to v8s

Users sign in to v8s with **OIDC** against the organization's IdP (Entra ID, Keycloak, Okta, Google, Dex, GitLab). No local passwords. In the expected deployment the IdP is Entra ID and the same app registration is trusted by the clusters and by Warpgate.

The browser holds only a host-only `__Host-` session cookie (HttpOnly, Secure, SameSite=strict). IdP tokens stay **in server memory only**, never written to disk or the database, **only for the lifetime of the session**: they are deleted at logout and at session expiry, and sessions have an absolute lifetime (default 12 h). Nothing is held long-term.

### Forwarding identity to clusters

Each cluster record has one **identity strategy**. In order of preference:

#### 1. OIDC pass-through (default)

The endpoint (API server or gateway) trusts the same IdP; v8s sends the user's own IdP-issued token as the bearer token. This covers Warpgate targets and directly reached clusters configured with `--oidc-*` flags or **Structured Authentication Configuration** (`AuthenticationConfiguration`, GA since 1.34), EKS with an associated OIDC provider, and AKS with Entra integration.

- **Which token.** `kubelogin` sends an **ID token** whose `aud` is the client ID the endpoint trusts. A server can only obtain an ID token for a client it signs in as, so v8s registers its hosted redirect URI on that **cluster app registration** and runs a second authorization-code flow with it right after the v8s login (silent through the IdP's SSO session). v8s's own session login stays separate from this cluster token. Redeeming codes for a web redirect URI requires a confidential client, so v8s holds a **certificate credential on the cluster app registration** (KMS-protected). That certificate cannot mint tokens without a user's sign-in, but it lets its holder complete sign-ins for the whole trust domain, so it is part of the risk profile below.
- **Shared audience is the normal case with ID tokens.** Every cluster and gateway that trusts the same client ID accepts the same token, so they form one trust domain: an operator of one endpoint who captures a token can replay it against the others until it expires (about an hour with Entra). That is already true of the `kubelogin` setup this mirrors; v8s states it in the cluster settings.
- **Per-cluster audiences** (optional hardening): endpoints that accept **access tokens** for their own API audience can be given one each, via on-behalf-of (Entra) or RFC 8693 token exchange; a token captured by one cluster is then useless against another.
- **Refresh**: the server refreshes tokens before expiry with the session's refresh token; a failed refresh marks the user's clusters `auth expired` and prompts re-login.
- **Risk profile**: v8s holds no cluster credential, only the app-registration certificate above. A server compromise yields the live sessions' IdP tokens, and with them whatever those users can reach, **including elevated gateway targets for anyone with an active gateway session**. Mitigations: session-lifetime-only storage, KMS-held encryption keys, IdP refresh-token rotation, short absolute session lifetime, and an optional **step-up re-authentication** (`max_age`/`prompt=login`) before any elevated-profile action in hosted mode.

#### 2. Cloud-native token exchange (later)

For managed clusters whose API server trusts cloud IAM, not the IdP: EKS via STS `AssumeRoleWithWebIdentity` with the user as role session name (coarser: RBAC sees the mapped role), AKS via Entra on-behalf-of for the AKS server application audience (per user), GKE via Workforce Identity Federation (per user). P5.

#### 3. Impersonation

For directly reached clusters that cannot trust the IdP, v8s authenticates as a dedicated identity (a ServiceAccount with a bound, short-lived token, or a client certificate) holding `impersonate`, and sets `Impersonate-User`/`Impersonate-Group` from the mapped identity.

- **The honest risk**: user names are unbounded, so an impersonator that can impersonate "any user" can impersonate whoever holds cluster-admin. **Classic impersonation is admin-equivalent if the credential leaks.** v8s mitigates, and says so in the cluster setup screen:
  - `impersonate` on `groups` must be limited by `resourceNames` to an explicit allowlist; v8s refuses to save a cluster whose impersonator can impersonate `system:masters`, any `system:` group, or `serviceaccounts`, checked with `SelfSubjectAccessReview` probes at save time and on every connect.
  - v8s only sends usernames produced by its own claim mapping, with a mandatory prefix (default `v8s:`), and hard-denies any mapped name or group starting with `system:`.
  - Where **constrained impersonation** is available (KEP-5284: beta 1.36, GA targeted 1.38), v8s uses it to limit what may be impersonated.
  - The IdP `sub` and a correlation id travel as user extras (`Impersonate-Extra-v8s.io%2Fcorrelation`; client-go percent-encodes the key).
- This is the only strategy (with the kiosk) where v8s holds a cluster credential, and the only one that permits the optional shared cache ([performance.md §10](performance.md#10-hosted-mode-fan-out-and-sharing)).

#### 4. Shared credential (read-only kiosk)

A single read-only ServiceAccount for everyone, for wallboards. v8s marks such clusters `shared identity` everywhere and disables every mutation, shell, and port-forward regardless of what the credential allows.

### Reachability

Hosted v8s is **self-hosted only** (no SaaS is planned). It reaches endpoints over whatever network the install has: the gateway, or private networking for directly reached clusters.

### Per-request flow (hosted)

```
browser ──(session cookie, WS)──▶ v8s server
    session → user (sub, username, groups, IdP tokens for this session only)
    cluster → strategy
        oidc-passthrough:  Authorization: Bearer <user's ID token for the cluster app>
                           ──▶ API server, or a gateway (Warpgate) that authorizes and records the person
                               and forwards as the target's role identity
        impersonation:     Authorization: Bearer <impersonator token>
                           Impersonate-User / Impersonate-Group: <mapped identity>
                           Impersonate-Extra-v8s.io%2Fcorrelation: <correlation id>
        kiosk:             Authorization: Bearer <read-only token>; mutations disabled
```

With OIDC pass-through, **everything is per user**: lists, watches, problem inputs, and writes all carry the user's token ([performance.md §10](performance.md#10-hosted-mode-fan-out-and-sharing) has the cost model).

## Correlation

v8s sends `User-Agent: v8s/<version> (corr <id>)`, where the correlation id is an HMAC of the session id (never the session id itself, which would be a credential in Warpgate's records). v8s keeps no long-term log of its own: the authoritative records are the gateway's and the Kubernetes audit log ([operations.md §Recent activity](operations.md#recent-activity)).

## Desktop vs hosted summary

| | Desktop | Hosted |
| --- | --- | --- |
| Who authenticates to clusters | the user's kubeconfig credential | the user's own IdP token (OIDC pass-through, default); impersonation or kiosk only where an admin configures it |
| Where credentials live | user's machine (plugins' own caches; OS keychain for v8s-held secrets) | v8s server memory and encrypted storage, for the session's lifetime only; never in the browser |
| Exec plugins | yes, natively | no (no user machine); replaced by server-side OIDC flows |
| Cluster list | user's kubeconfig contexts | admin-configured cluster records, filtered to clusters the user can reach |
| Elevation | gateway-elevated profiles per action | same, plus optional step-up re-auth |

## RBAC awareness

The agent resolves the identity **as the endpoint sees it** with `SelfSubjectReview` (behind Warpgate, the target's role identity), then asks what that identity can do (`SelfSubjectRulesReview` per namespace on first visit, `SelfSubjectAccessReview` for specific actions), cached 60 s per user per profile. The UI disables what the user cannot do and says why.

**Hosted cluster visibility** comes from configuration, not probing: admins map IdP groups to cluster records, and a user's selector lists the clusters mapped to their groups (plus admin pins). v8s never connects to a cluster the user has not selected, so access is checked only when a cluster is selected: a `SelfSubjectAccessReview` through its default profile, and a rejection by the gateway or API server shows "no access" on that cluster.

On desktop, a namespace-scoped user's namespaces cannot be discovered from the API (rules reviews take a namespace as input). v8s uses the kubeconfig context's namespace plus a per-cluster namespace list in settings, and offers to add namespaces as the user navigates to them.
