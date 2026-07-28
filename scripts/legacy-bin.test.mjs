import assert from "node:assert/strict";
import { spawnSync } from "node:child_process";
import { mkdtempSync, rmSync, symlinkSync } from "node:fs";
import { tmpdir } from "node:os";
import { join, resolve } from "node:path";
import test from "node:test";

const guard = resolve("scripts/legacy-bin.mjs");

test("legacy queue-submit bin gives a native migration command without loading TypeScript", () => {
  const directory = mkdtempSync(join(tmpdir(), "localreview-legacy-bin-"));
  try {
    const bin = join(directory, "queue-submit");
    symlinkSync(guard, bin);
    const result = spawnSync(process.execPath, [bin], { encoding: "utf8" });
    assert.equal(result.status, 64);
    assert.match(result.stderr, /queue-submit is a retired TypeScript entrypoint/);
    assert.match(result.stderr, /localreview submit/);
    assert.doesNotMatch(result.stderr, /src\//);
  } finally {
    rmSync(directory, { recursive: true, force: true });
  }
});
