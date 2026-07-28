---
name: localreview-reproduce
description: Reproduce an immutable cmux-localreview snapshot safely.
user-invocable: true
---

# Reproduce a review

Materialize only into a new or empty destination with:

```sh
localreview reproduce --copilot <queue-id> <empty-directory>
```

A saved snapshot does not restore an expired Copilot session, SSH tunnel, or
authentication state. Open the reproduced directory for a fresh native `/ask`
conversation. Historic session IDs are provenance only and no prompt is replayed
when a reproduction or transcript is opened.
