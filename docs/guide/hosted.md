# Hosted mode

Hosted mode is the planned self-hosted, multi-user deployment of v8s: one binary and one Deployment that serve the web app to desktop and phone browsers.

::: warning Not available yet
Hosted mode is on the roadmap and has not shipped. The container image today runs the single-user agent, which is not safe to expose to other people. See [Install](/guide/install#container).
:::

## What it will provide

- **Sign-in with your identity provider** (OIDC). Each person uses their own credential to reach clusters; v8s holds no cluster credential of its own unless an admin configures one for a specific cluster.
- **Cluster visibility from configuration.** Admins map identity-provider groups to clusters, and each person sees the clusters mapped to their groups.
- **Per-user state.** Stars, bookmarks, layouts, and acknowledgements follow you across devices.
- **Phone-friendly access** to Problems, Flux, and Helm from a browser, with no app install.
- **Helm chart and container** that work with Flux-based installs.

## Self-hosted only

There is no hosted service run by the project and no account or billing system. You run it in your own infrastructure.

## Learn more

- [Packaging](/packaging#hosted-v8s-server) describes the server, its state store, and scaling.
- [Authentication](/auth) describes the credential model and hosted security.
