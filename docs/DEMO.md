# Disposable demo and source install

This document gives an agent or human a safe way to try the full local review
loop without GitHub, Copilot, cmux, SSH, or an existing repository. It also
explains the optional per-user CLI install for a remote or fresh machine.

## Run a credential-free demo

From this checkout, run:

```sh
bun src/localreview-demo.ts
# or, after the optional install below:
localreview-demo
```

The command creates one new system temporary directory containing both daemon
state and a disposable Git fixture. The fixture has a committed baseline and
one uncommitted change, then it queues and opens that fixture automatically.
It never snapshots, registers, or writes into this source checkout. It prints
a tokenized `http://127.0.0.1:...` URL that can be pasted directly into Chrome
or Firefox. The bearer token stays in the URL fragment and therefore is not
sent to the web server.

Pass `--open` only if you want the command to launch the default browser. Use
`--no-fixture` to inspect an empty Queue Home instead. `--json` produces the
same URLs, temporary paths, and queue ID for an agent harness.

```sh
# Do not open a browser; emit machine-readable test coordinates.
bun src/localreview-demo.ts --json

# Explicitly open the seeded reviewer in the default browser.
bun src/localreview-demo.ts --open
```

Keep the command running while testing. Press `Ctrl-C` to stop the loopback
daemon, then remove exactly the temporary directory printed by the command if
you no longer want the fixture or its review snapshot. No remote network call
or credential lookup is performed.

## Install launchers from a checkout (macOS/Linux)

The portable setup wrapper can build this checkout and install launchers under
a user-owned prefix. It does **not** use `sudo`, alter a shell profile, install
Bun, change Git configuration, or copy credentials. The installed launchers
continue to run this checkout, so keep the checkout at the same path; rerun
the installer after moving/updating it.

```sh
# Build and install wrappers under ~/.local/bin, then add skills to a project.
sh scripts/localreview-setup.sh --install /absolute/path/to/project

# Use a separate user-controlled prefix, without configuring a project.
sh scripts/localreview-setup.sh --install --prefix "$HOME/.local" --no-project-setup

# Inspect actions only; this does not build or write files.
sh scripts/localreview-setup.sh --install --dry-run --prefix "$HOME/.local" /absolute/path/to/project
```

The install runs `bun install --frozen-lockfile` and the production build before
replacing any launcher. Use `--no-deps` only when dependencies are already
present and a network-free install is required. Launchers are staged first, so
a failed dependency install, build, or staging step leaves a previous install
intact. When a project path is supplied, it automatically invokes the
safe Copilot setup with the installed `localreview-submit` and reproduction
commands. Existing unmanaged Copilot files remain untouched.

If `~/.local/bin` is not already on `PATH`, add it through your normal shell
or host-provisioning process. The installer deliberately does not edit shell
profiles. Confirm the setup with:

```sh
localreview-demo --help
localreview-submit --help
localreview-remote --help
```

## Remote host checklist

On a remote macOS/Linux worker, clone or otherwise place this repository in a
stable user-owned path, run the install command above, and choose a
user-owned daemon data directory when starting the remote daemon:

```sh
localreview-remote-daemon --port 4311 --data-dir "$HOME/.local/share/cmux-localreview"
```

Keep the daemon loopback-only and use `localreview-remote tunnel` (or the
matching shell wrapper) from the reviewer machine. Do not use the demo data
directory for a persistent remote daemon.
