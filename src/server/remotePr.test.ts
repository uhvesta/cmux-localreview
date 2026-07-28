import { afterEach, describe, expect, test } from "bun:test";
import { execFileSync } from "node:child_process";
import { existsSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";

const temporaryRoots: string[] = [];

function tempRoot(): string {
  const root = mkdtempSync(join(tmpdir(), "cmux-localreview-remote-pr-test-"));
  temporaryRoots.push(root);
  return root;
}

function git(cwd: string, args: string[]): string {
  return execFileSync("git", args, { cwd, encoding: "utf8" }).trim();
}

afterEach(() => {
  while (temporaryRoots.length) rmSync(temporaryRoots.pop()!, { recursive: true, force: true });
});

describe("managed remote PR workspaces", () => {
  test("pins each fetched PR head in a separate worktree and cleans only managed paths", async () => {
    const root = tempRoot();
    const cache = join(root, "cache");
    // The module reads this at import time, before any cache paths are made.
    process.env.CMUX_LOCALREVIEW_CACHE_DIR = cache;
    const { cleanupRemoteWorkspace, prepareRemoteWorkspace, remoteWorkspacePaths } = await import("./remotePr.ts");
    const source = join(root, "source");
    const remote = join(root, "remote.git");
    execFileSync("git", ["init", "-q", source]);
    git(source, ["config", "user.email", "test@example.invalid"]);
    git(source, ["config", "user.name", "test"]);
    writeFileSync(join(source, "README.md"), "base\n");
    git(source, ["add", "README.md"]);
    git(source, ["commit", "-qm", "base"]);
    execFileSync("git", ["init", "--bare", "-q", remote]);
    git(source, ["remote", "add", "origin", remote]);
    git(source, ["push", "-q", "origin", "HEAD:refs/heads/main"]);

    writeFileSync(join(source, "README.md"), "first review revision\n");
    git(source, ["commit", "-am", "first review revision", "-q"]);
    const firstHead = git(source, ["rev-parse", "HEAD"]);
    git(source, ["push", "-q", "origin", `HEAD:refs/pull/42/head`]);
    const base = {
      url: "https://github.example/acme/widget/pull/42", number: 42, title: "Widget", body: "",
      state: "OPEN", isDraft: false, repository: "acme/widget", repositoryUrl: remote,
      headRefName: "feature", baseRefName: "main", baseSha: git(source, ["rev-parse", "HEAD~1"]),
    };
    const first = { ...base, headSha: firstHead };
    const firstWorkspace = await prepareRemoteWorkspace(first);
    expect(readFileSync(join(firstWorkspace.worktreePath, "README.md"), "utf8")).toBe("first review revision\n");
    expect(git(firstWorkspace.worktreePath, ["rev-parse", "HEAD"])).toBe(firstHead);

    writeFileSync(join(source, "README.md"), "second review revision\n");
    git(source, ["commit", "-am", "second review revision", "-q"]);
    const secondHead = git(source, ["rev-parse", "HEAD"]);
    git(source, ["push", "-q", "--force", "origin", `HEAD:refs/pull/42/head`]);
    const second = { ...base, headSha: secondHead };
    const secondWorkspace = await prepareRemoteWorkspace(second);
    expect(secondWorkspace.worktreePath).not.toBe(firstWorkspace.worktreePath);
    expect(readFileSync(join(secondWorkspace.worktreePath, "README.md"), "utf8")).toBe("second review revision\n");

    const firstCleanup = await cleanupRemoteWorkspace(first);
    expect(firstCleanup).toEqual({ worktreeRemoved: true, mirrorRemoved: false });
    const secondPaths = remoteWorkspacePaths(second);
    expect(secondPaths.mirrorPath).toBe(firstWorkspace.mirrorPath);
    const secondCleanup = await cleanupRemoteWorkspace(second, { removeMirror: true });
    expect(secondCleanup).toEqual({ worktreeRemoved: true, mirrorRemoved: true });
  });

  test("uses separate mirror keys for repositories on different GitHub hosts", async () => {
    const { remoteWorkspacePaths } = await import("./remotePr.ts");
    const common = {
      number: 1, title: "title", body: "", state: "OPEN", isDraft: false, repository: "acme/widget",
      repositoryUrl: "https://github.example/acme/widget.git", headRefName: "feature", headSha: "a".repeat(40),
      baseRefName: "main", baseSha: "b".repeat(40),
    };
    expect(remoteWorkspacePaths({ ...common, url: "https://github.com/acme/widget/pull/1" }).mirrorPath)
      .not.toBe(remoteWorkspacePaths({ ...common, url: "https://github.example/acme/widget/pull/1" }).mirrorPath);
  });

  test("refuses to publish a decision when the opened PR head is stale", async () => {
    const root = tempRoot();
    const { setGitHubFetchForTests, submitRemoteDecision } = await import("./remotePr.ts");
    const calls: string[] = [];
    setGitHubFetchForTests(async (input) => {
      calls.push(String(input));
      return new Response(JSON.stringify({
        number: 42, html_url: "https://github.example/acme/widget/pull/42", title: "Widget", body: "", state: "OPEN", draft: false,
        head: { sha: "2".repeat(40), ref: "feature" },
        base: { sha: "b".repeat(40), ref: "main", repo: { full_name: "acme/widget", clone_url: "https://github.example/acme/widget.git" } },
      }), { status: 200 });
    });
    try {
      const pr = {
        url: "https://github.example/acme/widget/pull/42", number: 42, title: "Widget", body: "", state: "OPEN", isDraft: false,
        repository: "acme/widget", repositoryUrl: "https://github.example/acme/widget.git", headRefName: "feature",
        headSha: "1".repeat(40), baseRefName: "main", baseSha: "b".repeat(40),
      };
      const item = {
        id: "item-1", remoteUrl: pr.url, workspacePath: root, decisionBody: "Please fix this.",
        snapshotManifest: { remotePullRequest: pr },
      } as any;
      await expect(submitRemoteDecision(item, "changes_requested", [{ body: "Bad edge case", path: "src/a.ts", line: 4 }], { read: "read-token", write: "write-token" }))
        .rejects.toThrow("refresh the queue before publishing a review");
      expect(calls).toEqual(["https://github.example/api/v3/repos/acme/widget/pulls/42"]);
    } finally {
      setGitHubFetchForTests();
    }
  });
});
