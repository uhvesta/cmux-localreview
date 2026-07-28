# GitHub OAuth setup

Queue Home uses dedicated GitHub **OAuth App** registrations. This is not a
GitHub App installation, a personal access token, a `gh` login, or a Copilot
CLI session. The daemon owns every resulting access token in the platform
secret store; the browser receives only a short-lived loopback capability and
never receives a GitHub token.

## Create the right kind of registration

Create an **OAuth App**, not a GitHub App. In GitHub, go to **Settings →
Developer settings → OAuth apps → New OAuth App**. The historic
`localreview github-app` command name is only a compatibility alias; it does
not use GitHub App installations, private keys, installation IDs, or GitHub
App permissions.

For every registration, use a non-sensitive public homepage and set the one
OAuth App callback URL to exactly:

```text
http://127.0.0.1:8787/oauth/callback
```

Use `127.0.0.1` exactly, not `localhost`, an HTTPS URL, a daemon port, or a
remote host. GitHub OAuth Apps allow one callback URL; localreview deliberately
uses this stable loopback callback on each desktop machine. The browser and
daemon must run on the same machine for this flow.

GitHub will show a client secret after registration. **Do not copy it into
localreview or a shell.** localreview is a public PKCE client: it configures
and transmits only the public client ID, and has no client-secret input,
storage, environment-variable, or fallback path. Keep the displayed secret
out of logs and handoffs; it is not part of localreview setup.

If a registration will be used on a headless/SSH host, enable GitHub's
**Device Flow** option. Leave it disabled when it will only use browser
loopback OAuth; GitHub recommends device flow only for constrained clients.

## Capability registrations

For each capability you intend to use, create a dedicated OAuth App
registration in GitHub. Use separate registrations for:

| Capability | Enables | Does not enable |
| --- | --- | --- |
| `read` | resolving and mirroring pull requests for local review | publishing reviews or Copilot `/ask` |
| `write` | an explicit GitHub review/comment publish action | read or Copilot authority by itself |
| `copilot` | fresh Copilot SDK `/ask` conversations | GitHub review publishing |

For a browser login, set the authorization callback URL exactly to:

```text
http://127.0.0.1:8787/oauth/callback
```

Save the OAuth App’s public client ID in Queue Home, select **Browser OAuth**,
and press **Connect**. The daemon creates a loopback-only callback listener
and uses PKCE: there is no client secret field anywhere in the product and no
secret is passed through the CLI. Review GitHub’s consent screen before
authorizing.

For an SSH/headless host, enable Device Flow on that OAuth App registration,
select **Device code**, and complete the displayed code at GitHub. Device flow
is a fallback for constrained hosts; use browser OAuth on a normal local
machine.

The capability split is a local routing and secure-store boundary, not a
GitHub OAuth permission boundary. Register three different OAuth Apps to
preserve that boundary. The CLI technically accepts the same public client ID
more than once, but doing so defeats separate consent/revocation and is not a
supported operator configuration.

## Fast, token-free setup

For a local `/ask` chat, only configure `copilot`:

```sh
# Starts the daemon when needed, saves only the public OAuth client ID, opens
# GitHub consent, and waits for the local loopback callback.
localreview auth login --capability copilot --client-id YOUR_COPILOT_CLIENT_ID

# Shows configuration/connection state and safe recovery text; never a token.
localreview auth status
```

Use `--no-open` to approve in a particular browser profile. The printed URL
contains short-lived OAuth state (never an access token), so keep it out of
chat and issue trackers:

```sh
localreview auth login --capability copilot \
  --client-id YOUR_COPILOT_CLIENT_ID --no-open
```

On a remote/headless machine, use device flow instead. It prints a verification
URL and short-lived user code; complete those in any browser while the command
polls on the remote host:

```sh
localreview auth login --capability copilot \
  --client-id YOUR_COPILOT_CLIENT_ID --device
```

If you used `--no-wait`, return to the same daemon and run `auth status` after
approval. Do not start a second login merely because Queue Home was reopened:
that replaces an in-progress loopback listener for that capability.

## What still requires your GitHub account

cmux-localreview cannot create or administer OAuth App registrations for you.
You must interactively create the registration, choose its requested OAuth
authority, enable Device Flow if needed, and approve or revoke the consent
screen with the intended GitHub account. Organization SSO policies can require
an additional organization authorization after OAuth consent.

The product intentionally does not substitute `gh`, environment tokens, a
PAT, existing Copilot CLI credentials, or a plaintext credential file when a
registration or approval is absent. Disconnect in Queue Home removes the
daemon’s stored token; revoke the OAuth App separately in GitHub if you no
longer want the authorization.

