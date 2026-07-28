# Setup: macOS, Linux, and remote workers

This guide describes the native Go runtime. Do not use historical `bun src/*`,
`gh`, PAT, or ACP setup instructions: they are not a supported credential or
feedback path.

## Choose an installation path

### Release archive (no source checkout at runtime)

`install.sh` detects macOS/Linux and `arm64`/`amd64`, downloads the matching
release archive, verifies its SHA-256 checksum, and installs two binaries into
`$LOCALREVIEW_INSTALL_PREFIX` (default `~/.local/bin`). It needs `curl`,
`tar`, and either `shasum` or `sha256sum`.

```sh
curl -fsSL https://raw.githubusercontent.com/uhvesta/cmux-localreview/main/install.sh | sh
export PATH="$HOME/.local/bin:$PATH"  # add through your own shell provisioning
localreview daemon run --port 0
```

The script does not install a package manager, change a shell profile, or
handle credentials.

### Build a checkout (macOS or Linux)

Install Git, Bazel/Bazelisk, and Bun. Bun is used only to build the embedded
web assets; installed native binaries do not start Node or Bun.

```sh
git clone https://github.com/uhvesta/cmux-localreview.git
cd cmux-localreview
sh scripts/localreview-install.sh /absolute/path/to/project
export PATH="$HOME/.local/bin:$PATH"
localreview daemon run --port 0
```

`scripts/localreview-install.sh` builds `localreview` and `localreviewd`,
installs them to `~/.local/bin` by default, and runs `localreview setup` for
the optional project argument. Alternatives:

```sh
# Show every file/build action without changing anything.
sh scripts/localreview-install.sh --dry-run /absolute/path/to/project

# Use a user-owned prefix and skip optional project skill installation.
sh scripts/localreview-install.sh --prefix "$HOME/.local" --no-project-setup
```

It intentionally does not use `sudo`, edit shell profiles, create a service,
or write credentials.

## First local review

```sh
# Terminal 1: keep the loopback daemon running.
localreview daemon run --port 0

# Terminal 2: capture the current Git state and enqueue it.
localreview submit --title 'Review parser change' --topic parser-boundaries \
  /absolute/path/to/project

# Opens Queue Home in the default browser. Add --no-open to print its URL.
localreview open
```

At Queue Home choose **Open workspace** for a queue item. The UI shows the
saved path and snapshot provenance; it does not silently open a mutable source
directory. Use the exact same path and topic for the next review round.

## Install Copilot skills

The setup command adds only cmux-localreview-owned files:

```sh
localreview setup /absolute/path/to/project
localreview setup --dry-run --json /absolute/path/to/project
localreview setup --personal /absolute/path/to/project
```

Project skills live under `.github/skills/`; `--personal` also installs them
under `~/.copilot/skills`. The managed block in
`.github/copilot-instructions.md` is bounded, and unrelated files are never
overwritten. Restart Copilot CLI or reload skills after changing them.

The installed skills describe `localreview submit`, formal-feedback export,
and snapshot reproduction. They do not receive daemon capabilities, OAuth
tokens, terminal transcripts, or a hidden ACP connection.

## Dedicated GitHub/OAuth capabilities

The native daemon has three separate capabilities:

| Capability | Needed for | Not used for |
| --- | --- | --- |
| `read` | resolving/mirroring a GitHub PR for local review | publishing feedback |
| `write` | explicit opt-in GitHub review publication | PR read or `/ask` |
| `copilot` | fresh SDK-native `/ask` and formal delivery | GitHub review publication |

Create the appropriate GitHub OAuth application registrations and configure
their public client IDs. Use a secret on stdin only if the registration is
confidential; never place it in argv or shell history.

```sh
localreview github-app guide
localreview github-app configure --capability read --client-id YOUR_CLIENT_ID
localreview github-app connect --capability read

# The default is a browser loopback flow; use --device on a headless host.
localreview github-app connect --capability copilot --device
localreview auth status
```

Tokens and optional client secrets stay in the OS secret store (Keychain on
macOS; a libsecret-backed `secret-tool` provider on Linux). The browser gets
neither OAuth tokens nor a persistent readable daemon token. The native daemon
does not fall back to `gh`, environment tokens, PATs, or an existing Copilot
CLI login.

## GitHub pull requests and local questions

Connect `read`, then queue a PR. The daemon resolves its canonical URL and
head, creates a managed cache worktree, and pins the review to that head:

```sh
localreview remote submit --title 'Review owner/repo#123' \
  https://github.com/OWNER/REPOSITORY/pull/123
localreview open
```

Opening the remote item lets you inspect the diff and use `/ask`; no `write`
capability is needed for local investigation. Refresh a remote item after a
new head before making a local decision. The currently native UI deliberately
rejects GitHub-review publication rather than pretending it was published;
save the decision locally until the write adapter ships.

## Remote machine role

Run the same native binary on a remote host with a separate data directory:

```sh
# Remote host; remain loopback-only and supervise with your normal tool.
localreview remote daemon run --port 4311 --data-dir /srv/cmux-localreview
```

The daemon never becomes public by changing this command. Add it to the local
Queue Home with `localreview federation add`; the native transport establishes
an ephemeral SSH loopback forward when a queue is opened. Validate SSH keys
and host verification first, and pass the discovery token through standard
input (`--token-stdin`), never as a command-line argument, prompt, or source
file.

## Verify installation

```sh
localreview daemon status
localreview open --no-open
go test ./...                 # source checkout only
bazel test //...              # source checkout only
```

If a command cannot discover the daemon, start it in the same environment
(especially the same `CMUX_LOCALREVIEW_DATA_DIR`). See
[Troubleshooting](TROUBLESHOOTING.md) for recovery steps.
