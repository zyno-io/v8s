# CLI reference

`v8s` is one static binary with five commands. The desktop app, the container image, and the Helm chart all run this same binary.

```text
usage: v8s <command> [flags]

commands:
  agent      run the agent (used by the desktop app and for development)
  serve      run the agent on loopback, serve the embedded UI, and open the browser
  server     run the self-hosted multi-user server (OIDC login, per-user engines): --config /etc/v8s/config.yaml
  version    print the version
```

Run with no command to print this usage to standard error and exit with status 2. An unknown command does the same. Flags use Go's `flag` syntax: `-name value`, `--name value`, and `--name=value` all work. Passing `-h` or `--help` to a subcommand prints that command's flags to standard error and exits with status 1.

## `v8s serve`

Runs the agent on loopback, serves the embedded UI, prints a URL, and opens your browser. This is the quick way to use v8s without the desktop app. See [Getting started](/guide/getting-started#run-it-in-a-browser-instead-v8s-serve).

| Flag                | Meaning                                                               | Default                                       |
| ------------------- | --------------------------------------------------------------------- | --------------------------------------------- |
| `--port`            | Port to listen on. Loopback only (`127.0.0.1`). `0` picks a free port | `7931`                                        |
| `--no-browser`      | Print the URL without opening a browser                               | off                                           |
| `--kubeconfig`      | Kubeconfig path                                                       | client-go loading rules, `$KUBECONFIG` merged |
| `--no-update-check` | Do not check GitHub for a newer release (also `V8S_UPDATE_CHECK=off`) | off                                           |

`serve` fails if the binary was built without the UI (a release archive always has it). It prints `v8s is running. Open:` and a URL of the form `http://localhost:<port>/#agent=ws://localhost:<port>/ws&token=<token>`. The token is random per run and lives in the URL fragment.

## `v8s agent`

Runs the bare agent. The desktop app starts it as a child process, and it is also used for [demo mode](/guide/getting-started#try-it-without-a-cluster-demo-mode) and development. Unless `--listen` says otherwise, it picks a free loopback port.

| Flag                | Meaning                                                                                                   | Default                                                          |
| ------------------- | --------------------------------------------------------------------------------------------------------- | ---------------------------------------------------------------- |
| `--listen`          | Listen address                                                                                            | `127.0.0.1:0`                                                    |
| `--kubeconfig`      | Kubeconfig path                                                                                           | client-go loading rules, `$KUBECONFIG` merged                    |
| `--fake`            | Serve synthetic clusters instead of real ones, for example `clusters=3,pods=50000,churn=200`. See below   | off                                                              |
| `--static-dir`      | Serve UI files from this directory at `/`                                                                 | the embedded UI, when the build has one                          |
| `--dev-token`       | Fixed token. Only allowed together with `--fake`                                                          | random                                                           |
| `--settings`        | Settings file                                                                                             | `<user config dir>/v8s/settings.json`; memory only with `--fake` |
| `--stdin-lifeline`  | Exit when standard input reaches end of file. The desktop app uses it so the agent never outlives the app | off                                                              |
| `--allowed-origin`  | Allowed WebSocket `Origin`. Repeatable                                                                    | none                                                             |
| `--no-update-check` | Do not check GitHub for a newer release (also `V8S_UPDATE_CHECK=off`). Never runs with `--fake`           | off                                                              |

On start the agent prints one JSON line to standard output, `{"port":<n>,"token":"<token>"}`, which the desktop app reads. Logs go to standard error as text.

### `--fake` options

`--fake` takes a comma-separated list of `key=value` pairs, all with whole-number values. A bare `--fake` (or `--fake=true`) uses the defaults. Unknown keys and negative numbers are errors, and `clusters` must be at least 1.

| Key        | Meaning                                                                        | Default |
| ---------- | ------------------------------------------------------------------------------ | ------- |
| `clusters` | Number of fake clusters                                                        | 3       |
| `pods`     | Total pods across the clusters                                                 | 50000   |
| `churn`    | Pod events per second across the clusters                                      | 200     |
| `lograte`  | Synthetic log lines per second per pod                                         | 20      |
| `rbac`     | `1` makes the second cluster read-only and adds a `<name>-admin` write context | `0`     |

## `v8s server`

Runs the self-hosted multi-user server. See [Hosted mode](/guide/hosted) for the configuration file.

| Flag       | Meaning                 | Default                |
| ---------- | ----------------------- | ---------------------- |
| `--config` | YAML configuration file | `/etc/v8s/config.yaml` |

The server logs JSON lines to standard error, serves the embedded UI when the build has one, and shuts down gracefully on `SIGINT` or `SIGTERM`. A configuration error, such as an unknown key or an unreadable secret, stops it at startup with a message.

## `v8s version`

Prints `v8s <version>`. `--version` and `-v` do the same.

## `v8s help`

Prints the usage text to standard output and exits with status 0. `--help` and `-h` do the same.

## Environment

| Variable                       | Effect                                                                                                                                    |
| ------------------------------ | ----------------------------------------------------------------------------------------------------------------------------------------- |
| `KUBECONFIG`                   | Kubeconfig paths, merged, when `--kubeconfig` is not given (`agent`, `serve`)                                                             |
| `V8S_UPDATE_CHECK`             | `0`, `false` or `off` turns the daily update check off (`agent`, `serve`, `server`, and the desktop app, which passes its environment on) |
| `KUBE_FEATURE_WatchListClient` | Streaming lists in client-go. v8s sets it to `true` unless you set it, so initial lists stream from the API server cache                  |

## Exit status

| Status | Meaning                                                 |
| ------ | ------------------------------------------------------- |
| 0      | Success, `version`, `help`, or a clean shutdown         |
| 1      | An error, printed as `v8s: <message>` on standard error |
| 2      | Usage error: no command, or an unknown command          |
