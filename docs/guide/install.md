# Install

v8s ships as a desktop app, a static CLI binary that serves the UI, a container image, and a Helm chart for hosted mode. They all contain the same Go agent and the same UI. Every release is on the [releases page](https://github.com/zyno-io/v8s/releases).

## Desktop app

| Platform                        | Package                     | Notes                                               |
| ------------------------------- | --------------------------- | --------------------------------------------------- |
| macOS (Apple silicon and Intel) | `.dmg`                      | Signed and notarized                                |
| Windows (x64)                   | `.exe` installer            | Signed. Per-user install, you can change the folder |
| Linux (x64)                     | `.AppImage`, `.deb`, `.rpm` |                                                     |

Installer files are named `v8s-<version>-<os>-<arch>.<ext>`.

::: code-group

```sh [macOS]
# Open the .dmg and drag v8s to Applications.
open v8s-0.1.0-mac-arm64.dmg
```

```sh [Debian / Ubuntu]
sudo apt install ./v8s-0.1.0-linux-x64.deb
```

```sh [Fedora / RHEL]
sudo dnf install ./v8s-0.1.0-linux-x64.rpm
```

```sh [AppImage]
chmod +x v8s-0.1.0-linux-x64.AppImage
./v8s-0.1.0-linux-x64.AppImage
```

:::

The app starts its own agent as a child process on a loopback port and stops it when you quit. The agent is bundled; you do not install anything else. Exec credential plugins (`kubelogin`, `aws`, `gke-gcloud-auth-plugin`) are not bundled: they need to be installed on your machine, as they do for `kubectl`.

## CLI

Each release has one archive per platform, named `v8s_<version>_<os>_<arch>.tar.gz` (`.zip` on Windows).

| OS      | Architectures |
| ------- | ------------- |
| Linux   | amd64, arm64  |
| macOS   | amd64, arm64  |
| Windows | amd64         |

```sh
tar xzf v8s_0.1.0_linux_amd64.tar.gz
./v8s_0.1.0_linux_amd64/v8s version
./v8s_0.1.0_linux_amd64/v8s serve
```

macOS binaries are signed and notarized. The Windows executable is signed. Put the binary somewhere on your `PATH` to run it as `v8s`.

| Command       | What it does                                                                                                                             |
| ------------- | ---------------------------------------------------------------------------------------------------------------------------------------- |
| `v8s serve`   | Runs the agent on loopback, serves the embedded UI, and opens your browser. Flags: `--port`, `--no-browser`, `--kubeconfig`              |
| `v8s server`  | Runs hosted multi-user mode. Flag: `--config`. See [Hosted mode](/guide/hosted)                                                          |
| `v8s agent`   | Runs the bare agent. The desktop app launches this. Also used for [demo mode](/guide/getting-started#try-it-without-a-cluster-demo-mode) |
| `v8s version` | Prints the version                                                                                                                       |

Every flag is listed in the [CLI reference](/guide/cli).

## Verify downloads

Each release includes a `checksums.txt` with a SHA-256 sum for every file. Download it next to your installer and check:

::: code-group

```sh [macOS / Linux]
sha256sum --check --ignore-missing checksums.txt
# macOS without coreutils:
shasum -a 256 --check --ignore-missing checksums.txt
```

```powershell [Windows]
(Get-FileHash .\v8s-0.1.0-win-x64.exe -Algorithm SHA256).Hash
# compare with the matching line in checksums.txt
```

:::

Release files and the container image also carry build provenance attestations from GitHub. If you use the GitHub CLI:

```sh
gh attestation verify v8s_0.1.0_linux_amd64.tar.gz --repo zyno-io/v8s
```

## Container image

```sh
docker run --rm -p 7931:7931 \
  -v $PWD/config.yaml:/etc/v8s/config.yaml:ro \
  ghcr.io/zyno-io/v8s:0.1.0
```

The image is distroless, runs as non-root, and is published for `linux/amd64` and `linux/arm64`. Tags are the exact version (`0.1.0`) and `latest` (stable releases only).

The entrypoint is `v8s` and the default command is `server --config /etc/v8s/config.yaml`, so the image runs [hosted mode](/guide/hosted) and needs that configuration file. Pass other arguments to run another command. For example `docker run ghcr.io/zyno-io/v8s:0.1.0 version` prints the version.

::: warning Single-user mode in a container
`agent` and `serve` run as one user with whatever credentials the container can reach, and have no login. `serve` also binds to loopback inside the container, so a published port cannot reach it. Use `server` for shared deployments, and the [desktop app or a local `v8s serve`](/guide/getting-started) for personal use.
:::

## Helm chart

The Helm chart in `charts/v8s` deploys hosted mode: one Deployment running `v8s server`, a Service, a ConfigMap for the configuration file, an optional Ingress, and a PersistentVolumeClaim for the per-user settings. Secrets (OIDC client secret, private keys, impersonation tokens) are referenced from Kubernetes Secrets, not written into values. The chart is published to `oci://ghcr.io/zyno-io/charts/v8s` with every release, and its default image tag is the release version.

```sh
helm install v8s oci://ghcr.io/zyno-io/charts/v8s --version 0.1.0 \
  --namespace v8s --create-namespace \
  --values my-values.yaml
```

The values mirror the server configuration. [Hosted mode](/guide/hosted#with-helm) lists them and has a full example, and `charts/v8s/values.yaml` documents every key.

## Build from source

You need Go 1.27 and Node 26 with Yarn 4 (through Corepack).

```sh
git clone https://github.com/zyno-io/v8s.git
cd v8s
corepack enable
yarn install
scripts/build-agent.sh --host     # UI + agent for this machine, in dist/
```

`scripts/build-agent.sh --help` lists options, including cross-compiling and the desktop layout. For the desktop app itself:

```sh
scripts/build-agent.sh --no-ui --layout desktop --host
yarn workspace @v8s/desktop build
CSC_IDENTITY_AUTO_DISCOVERY=false yarn workspace @v8s/desktop dist --publish never
```

That produces an unsigned app in `packages/desktop/release/`.

## Updates

The CLI and container do not update themselves. Download the new archive, or pull the new image tag.

v8s checks `https://v8s.io/release.json` every 15 minutes for a newer stable release (a new release shows up within about 20 minutes of being published). When there is one, an **Update** pill appears in the top bar with a link to the release notes, and **Dismiss** hides it until the next release. Hover the logo to see the running version. To turn the check off, pass `--no-update-check` or set `V8S_UPDATE_CHECK=off` (the desktop app passes its environment on; on macOS an app opened from Finder or the Dock does not see your shell profile, so use `launchctl setenv V8S_UPDATE_CHECK off` or start it from a terminal); on a hosted server set `updateCheck: false`. Nothing is downloaded or installed automatically.

The desktop app ships its UI and agent together, so they always match. Install a newer installer over the old one to update; settings, groups, stars, and bookmarks live in your user config directory and are kept.

::: info macOS and Windows first run
Because the builds are signed and notarized, macOS opens them without the "unidentified developer" prompt. If your organization blocks unknown apps, allow `io.zyno.v8s`. Other first-run notes are in [Troubleshooting](/guide/troubleshooting#desktop-first-run).
:::
