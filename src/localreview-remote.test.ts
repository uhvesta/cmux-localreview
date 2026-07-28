import { describe, expect, test } from "bun:test";

import { remoteDataDirectory, remoteEnvironment, tunnelCommand, validPort } from "./localreview-remote.ts";

describe("localreview-remote helpers", () => {
  test("produces a loopback-only forward with SSH batch safeguards", () => {
    expect(tunnelCommand({ sshTarget: "reviewer@example.test", remotePort: 4311, localPort: 5311 })).toBe(
      "ssh -N -o ExitOnForwardFailure=yes -o BatchMode=yes -L 127.0.0.1:5311:127.0.0.1:4311 'reviewer@example.test'",
    );
  });

  test("validates ports and creates an optional isolated data directory value", () => {
    expect(() => validPort(0, "--port")).toThrow("--port must be an integer");
    expect(remoteDataDirectory("relative-state")).toMatch(/relative-state$/);
    expect(remoteEnvironment("/srv/localreview")).toEqual({ CMUX_LOCALREVIEW_DATA_DIR: "/srv/localreview" });
  });
});
