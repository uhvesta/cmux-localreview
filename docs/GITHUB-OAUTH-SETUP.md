# Desktop GitHub authorization

CMUX Local Review is a desktop product. Electron is the supported UI and owns
the loopback daemon as a private sidecar. The daemon uses the operating
system's secure credential store (macOS Keychain, Windows Credential Manager,
or Linux Secret Service) for its own credentials.

It does **not** use `gh auth token`, a personal access token, environment
credentials, a Copilot CLI login, or a plaintext credential file. Reusing a
`gh` credential would couple this app to a broad, general-purpose token and
would remove separate consent and revocation.

## Why desktop sign-in uses Device Flow

The supported desktop flow is GitHub's OAuth 2.0 Device Authorization Grant:

1. Queue Home asks GitHub for a short-lived device and user code.
2. Electron displays the user code and opens `https://github.com/login/device`
   in the user's normal browser.
3. The user signs in and approves that one code at GitHub.
4. The Electron-owned daemon polls GitHub at the prescribed interval and saves
   the resulting credential in the OS secret store.

There is no local OAuth callback listener, no redirect URL with an access
token, and no requirement for the browser to connect back to the app. The
browser is only GitHub's identity and consent surface. GitHub documents Device
Flow as the OAuth mechanism for desktop and headless clients. [GitHub OAuth
device flow](https://docs.github.com/en/apps/oauth-apps/building-oauth-apps/authorizing-oauth-apps)

## Register one desktop OAuth App

Create an **OAuth App** under your GitHub account: **Settings → Developer
settings → OAuth apps → New OAuth App**. This is not a GitHub App installation.

- Give it a public, non-sensitive name and homepage.
- GitHub requires an authorization callback field even when this desktop app
  uses Device Flow. Use a harmless stable value such as
  `http://127.0.0.1:8787/oauth/callback`; the supported desktop path does not
  listen on or redirect to it.
- Enable **Device Flow**.
- Copy only the public **Client ID**.
- Do not copy, ship, enter, or store the displayed client secret. This is a
  public desktop client and localreview has no client-secret input or fallback.

One registration under your account can identify your desktop app globally.
The Client ID is public configuration; each machine receives its own approved
credential, stored locally in that machine's secure store. Disconnecting from
Queue Home removes only this app's local credential; revoke the OAuth App from
GitHub's Authorized OAuth Apps page to revoke server-side authorization.

## Connect it in the desktop app

1. Open the Electron app and go to **Queue Home → GitHub connections**.
2. Select the capability to connect:

   | Capability | Purpose |
   | --- | --- |
   | `copilot` | fresh Copilot SDK `/ask` chats |
   | `read` | mirror/inspect GitHub pull requests locally |
   | `write` | explicitly publish GitHub review feedback |

3. Paste the public Client ID and choose **Save OAuth client ID**.
4. Click **Connect**. The app displays a short-lived user code.
5. Complete that code at GitHub in the browser page it opens.
6. Return to the desktop app; it polls and changes the capability to
   **Connected**. No token is displayed or pasted.

If the normal browser does not open, use the visible `github.com/login/device`
link and displayed code yourself. Refreshing Queue Home does not lose an
in-progress code: the daemon returns that short-lived public approval data
until it expires, succeeds, or is canceled. Use **Cancel device flow** to
discard a code before starting a replacement.

The capability split is a local secret-store and routing boundary. OAuth scope
limits still apply: GitHub OAuth cannot make private repository source access
strictly read-only, so `read` and `write` should use separate registrations
and credentials. The `copilot` registration requests no unrelated repository
scope; a successful authorization is not by itself proof of a Copilot
entitlement.

## CLI and debug-only paths

The CLI remains for setup, submission, remote workers, and diagnostics, but
the supported reviewer UI is Electron. A CLI can initiate the same Device
Flow without borrowing `gh`:

```sh
localreview auth login --capability copilot \
  --client-id YOUR_COPILOT_CLIENT_ID --device
localreview auth status
```

The old loopback callback flow remains an internal development/test path while
the migration is validated. It is not surfaced by the desktop UI and must not
be used as a production workaround for failed Device Flow.

## Recovery and revocation

| Symptom | Safe next action |
| --- | --- |
| No Client ID configured | Save the public ID from the dedicated OAuth App in Queue Home. |
| Device code expired | Click **Connect** once more and use the new code; do not paste a token. |
| Browser did not open | Use the displayed `github.com/login/device` link and code; no callback listener is required. |
| Started the wrong capability/account | Click **Cancel device flow**, then start the intended capability again. |
| Wrong GitHub account/app | Disconnect the capability, revoke the OAuth App in GitHub, then configure/connect again. |
| Keychain unavailable | Repair/unlock the OS secret store. The desktop app does not fall back to plaintext. |
| `/ask` cannot list models | Confirm `copilot` shows Connected, then check the displayed Copilot entitlement recovery; OAuth success is not a model-access guarantee. |

For hosted multi-user deployments, prefer a GitHub App for GitHub repository
permissions because GitHub Apps provide fine-grained repository selection and
short-lived tokens. Do not assume a GitHub App token is accepted by the
Copilot SDK without an explicit SDK compatibility validation.
