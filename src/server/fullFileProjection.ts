import type { DiffChunk, DiffLine } from "../../vendor/difit/src/types/diff.ts";

export interface FullFileGate {
  /** Line number (in the shown side's own numbering) after which this gate sits; 0 = before the first line. */
  afterLine: number;
  /** Line range on the *hidden* side (old, for a 'current'-side gate; new, for a 'base'-side gate). */
  hiddenStart: number;
  hiddenEnd: number;
  lines: string[];
}

export type FullFileSide = "current" | "base";

/**
 * Projects a diff's chunks into gates for a Full File view (SPEC.md §2).
 * For side='current': the full current file *is* the new-side content, so
 * gaps are exactly the deleted-line runs (not present in the current file) —
 * anchored at the new-line number of the line before them.
 * For side='base': symmetric, gaps are added-line runs anchored at the
 * old-line number of the line before them.
 * Pure and pre-render: doesn't need the full file text at all, only the
 * diff chunks — the gate content comes straight from the diff lines.
 */
export function buildGates(chunks: DiffChunk[], side: FullFileSide): FullFileGate[] {
  const gateLineType: DiffLine["type"] = side === "current" ? "delete" : "add";
  const gates: FullFileGate[] = [];
  let lastAnchorLine = 0;

  let pending: { hiddenStart: number; hiddenEnd: number; lines: string[] } | null = null;

  const flush = () => {
    if (pending) {
      gates.push({ afterLine: lastAnchorLine, ...pending });
      pending = null;
    }
  };

  for (const chunk of chunks) {
    for (const line of chunk.lines) {
      if (line.type === gateLineType) {
        const hiddenLineNumber = side === "current" ? line.oldLineNumber : line.newLineNumber;
        if (!pending) {
          pending = {
            hiddenStart: hiddenLineNumber ?? 0,
            hiddenEnd: hiddenLineNumber ?? 0,
            lines: [],
          };
        }
        pending.lines.push(line.content);
        if (hiddenLineNumber !== undefined) pending.hiddenEnd = hiddenLineNumber;
        continue;
      }

      flush();
      const shownLineNumber = side === "current" ? line.newLineNumber : line.oldLineNumber;
      if (shownLineNumber !== undefined) lastAnchorLine = shownLineNumber;
    }
  }
  flush();

  return gates;
}
