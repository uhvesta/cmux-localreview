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
