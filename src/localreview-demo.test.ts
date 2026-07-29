import { afterEach, describe, expect, test } from "bun:test";
import { existsSync, rmSync } from "node:fs";

import { createDemoPaths, startDemo } from "./localreview-demo.ts";

const roots: string[] = [];

afterEach(() => {
  while (roots.length) rmSync(roots.pop()!, { recursive: true, force: true });
});

describe("localreview-demo", () => {
  test("starts an isolated fixture with a queue item and reviewer URL", async () => {
    const demo = await startDemo();
    roots.push(demo.paths.root);
    try {
      expect(demo.reviewUrl).toMatch(/^http:\/\/127\.0\.0\.1:\d+\/review#bootstrapCode=/);
      expect(existsSync(demo.paths.dataDir)).toBe(true);
      expect(existsSync(demo.paths.workspace)).toBe(true);
      expect(demo.itemId).toBeTruthy();
      expect((await fetch(`${demo.baseUrl}/health`)).status).toBe(200);
      // `/review` is a SPA route rather than a physical static file. Keep a
      // browser-facing smoke assertion here so a daemon can never publish a
      // demo URL that opens to Express's missing-file page.
      expect((await fetch(demo.reviewUrl)).status).toBe(200);
      const queue = await fetch(`${demo.baseUrl}/api/queue`, { headers: { authorization: `Bearer ${demo.daemon.discovery.token}` } });
      expect((await queue.json() as { items: { id: string }[] }).items.some((item) => item.id === demo.itemId)).toBe(true);
    } finally {
      await demo.daemon.close();
      demo.restoreEnvironment();
    }
  });

  test("rejects a requested data directory inside the source checkout", () => {
    expect(() => createDemoPaths(process.cwd())).toThrow("inside this source checkout");
  });
});
