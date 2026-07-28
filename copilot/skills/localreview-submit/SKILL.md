---
name: localreview-submit
description: Capture the current Git worktree as an immutable cmux-localreview queue item for native review.
argument-hint: "[title]"
user-invocable: true
---

# Submit a local review

Use this skill when the current branch is ready for a human review in
cmux-localreview. The queue submission is a snapshot: it must describe the
current working directory and not a future or hypothetical revision.

1. Confirm the repository path with `pwd`, `git status --short`, and the
   branch/base information that matters to the review.
2. If the caller supplied a title, use it. Otherwise form a short title from
   the branch/change; do not include secrets or terminal output.
3. Submit from the repository root with the native CLI:

   ```sh
   localreview submit --title "<review title>" --topic "<stable-topic>" .
   ```

4. Report the returned queue ID and snapshot path. Do not claim that feedback
   was delivered: that is an explicit reviewer action in Queue Home.

The submission command captures cwd, Git branch/base/head, immutable snapshot,
cmux surface/workspace provenance when available. It never captures terminal
transcripts or credentials. Use the same path and stable topic for later
rounds; the native daemon links snapshots instead of overwriting prior review
evidence. The native feedback path uses explicit SDK delivery, not ACP or
cmux keystroke injection.
