import { createDiffSelection } from "../../vendor/difit/src/utils/diffSelection.ts";
import { DiffMode } from "../../vendor/difit/src/types/watch.ts";
import type { DiffSelection } from "../../vendor/difit/src/types/diff.ts";

export type SpecialBase = "working" | "staged" | ".";

function isSpecialBase(value: string): value is SpecialBase {
  return value === "working" || value === "staged" || value === ".";
}

/**
 * Mirrors difit CLI's `resolveDiffSelection` + `determineDiffMode` (see
 * `../../vendor/difit/src/cli/index.ts`) for the special targets ('.',
 * 'staged', 'working') plus arbitrary refs, without importing that file
 * directly — it runs `program.parseAsync()` at module load time, so it
 * can't be imported as a library.
 */
export function resolveDiffBase(target: string): { selection: DiffSelection; diffMode: DiffMode } {
  let baseCommitish: string;
  let targetCommitish = target;
  if (target === "working") {
    baseCommitish = "staged";
  } else if (isSpecialBase(target)) {
    baseCommitish = "HEAD";
  } else {
    // localreview's --base value is a comparison base, not the revision to
    // review.  Keep the current worktree (a detached remote PR HEAD for
    // cached GitHub reviews) as the target.  The previous `base^..base`
    // interpretation both hid the PR and let the revision picker request
    // origin/main against itself.
    baseCommitish = target;
    targetCommitish = ".";
  }

  const selection = createDiffSelection(baseCommitish, targetCommitish);

  let diffMode: DiffMode;
  if (target === "working") {
    diffMode = DiffMode.WORKING;
  } else if (target === "staged") {
    diffMode = DiffMode.STAGED;
  } else if (target === ".") {
    diffMode = DiffMode.DOT;
  } else {
    diffMode = DiffMode.DOT;
  }

  return { selection, diffMode };
}
