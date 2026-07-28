# cmux-localreview

`cmux-localreview` is a local multi-repository review UI with persistent comments, `/btw`, and a global local-review queue.

## Common workflow

```sh
# Start/open the review UI for a workspace (starts the daemon when needed)
bun src/localreview-open.ts /path/to/workspace

# Capture an immutable local snapshot and enqueue it
bun src/queue-submit.ts /path/to/workspace --title "Review parser change"

# A GitHub PR URL is accepted too; it is mirrored under ~/.cache/cmux-localreview
bun src/queue-submit.ts https://github.com/org/repo/pull/123

# Reconstruct a retained snapshot elsewhere
bun src/localreview-reproduce.ts /path/to/manifest.json /tmp/reproduced-review
```

The daemon only binds `127.0.0.1`. Its discovery file is
`~/.local/share/cmux-localreview/daemon.json`, is written atomically with mode
`0600`, and holds the bearer token used by queue/workspace/agent control APIs.
The browser receives that token only through a URL fragment from
`localreview-open`; fragments are not sent to the server.

Run the standalone reviewer with `bun src/cli.ts [workspace]`; run the daemon
explicitly with `bun src/global-daemon.ts`. Build the client with `bun run build`.

Snapshots use a temporary Git index, leaving the source repository's HEAD,
index, and working tree untouched. Each retained manifest includes SHA-256
checks for its self-contained Git bundles.
