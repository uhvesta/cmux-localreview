---
name: localreview-submit
description: Submit the current workspace as an immutable cmux-localreview snapshot.
user-invocable: true
---

# Submit local review

From the workspace root, submit the current state with:

```sh
localreview queue-submit . --title "<review title>"
```

Do not include credentials or terminal transcripts in the submission.
