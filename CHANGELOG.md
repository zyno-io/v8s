# Changelog

All notable changes are recorded here. The release workflow uses the section matching the tag (for example `## 0.1.0`) as the
release notes when the tag has no annotation.

## Unreleased

- `v8s server`: self-hosted multi-user mode. OIDC login, server-side sessions (`__Host-` cookie), the user's own cluster ID token forwarded to clusters (impersonation and read-only kiosk as admin-configured alternatives), per-user engines that stop 30 s after the last client closes, YAML config, and a Helm chart (`charts/v8s`). The container image now defaults to `server`.
- Embedded UI: the agent binary serves the UI it was built with.
- `v8s serve` runs the agent on loopback, serves the UI, and opens the browser; `v8s version` prints the version.
- Static CLI builds for Linux, macOS, and Windows, a container image, and desktop installers (dmg, nsis, AppImage, deb, rpm).
- Docs site (VitePress) published to GitHub Pages.
