# GitHub OAuth setup

Queue Home uses dedicated GitHub **OAuth App** registrations. This is not a
GitHub App installation, a personal access token, a `gh` login, or a Copilot
CLI session. The daemon owns every resulting access token in the platform
secret store; the browser receives only a short-lived loopback capability and
never receives a GitHub token.

## What to create interactively

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
