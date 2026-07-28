import { describe, expect, test } from "bun:test";

import { GitHubAuthService, type GitHubAuthDependencies } from "./githubAuth.ts";
import type { CommandResult } from "./gitExec.ts";

function result(exitCode: number, stdout = "", stderr = ""): CommandResult {
  return { exitCode, stdout, stderr };
}

function closedStream(): ReadableStream<Uint8Array> {
  return new ReadableStream({ start(controller) { controller.close(); } });
}

describe("GitHubAuthService", () => {
  test("reports the GitHub CLI identity and a Copilot installation without exposing credentials", async () => {
    const seen: string[][] = [];
    const service = new GitHubAuthService({
      run: async (command) => {
        seen.push(command);
        if (command[0] === "gh" && command[1] === "--version") return result(0, "gh version 2.99.0");
        if (command[0] === "gh" && command[1] === "auth") return result(0);
        if (command[0] === "gh" && command[1] === "api") return result(0, "octocat\n");
        if (command[0] === "copilot") return result(0, "1.2.3\n");
        return result(1, "", "unexpected command");
      },
    });

    expect(await service.status()).toMatchObject({
      gh: { installed: true, authenticated: true, login: "octocat" },
      copilot: { installed: true, version: "1.2.3" },
      login: { state: "idle" },
    });
    expect(seen).toContainEqual(["gh", "api", "user", "--hostname", "github.com", "--jq", ".login"]);
  });

  test("starts exactly one browser OAuth login and surfaces completion", async () => {
    let finish!: (code: number) => void;
    const started: string[][] = [];
    const dependencies: GitHubAuthDependencies = {
      run: async () => result(1, "", "not logged in"),
      spawn: (command) => {
        started.push(command);
        return { stdout: closedStream(), stderr: closedStream(), exited: new Promise((resolve) => { finish = resolve; }), kill: () => undefined };
      },
    };
    const service = new GitHubAuthService(dependencies);
    expect(service.start()).toMatchObject({ state: "waiting" });
    expect(service.start()).toMatchObject({ state: "waiting" });
    expect(started).toEqual([["gh", "auth", "login", "--hostname", "github.com", "--git-protocol", "ssh", "--web", "--scopes", "repo,read:org"]]);
    finish(0);
    await new Promise((resolve) => setTimeout(resolve, 0));
    expect((await service.status()).login).toMatchObject({ state: "succeeded" });
  });

  test("makes a missing GitHub CLI an actionable unavailable state", async () => {
    const service = new GitHubAuthService({ run: async () => { throw new Error("Executable not found"); } });
    expect(await service.status()).toMatchObject({
      gh: { installed: false, authenticated: false },
      copilot: { installed: false },
    });
  });
});
