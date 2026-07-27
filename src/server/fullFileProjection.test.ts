import { describe, expect, test } from "bun:test";
import type { DiffChunk, DiffLine } from "../../vendor/difit/src/types/diff.ts";

import { buildGates } from "./fullFileProjection.ts";

function normal(oldN: number, newN: number, content = `ctx${oldN}`): DiffLine {
  return { type: "normal", content, oldLineNumber: oldN, newLineNumber: newN };
}
function del(oldN: number, content = `del${oldN}`): DiffLine {
  return { type: "delete", content, oldLineNumber: oldN };
}
function add(newN: number, content = `add${newN}`): DiffLine {
  return { type: "add", content, newLineNumber: newN };
}
function chunk(lines: DiffLine[]): DiffChunk {
  return { header: "", oldStart: 1, oldLines: 1, newStart: 1, newLines: 1, lines };
}

describe("buildGates — side=current (deletions become gates)", () => {
  test("a single deletion mid-file anchors after the preceding kept line", () => {
    const chunks = [chunk([normal(1, 1), del(2), normal(3, 2)])];
    const gates = buildGates(chunks, "current");
    expect(gates).toEqual([{ afterLine: 1, hiddenStart: 2, hiddenEnd: 2, lines: ["del2"] }]);
  });

  test("a run of consecutive deletions becomes one gate spanning the whole range", () => {
    const chunks = [chunk([normal(1, 1), del(2), del(3), del(4), normal(5, 2)])];
    const gates = buildGates(chunks, "current");
    expect(gates).toEqual([
      { afterLine: 1, hiddenStart: 2, hiddenEnd: 4, lines: ["del2", "del3", "del4"] },
    ]);
  });

  test("deletions at the very start of the file anchor at line 0", () => {
    const chunks = [chunk([del(1), del(2), normal(3, 1)])];
    const gates = buildGates(chunks, "current");
    expect(gates).toEqual([{ afterLine: 0, hiddenStart: 1, hiddenEnd: 2, lines: ["del1", "del2"] }]);
  });

  test("deletions at EOF (no trailing kept line) still produce a gate", () => {
    const chunks = [chunk([normal(1, 1), del(2), del(3)])];
    const gates = buildGates(chunks, "current");
    expect(gates).toEqual([{ afterLine: 1, hiddenStart: 2, hiddenEnd: 3, lines: ["del2", "del3"] }]);
  });

  test("two separate hunks with deletions produce two distinct gates anchored correctly", () => {
    const chunks = [
      chunk([normal(1, 1), del(2), normal(3, 2)]),
      chunk([normal(10, 9), del(11), normal(12, 10)]),
    ];
    const gates = buildGates(chunks, "current");
    expect(gates).toHaveLength(2);
    expect(gates[0]).toEqual({ afterLine: 1, hiddenStart: 2, hiddenEnd: 2, lines: ["del2"] });
    expect(gates[1]).toEqual({ afterLine: 9, hiddenStart: 11, hiddenEnd: 11, lines: ["del11"] });
  });

  test("adjacent hunks separated only by an addition don't merge their deletion gates", () => {
    // normal, delete, add, normal — the add line flushes the pending
    // deletion gate before a second (empty-content) deletion run could ever
    // wrongly merge with anything after it.
    const chunks = [chunk([normal(1, 1), del(2), add(2), normal(3, 3)])];
    const gates = buildGates(chunks, "current");
    expect(gates).toEqual([{ afterLine: 1, hiddenStart: 2, hiddenEnd: 2, lines: ["del2"] }]);
  });

  test("a file with only additions (no deletions) produces no gates", () => {
    const chunks = [chunk([normal(1, 1), add(2), normal(2, 3)])];
    expect(buildGates(chunks, "current")).toEqual([]);
  });

  test("no chunks at all produces no gates", () => {
    expect(buildGates([], "current")).toEqual([]);
  });
});

describe("buildGates — side=base (additions become gates)", () => {
  test("a single addition anchors after the preceding old-side line", () => {
    const chunks = [chunk([normal(1, 1), add(2), normal(2, 3)])];
    const gates = buildGates(chunks, "base");
    expect(gates).toEqual([{ afterLine: 1, hiddenStart: 2, hiddenEnd: 2, lines: ["add2"] }]);
  });

  test("a run of additions becomes one gate", () => {
    const chunks = [chunk([normal(1, 1), add(2), add(3), add(4), normal(2, 5)])];
    const gates = buildGates(chunks, "base");
    expect(gates).toEqual([
      { afterLine: 1, hiddenStart: 2, hiddenEnd: 4, lines: ["add2", "add3", "add4"] },
    ]);
  });

  test("additions at start of file anchor at 0", () => {
    const chunks = [chunk([add(1), normal(1, 2)])];
    const gates = buildGates(chunks, "base");
    expect(gates).toEqual([{ afterLine: 0, hiddenStart: 1, hiddenEnd: 1, lines: ["add1"] }]);
  });

  test("a file with only deletions (no additions) produces no gates", () => {
    const chunks = [chunk([normal(1, 1), del(2), normal(3, 2)])];
    expect(buildGates(chunks, "base")).toEqual([]);
  });
});
