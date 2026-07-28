import { describe, expect, test } from "bun:test";

import { redactSubmissionMetadata, summarizeCmuxSurfaces } from "./submissionContext.ts";

describe("submission provenance helpers", () => {
  test("summarizes only portable cmux surface metadata", () => {
    expect(summarizeCmuxSurfaces({ surfaces: [
      { surface_id: "surface-a", workspace_id: "workspace-a", title: "Codex", focused: true, transcript: "must not be copied" },
      { id: "surface-b", name: "shell" },
    ] })).toEqual([
      { id: "surface-a", workspaceId: "workspace-a", title: "Codex", focused: true },
      { id: "surface-b", title: "shell" },
    ]);
  });

  test("redacts credentials recursively before durable persistence", () => {
    expect(redactSubmissionMetadata({ token: "abc", nested: { apiKey: "xyz", okay: "kept" } })).toEqual({
      token: "[redacted]",
      nested: { apiKey: "[redacted]", okay: "kept" },
    });
  });
});
