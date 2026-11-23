# Troubleshooting

Start with the cluster's state in the [cluster selector](/guide/clusters#connection-states). It usually says what is wrong. Anything you can do with `kubectl --context <name>` in a terminal should work in v8s; if it does not work there either, fix that first.

## Exec plugin not found

**Symptom.** A cluster shows an error such as `exec: "aws": executable file not found in $PATH`, `gke-gcloud-auth-plugin: not found`, or `kubelogin: not found`. It works in your terminal but not in the desktop app.

**Cause.** An app launched from the macOS Dock or Finder does not inherit your shell's `PATH`. v8s captures your environment once from your login shell (`$SHELL -ilc 'env'`, with a 5 second timeout) and runs plugins with it. If a plugin is still not found, the capture did not include it.

**Fix.**

1. Confirm the plugin is on your `PATH` in a **new** terminal window: `command -v aws gke-gcloud-auth-plugin kubelogin`.
2. Make sure its `PATH` entry is set in a file your login shell reads (`~/.zprofile` or `~/.zshrc` for zsh, `~/.bash_profile` for bash), not only in a file that is read by non-login shells. Homebrew's `eval "$(/opt/homebrew/bin/brew shellenv)"` belongs in `~/.zprofile`.
3. If your shell startup is slow or prompts for input, the 5 second capture can time out. Trim the startup files, or start v8s from a terminal (`open -a v8s`) so it inherits that terminal's environment.
4. Use an absolute path to the plugin in your kubeconfig's `exec.command` as a last resort.

On Linux and Windows, the app uses the environment it was started with.

## Sign-in required

**Symptom.** A cluster shows **sign-in required** (the `auth-required` state).

**Cause.** The credential is missing or has expired: an OIDC token, an expired SSO session, an exec plugin that needs you to sign in again.

**Fix.**

- If the plugin opens a browser, v8s shows "waiting for sign-in" on that cluster. Finish it in the browser.
- Otherwise, run the plugin once in a terminal to refresh its cache, for example `aws sso login`, `gcloud auth login`, or `kubectl --context <name> get ns`. Then open the selector and press **Connect** again.
- Plugins that prompt for an MFA code in the terminal cannot be answered from v8s. Run them once in a terminal, then retry.
- Many contexts can share one IdP client. v8s serializes the first sign-in so you see one browser tab, not one per cluster. If you see several, the contexts use different plugin commands or arguments.

