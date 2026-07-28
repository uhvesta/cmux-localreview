---
name: localreview-submit
description: Submit the current workspace as an immutable cmux-localreview snapshot.
user-invocable: true
---

# Submit local review

From the workspace root, submit the current state with:

```sh
localreview submit --title "<review title>" --topic "<stable-topic>" .
```

Use the same absolute path and stable topic for future rounds. The submission
creates an immutable snapshot; it does not overwrite prior review evidence.
Report the queue ID and snapshot path, but do not claim feedback was delivered:
that is an explicit reviewer action in Queue Home. Do not include credentials,
terminal transcripts, or ACP/session identifiers in the submission.