GitHub documents that OAuth Apps use scopes and that source-code access cannot
be made read-only through OAuth scopes. Keep the registrations dedicated and
review consent carefully; a future GitHub-App migration is needed for truly
fine-grained repository selection. See GitHub’s [OAuth authorization
guidance](https://docs.github.com/en/apps/oauth-apps/building-oauth-apps/authorizing-oauth-apps)
and [OAuth scope documentation](https://docs.github.com/en/apps/oauth-apps/building-oauth-apps/scopes-for-oauth-apps).

## Operator runbook

Use this checklist on the machine that runs the daemon. It is intentionally
safe to repeat: configuring a different client ID deletes the old token for
that capability rather than reusing it under a new registration.

1. For `/ask`, install a current Copilot CLI binary and confirm that the daemon
   can find it. The SDK starts that binary as an isolated runtime, but does not
   reuse its local login:

   ```sh
   copilot --version
   ```

2. Create/configure the capability and complete browser OAuth. The normal
   command waits for the callback; add `--no-wait` only when another terminal
   or automation will poll status afterward.

   ```sh
   localreview auth login --capability copilot --client-id YOUR_COPILOT_CLIENT_ID
   localreview auth status
   ```

3. Add `read` only when reviewing GitHub pull requests, and `write` only when
   deliberately publishing GitHub review feedback:

   ```sh
   localreview auth login --capability read --client-id YOUR_READ_CLIENT_ID
   localreview auth login --capability write --client-id YOUR_WRITE_CLIENT_ID
   localreview auth status
   ```

4. If port `8787` is already in use, stop the conflicting local listener or
   use device flow. Device flow is for a daemon with no usable local browser;
   it still stores the completed credential only on that host.

   ```sh
   localreview auth login --capability copilot --client-id YOUR_COPILOT_CLIENT_ID --device
   ```

`auth status` reports configuration and connection state only. It never prints
an access token, refresh token, browser capability, or OAuth callback URL with
state.

## Recovery and revocation

| Symptom | Safe next action |
| --- | --- |
| “configure … OAuth App client ID” | Create or locate the OAuth App under GitHub **OAuth apps**, then rerun `auth login --client-id …`. |
| `127.0.0.1:8787` is already in use | Stop the conflicting local listener, then retry loopback; or use `--device` on a constrained host. Do not change the callback in only one place. |
| Consent completed but status is disconnected | Run `localreview auth status` against the same daemon/data directory; if needed, start one new explicit login. Never paste a token. |
| Device code expired | Start one new `--device` flow and use the new code. Codes expire in roughly 15 minutes. |
| Wrong account/app or suspected compromise | `localreview auth logout <capability>`, revoke the OAuth App in GitHub's **Authorized OAuth Apps** settings, then configure/connect again. |
| `/ask` still cannot list models | Confirm only `copilot` is connected, check `copilot --version`, then use `/ask`'s actionable recovery. OAuth success is not proof of Copilot entitlement. |

`logout` deletes only the local OS-secret-store entry. Revocation at GitHub is
still an operator action, so use both steps when retiring a registration.

## Scope model and current limitation

The capability names (`read`, `write`, and `copilot`) are separate local
storage and routing boundaries; they are not magically fine-grained GitHub
permissions. The daemon now requests the narrowest OAuth scope GitHub offers
for each supported operation:

| Capability | Requested OAuth scope | Constraint and safeguard |
| --- | --- | --- |
| `read` | `repo` | GitHub OAuth has no read-only scope for private source code; `repo` also has write authority. The daemon structurally never routes this credential to publishing APIs, and Queue Home shows this warning. |
| `write` | `repo` | Required for explicit repository review publication. It is kept in a separate OS-secret-store account from `read`. |
| `copilot` | none | GitHub documents no OAuth scope that grants Copilot entitlement. The daemon requests no unrelated GitHub data scope and verifies Copilot separately through the SDK. |

Queue Home shows both requested and GitHub-reported granted scopes. A token
created before scope tracking is marked as unknown; disconnect and reconnect
that capability before relying on private repository access. This is an OAuth
platform limitation, not a reason to paste a broader credential into the app.

This repository has not validated a real GitHub API call or a real Copilot
completion against a self-hosted registration. Do not treat a successful
browser callback or an `authenticated` status as proof that private repository
access, GitHub publishing, or Copilot inference will succeed.

After connecting, test only the action you intend to use and inspect the
token-free error state: open a read-only PR locally, load `/ask` models, or
publish an explicitly disposable review. Never work around an authorization
failure by pasting a PAT, `gh` credential, or Copilot CLI token into
cmux-localreview; none is a supported fallback.
