# Hosted mode

Hosted mode is `v8s server`: one self-hosted, multi-user deployment that serves the v8s web app to desktop and phone browsers. People sign in with your identity provider, and v8s reaches each cluster as that person, not as a shared admin.

::: warning Self-hosted only
There is no v8s cloud service. You run `v8s server` in your own infrastructure, where it can reach your clusters or your access gateway.
:::

## When to use it

Use hosted mode when:

- A team wants one URL for Problems, Flux, and Helm, including from a phone, with no install.
- Clusters are reachable from a place you can run a server (a management cluster, a VM), but not from every laptop.
- You already have an identity provider (Entra ID, Keycloak, Okta, Google, Dex, GitLab) that your clusters or your gateway trust.

Use the [desktop app](/guide/install#desktop-app) or `v8s serve` for personal use. They use your own kubeconfig and need no server. Exec plugins (`kubelogin`, `aws`) only work there: a server has no user machine, so hosted mode does the equivalent OIDC flow on the server.

## How it works

![Browsers sign in to the v8s server with a session cookie. The server signs users in with the identity provider and forwards each user's own token to the Kubernetes API server directly, or through Warpgate.](/hosted-architecture.svg)

1. A person opens the v8s URL and signs in with OIDC.
2. v8s sets a host-only session cookie. The IdP tokens stay on the server, in memory.
3. The person picks clusters. v8s starts **that person's own watches** on those clusters, carrying their own token, to the API server or to Warpgate.
4. v8s sends the UI only the rows it is showing, like the desktop app.

Nothing is watched on behalf of someone who is not looking. A user's watches start when they select a cluster and stop 30 seconds after their last browser tab closes or deselects it. There is no background alerting, so [Problems](/guide/problems) are computed only while someone has v8s open.

## Identity strategies

Each cluster has one strategy for how v8s authenticates as the user.

| Strategy           | When to use                                                                  | What v8s holds                          |
| ------------------ | ---------------------------------------------------------------------------- | --------------------------------------- |
| `oidc-passthrough` (default) | The API server or gateway trusts the same IdP. Covers Warpgate targets, clusters with `AuthenticationConfiguration` or `--oidc-*` flags, EKS with an OIDC provider, AKS with Entra | Nothing per cluster. Only the cluster app credential (below) |
| `impersonation`    | A directly reached cluster cannot trust your IdP                             | A scoped ServiceAccount token with `impersonate` |
| `kiosk`            | A read-only wallboard for everyone                                           | A read-only token. All mutations, shells, and port-forwards are disabled |

Prefer `oidc-passthrough`. With it, **everything is per user**: lists, watches, problems, and writes all carry the user's own token, so Kubernetes RBAC and your gateway's records are accurate.

::: danger Impersonation is admin-equivalent if the credential leaks
Limit `impersonate` on `groups` with `resourceNames` to an explicit allowlist, and never allow `system:masters`, any `system:` group, or `serviceaccounts`. v8s refuses `system:` names and prefixes every mapped user and group (default `v8s:`). Use it only where you must.
:::

## Configuration file

`v8s server --config /etc/v8s/config.yaml` reads one YAML file. Unknown keys are errors, so typos fail at startup. Secrets are never inline: each secret is a reference to an environment variable (`env:`) or a file (`file:`), and files are re-read, so rotated tokens work.

::: tip Check your version
The keys below are from the server's configuration schema. `v8s server --help` is the authority for your build.
:::

### Top level

| Key              | Meaning                                                                                  | Default            |
| ---------------- | ---------------------------------------------------------------------------------------- | ------------------ |
| `listen`         | Address to listen on                                                                     | `:7931`            |
| `publicUrl`      | The external origin, for example `https://v8s.example.com`. https required (http only for localhost). No path | required |
| `dataDir`        | Where per-user settings (stars, bookmarks, groups, acknowledgements) are stored as files | `/var/lib/v8s`     |
| `session.lifetime` | Absolute session length                                                                | `12h`              |
| `session.idleTimeout` | Idle timeout                                                                        | `30m`              |
| `session.maxPerUser` | Concurrent sessions per user                                                         | `5`                |
| `oidc`           | The login provider: v8s's own sign-in                                                    | required           |
| `clusterApp`     | The app registration whose ID token clusters receive. Required if any cluster uses `oidc-passthrough` | |
| `clusters`       | The cluster records                                                                      |                    |
| `adminGroups`    | IdP groups that can see every cluster                                                    |                    |
| `limits`         | `readQps` (20), `readBurst` (40), `clusterQps` (100), `clusterBurst` (200)               | as listed          |
| `correlationKey` | Secret that keys the correlation id sent to clusters. Random per start by default        |                    |

Register `https://<publicUrl>/auth/callback` on the login app and `https://<publicUrl>/auth/cluster-callback` on the cluster app as redirect URIs.

### Example: Entra ID, clusters and Warpgate trusting one app

This is the common setup. You have two Entra app registrations:

- **v8s login**: used only to sign people in to v8s.
- **cluster app**: the client ID your clusters and Warpgate already trust, the same one `kubelogin` uses. v8s signs people in to it a second time, silently through their existing Entra session, to get the ID token it forwards. Because Entra requires a confidential client for a web redirect, v8s authenticates to this app with a certificate.

```yaml
listen: ':7931'
publicUrl: https://v8s.example.com
dataDir: /var/lib/v8s

session:
  lifetime: 12h
  idleTimeout: 30m

oidc:
  issuer: https://login.microsoftonline.com/<tenant-id>/v2.0
  clientId: <v8s-login-client-id>
  clientSecret:
    env: V8S_OIDC_CLIENT_SECRET
  usernameClaim: preferred_username
  groupsClaim: groups

clusterApp:
  clientId: <cluster-app-client-id>      # the client ID your clusters and Warpgate trust
  privateKey:
    file: /etc/v8s/cluster-app.key       # PEM, RSA
  certificate:
    file: /etc/v8s/cluster-app.crt       # uploaded to the cluster app registration

adminGroups:
  - <platform-admins-group-object-id>

clusters:
  - id: prod-eu
    name: Production EU
    server: https://prod-eu.example.com
    caFile: /etc/v8s/ca/prod-eu.crt
    groups: [<sre-group-object-id>]
    displayGroups: [production]

  - id: prod-eu-gw              # a Warpgate target: just another OIDC endpoint
    name: Production EU (via Warpgate)
    server: https://warpgate.example.com:8888/prod-eu-operator
    groups: [<sre-group-object-id>, <dev-group-object-id>]
    displayGroups: [production]
```

Entra emits group object IDs in the `groups` claim, so `groups:` lists IDs. A user sees a cluster if they belong to one of its `groups`, or to an `adminGroups` group. `groups: ['*']` means every signed-in user.

On the Kubernetes side, the API server needs to trust the cluster app. With structured authentication configuration (stable in Kubernetes 1.34):

```yaml
apiVersion: apiserver.config.k8s.io/v1
kind: AuthenticationConfiguration
jwt:
  - issuer:
      url: https://login.microsoftonline.com/<tenant-id>/v2.0
      audiences: [<cluster-app-client-id>]
    claimMappings:
      username: { claim: preferred_username, prefix: 'oidc:' }
      groups: { claim: groups, prefix: 'oidc:' }
```

### Shared audience

With ID tokens, every cluster and gateway that trusts the same client ID accepts the same token. They form one trust domain: someone who operates one endpoint and captures a token could replay it against the others until it expires (about an hour with Entra). That is already true of the `kubelogin` setup this mirrors. If it matters to you, give endpoints their own audiences where they can accept access tokens.

### Impersonation and kiosk clusters

```yaml
clusters:
  - id: legacy
    server: https://legacy.example.com:6443
    caFile: /etc/v8s/ca/legacy.crt
    strategy: impersonation
    impersonation:
      token:
        file: /var/run/secrets/v8s/legacy-token   # a ServiceAccount token that can impersonate
      prefix: 'v8s:'              # mandatory username prefix
      groups: [sre, developers]   # forward only these IdP groups
    groups: [<sre-group-object-id>]

  - id: wall
    server: https://wall.example.com
    strategy: kiosk
    kiosk:
      token:
        env: V8S_WALL_TOKEN       # a read-only ServiceAccount token
    groups: ['*']
```

Kiosk clusters are tagged **shared identity** everywhere in the UI, and every mutation, shell, and port-forward is disabled for them, whatever the token allows.

## Install

### With Helm

The chart in `charts/v8s` deploys one `v8s server` Deployment, a Service, and the configuration:

```sh
helm install v8s ./charts/v8s \
  --namespace v8s --create-namespace \
  --values values.yaml
```

The chart's values cover the server configuration, including OIDC, cluster records, and storage. See the chart's `values.yaml` for the exact keys in your version. Keep secrets in Kubernetes Secrets and reference them through the `env` and `file` forms above. The chart works well with Flux: put the HelmRelease in Git and the values in a Secret or ConfigMap.

### Without Helm

The image is distroless and runs as non-root with a read-only root filesystem. Mount the config file, a writable volume for `dataDir`, and your secrets:

```yaml
apiVersion: apps/v1
kind: Deployment
metadata: { name: v8s, namespace: v8s }
spec:
  replicas: 1
  selector: { matchLabels: { app: v8s } }
  template:
    metadata: { labels: { app: v8s } }
    spec:
      containers:
        - name: v8s
          image: ghcr.io/zyno-io/v8s:0.1.0
          args: ['server', '--config', '/etc/v8s/config.yaml']
          ports: [{ containerPort: 7931 }]
          envFrom: [{ secretRef: { name: v8s-secrets } }]
          readinessProbe: { httpGet: { path: /readyz, port: 7931 } }
          livenessProbe: { httpGet: { path: /healthz, port: 7931 } }
          securityContext: { readOnlyRootFilesystem: true, runAsNonRoot: true }
          volumeMounts:
            - { name: config, mountPath: /etc/v8s, readOnly: true }
            - { name: data, mountPath: /var/lib/v8s }
      volumes:
        - { name: config, projected: { sources: [{ configMap: { name: v8s-config } }, { secret: { name: v8s-keys } }] } }
        - { name: data, persistentVolumeClaim: { claimName: v8s-data } }
```

Put it behind a TLS-terminating ingress at your `publicUrl`. v8s runs as a single replica; per-user watches live in that process's memory, with about 50 MB per active user per 10k pods per cluster.

## Security model

- **Browser.** Holds only a host-only `__Host-` session cookie (HttpOnly, Secure, SameSite=Strict). No tokens, no cluster credentials.
- **Tokens.** IdP tokens live in the server's memory, only for the life of the session. They are deleted at sign-out and at expiry, and never written to disk. Sessions have an absolute lifetime (12 hours by default).
- **Storage.** The only thing written to disk is each user's settings: stars, bookmarks, groups, layouts, and acknowledgements. There is no database table for activity.
- **Per-user access.** Each user's watches carry that user's own token. Problems contain only what that user's token can read. A user sees only the clusters mapped to their IdP groups, and v8s connects to a cluster only after they select it.
- **No long-term logs.** v8s keeps a short, in-memory list of a user's own recent actions, cleared at sign-out. The authoritative records are your gateway's and the Kubernetes audit log. v8s sends `User-Agent: v8s/<version> (corr <id>)`, where the id is an HMAC of the session, never the session itself.
- **Request checks.** `Origin` and `Sec-Fetch-Site` are checked on every HTTP and WebSocket endpoint.
- **Shells and port-forward.** Shells run as the user, so Kubernetes RBAC decides who can open them. Streams through a write context have idle and duration caps. Port-forward is not offered in hosted mode, and node shell is hidden on phones.
- **If the server is compromised,** the attacker gets the live sessions' tokens, and with them whatever those users can reach, including write targets for anyone with an active elevated Warpgate session. Mitigate with a short session lifetime and a locked-down network path to the server.

## Warpgate

To v8s, [Warpgate](https://github.com/warp-tech/warpgate) is an ordinary Kubernetes endpoint with OIDC. A Warpgate target is configured as a cluster with the default `oidc-passthrough` strategy. A few behaviours to know:

- **One target per cluster and role.** A read-only `operator` target and a writable `cluster-admin` target are two contexts. [Link them](/guide/clusters#linked-read-and-write-contexts) so writes switch to the admin target.
- **Elevation is Warpgate's.** Time-boxed elevated sessions are granted in Warpgate. If the write target rejects a request, v8s reports "write context rejected" and the read context keeps working. See [Troubleshooting](/guide/troubleshooting#write-context-rejected).
- **Attribution.** Warpgate records the person and the session. The Kubernetes audit log shows Warpgate's target role identity. That is Warpgate's design.
- **Lean traffic.** Warpgate checks every request, so v8s keeps traffic modest: protobuf where possible, no polling, and no connections while nobody has v8s open. Long watches are re-established at least every 30 minutes, so each is re-authorized at least that often.

## Related

- [Authentication](/auth) explains the credential model in depth.
- [Packaging](/packaging#hosted-v8s-server) describes the server's design and scaling.
- [On a phone](/guide/mobile).
