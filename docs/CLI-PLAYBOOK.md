# CLI playbook for humans and agents

Use this page as the safe, copyable command reference. All commands assume a
macOS or Linux checkout with Bun, Git, and GitHub CLI installed.

## First-time local review

```sh
# Authenticate once; the daemon reads this credential only for a request.
gh auth login --hostname github.com

# Install managed Copilot skills, start the loopback daemon, snapshot the
# repository, queue it, and open the exact review in the default browser.
localreview-start /absolute/path/to/project --setup-copilot --submit \
  --title "Review parser change" --base origin/main
```

`localreview-start` is safe to run again. Without `--submit`, it opens a
workspace reviewer but does not create a queue item. With `--home`, it opens
the global Queue Home. Add `--no-open --json` for automation.

## Install command launchers and Copilot skills

```sh
# Builds this checkout and writes user-owned launchers below ~/.local/bin.
# It also installs project skills if a target repository is supplied.
sh scripts/localreview-install.sh /absolute/path/to/project

# Or install/update only managed skills and instructions.
sh scripts/localreview-setup.sh /absolute/path/to/project
```

The setup tools are additive: unmanaged Copilot files are preserved. They do
not write credentials, daemon tokens, terminal output, or snapshots to the
project. Start a new Copilot session (or run `/skills reload`) after setup.

## Queue and reviewer commands

```sh
# Global queue (local and remote items)
localreview-start --home

# Open a workspace without submitting a review
localreview-start /absolute/path/to/project --base origin/main

# Submit a local immutable snapshot or a remote PR
localreview-submit /absolute/path/to/project --title "Review parser change"
localreview-submit https://github.com/OWNER/REPOSITORY/pull/123

# Reproduce a retained snapshot or explain an ACP/Copilot setup
localreview-reproduce /path/to/manifest.json /tmp/reproduced-review
localreview-reproduce-copilot QUEUE_ITEM_ID /tmp/reproduced-review
```

For a Copilot CLI agent, use the installed `/localreview-submit`,
`/localreview-feedback`, and `/localreview-reproduce` skills. An agent must
pass `--acp-host`, `--acp-port`, and `--acp-session-id` together when feedback
needs to return to the exact persistent ACP session.

## GitHub and failure recovery

```sh
gh auth status --hostname github.com
localreview-github-app status
```

`gh-cli` in the status output means the local default is ready. It never
reveals the token. Optional GitHub Apps are for separate least-privilege
identities and are configured with `localreview-github-app guide`.

If Queue Home says `gh` is unavailable, run `gh auth login --hostname
github.com` in the same user account that starts the daemon, then refresh the
page. Do not put a token in a URL, browser form, shell history, or project
configuration.