In [hosted mode](/guide/hosted#sign-in-and-sign-out), a lapsed cluster token shows a banner, "Sign in to the clusters to continue", with a **Sign in to clusters** button. Press it, and the clusters recover. If your v8s session itself ended, you are sent to sign in again.

## Hosted sign-in fails with an Entra error

**Symptom.** Signing in to [hosted mode](/guide/hosted) stops on a Microsoft error page, or v8s reports a failed sign-in with an `AADSTS` code.

**Cause.** Usually the redirect URI is missing, or it is registered under a platform that does not match how v8s authenticates. See [Which Entra platform to register](/guide/hosted#which-entra-platform-to-register).

**Fix.**

| Code            | Meaning                                                                                                       | Fix                                                                                                                      |
| --------------- | ------------------------------------------------------------------------------------------------------------- | ------------------------------------------------------------------------------------------------------------------------ |
| `AADSTS50011`   | The redirect URI is not registered on the app                                                                 | Register exactly `<publicUrl>/auth/callback`, including the port if `publicUrl` has one                                  |
| `AADSTS7000218` | The URI is under **Web**, which needs a credential, but v8s has `publicClient: true`                          | Move the URI to **Mobile and desktop applications**, or give v8s a `clientSecret` or certificate and drop `publicClient` |
| `AADSTS700025`  | The URI is under **Mobile and desktop applications** (a public client), but v8s sends a secret or certificate | Set `publicClient: true` and remove the credential, or move the URI to **Web**                                           |
| `AADSTS9002327` | The URI is under **Single-page application**                                                                  | Move it to **Mobile and desktop applications** (or **Web** with a credential). v8s cannot use SPA redirects              |

## Write context rejected

**Symptom.** An action fails with a "write context rejected" message, and the cluster has a [linked write context](/guide/clusters#linked-read-and-write-contexts).

**Cause.** The linked write context refused the request. With a gateway such as Warpgate, that nearly always means **your elevated session is not active**: it expired, or was never started.

**Fix.** Start or renew the elevated session in the gateway, then retry. Reads keep working in the meantime. If the message is something else (an admission denial, a quota error), it is the cluster's answer, not an access problem, and the message says what.

While a cluster is **elevated**, the same cause shows on the cluster's chip as `sign-in required as <write context> (is its elevated session active?)`: renew the session in the gateway, or use the arrow on the chip to **Revert** to the read context.

Related messages:

- **"This will run as the write context..."** is a prompt, not an error. It appears when you turned off the automatic switch. Confirm to proceed.
- **A link mismatch** means the two linked contexts do not point at the same cluster. Nothing was written. Unlink and relink the correct pair.

## An action is disabled

Hover the button: it says what permission is missing ("You can't `delete` pods in `payments`"). If the cluster has no linked write context and your read credential cannot do it, ask for the permission or [link a write context](/guide/clusters#link-two-contexts). The check is cached for about a minute, so a just-granted permission may take that long to show up.

## A cluster is degraded or some checks are missing

**Degraded** means connected, but some kinds are forbidden or failing. The state's message says which ("no permission to list `ingresses`"). Problems checks that depend on those kinds are skipped, and the Problems view says so rather than reporting zero problems. Grant `list` and `watch` on the kind to restore them.

If you can only access some namespaces, v8s watches the core kinds in a few namespaces (five by default) instead of the whole cluster, and tells you which. Add the namespaces you work in as you navigate to them.

## Slow clusters and API priority and fairness

**Symptom.** A cluster takes a long time to sync, or shows `429` or "too many requests" messages.

v8s limits itself to 20 reads per second (burst 40) and at most 96 watches per cluster and identity, uses streaming lists where available, and honors `429` with `Retry-After`. If a cluster is still slow:

- Watch the sync progress in the toolbar (`syncing prod-eu · 4,210`). Large clusters take seconds, not milliseconds, to sync the first time; rows appear as they load.
- A chip that stays at `loading secrets…` (or another type) means the API server is slow to list that type, often Helm release Secrets. The agent log names the cause: `slow discovery`, `discovery failed` (the type is not placed until discovery works), `first list failing` (the list returns an error other than a refusal and is retried; the line carries the error) or `slow first list`.
- Check the control plane. On very large or busy clusters the API server may be throttling. API Priority and Fairness matches users and groups, not user agents, so v8s traffic is shaped like your own `kubectl` traffic. To isolate it, give your team's identities their own FlowSchema.
- Behind a gateway, every request is authorized and recorded, which adds latency. Long-lived watches are re-established about every 30 minutes.
- Fewer clusters at once means less work. Deselect the ones you are not using.

## "watch-list stream truncated" in the agent log

**Symptom.** The agent logs `watch-list stream truncated; using LIST+watch` with a cluster and a kind.

**Cause.** A gateway or proxy between v8s and the API server ended a streaming list early (before the API server's end-of-list marker). One known cause is a proxy that decodes only the first part of a gzip response. v8s stops using streaming lists for that kind and loads it with a paged LIST plus a watch instead, so data stays correct. Watches that a proxy keeps closing early without delivering any events are retried with a growing delay (up to a minute) instead of immediately.

**Fix.** Nothing is needed in v8s. Report it to whoever runs the gateway: other Kubernetes clients behind it (`kubectl`, controllers) may retry those requests in a tight loop.

## "Reconnecting…" banner

**Symptom.** A grey **Reconnecting…** bar appears under the top bar, and the tables stop updating. After a minute it turns into a red **Can't reach the agent** banner with **Retry now** and **Reload** buttons.

**Cause.** The connection between the page and the agent dropped: the server restarted or was redeployed, a proxy (for example `tailscale serve` or a load balancer) closed an idle connection, the laptop slept, or the network changed. v8s reconnects on its own, trying again after 0.5 s, then 1 s, 2 s and so on up to every 15 s, and immediately when the network comes back or the tab becomes visible. Short blips are not shown at all.

**What to expect.** Tables keep their last rows while it reconnects, then refresh in place; you do not need to reload. Requests made meanwhile (an apply, a scale) fail with "unavailable" and are not replayed, so repeat them once the bar is gone. Open log and flow panes that lost their stream offer **Reconnect**. A terminal session (exec, node shell) ends and must be reopened.

**Fix.** Usually none. If **Can't reach the agent** stays, press **Retry now**, then check that the agent or `v8s server` is running and reachable at the same address. In hosted mode, if your session ended, v8s sends you to sign in again instead of retrying.

## A cluster is unreachable

Check VPN and network access to the API server address, that `proxy-url` or `HTTPS_PROXY` is right if you use a proxy, and the server certificate and CA in your kubeconfig. v8s retries with backoff and keeps the last-known rows, greyed out, until it reconnects.

## Desktop first run

- **macOS.** The app and its bundled agent are signed and notarized, so it should open normally. If your organization blocks unknown apps, allow `io.zyno.v8s`. If you see a firewall prompt, it is for the agent's loopback listener; the agent only listens on `127.0.0.1`.
- **Windows.** The installer and app are signed. SmartScreen may still warn for a very new release until it has built reputation. Choose **More info**, then **Run anyway**.
- **Linux.** An AppImage needs execute permission (`chmod +x`). If the window is blank on some GPU and driver combinations, try launching with `--disable-gpu`. v8s uses Chromium through Electron, which is the same on every OS.
- **A blank window or "no agent".** The app starts its agent as a child process. If it cannot, quit and relaunch. If it persists, run `v8s serve` from a terminal to see the agent's error output.
- **Antivirus.** Some security software blocks the bundled agent from opening a local port. Allow the app.
- **Quitting.** The agent stops when you quit the app.

## `v8s serve` shows "no embedded UI"

You built the CLI without the UI. Build with `scripts/build-agent.sh --host`, or use a release archive, which has the UI embedded. See [Install](/guide/install#build-from-source).

## Logs and diagnostics

The agent logs to standard error. Run `v8s serve` (or `v8s agent`) in a terminal and watch the output while you reproduce a problem. The desktop app's agent logs are visible when you start the app from a terminal.

## How to report a bug

Open an issue on [GitHub](https://github.com/zyno-io/v8s/issues) with:

- v8s version (`v8s version`) and the OS.
- How you run it: desktop, `v8s serve`, container, or hosted.
- How you reach the cluster: direct, exec plugin (which one), or a gateway such as Warpgate. Kubernetes version if you know it.
- What you did, what you expected, and what happened. A screenshot helps. Remove cluster names and object names you do not want to share.
- The agent's output from the terminal, if relevant.

Never paste tokens, kubeconfig files, or Secret values. v8s itself never logs them.
