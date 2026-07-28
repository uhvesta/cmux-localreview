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

Do not include credentials or terminal transcripts in the submission.
