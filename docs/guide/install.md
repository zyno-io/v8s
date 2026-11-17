# Install

v8s ships as a desktop app, a single static CLI binary that serves the UI, and a container image. All of them contain the same Go agent and the same UI.

## Desktop app

Download the installer for your platform from the [releases page](https://github.com/zyno-io/v8s/releases).

| Platform                        | Package                      |
| ------------------------------- | ---------------------------- |
| macOS (Apple silicon and Intel) | `.dmg`, signed and notarized |
| Windows (x64)                   | `.exe` installer, signed     |
| Linux (x64)                     | `.AppImage`, `.deb`, `.rpm`  |

The desktop app starts its own agent as a child process on a loopback port and stops it when you quit.

## CLI

Each release has an archive per platform, named `v8s_<version>_<os>_<arch>.tar.gz` (`.zip` on Windows), plus a `checksums.txt` with SHA-256 sums.

| OS      | Architectures |
| ------- | ------------- |
| Linux   | amd64, arm64  |
| macOS   | amd64, arm64  |
| Windows | amd64         |

```sh
tar xzf v8s_<version>_linux_amd64.tar.gz
./v8s_<version>_linux_amd64/v8s version
./v8s_<version>_linux_amd64/v8s serve
```

Subcommands:

| Command       | What it does                                                                                                                                       |
| ------------- | -------------------------------------------------------------------------------------------------------------------------------------------------- |
| `v8s serve`   | Runs the agent on `127.0.0.1:7931`, serves the embedded UI, and opens your browser. `--port` changes the port, `--no-browser` only prints the URL. |
| `v8s agent`   | Runs the bare agent. This is what the desktop app launches.                                                                                        |
| `v8s version` | Prints the version.                                                                                                                                |

## Container

```sh
docker run --rm -p 7931:7931 -v ~/.kube:/home/nonroot/.kube:ro ghcr.io/zyno-io/v8s:latest
```

The image runs the single-user agent, which exposes whatever credentials the container can reach to anyone who reaches the port. Keep the port on a trusted network until [hosted mode](/guide/hosted) exists.

## From source

You need Go 1.27 and Node 26 with Yarn 4 (through Corepack).

```sh
corepack enable
yarn install
scripts/build-agent.sh --host     # UI + agent for this machine, in dist/
```
