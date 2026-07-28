import type { QueueItem } from "./queueStore.ts";
import { runCommand } from "./gitExec.ts";

/** Send the final remote review decision using the user's authenticated gh CLI. */
export async function submitRemoteDecision(item: QueueItem, decision: "approved" | "changes_requested", feedback: { body: string; path: string | null; line: number | null }[]): Promise<void> {
  if (!item.remoteUrl) return;
  const comments = feedback.map((entry) => entry.path ? `- ${entry.path}${entry.line ? `:${entry.line}` : ""}: ${entry.body}` : `- ${entry.body}`).join("\n");
  const body = [item.decisionBody, comments].filter(Boolean).join("\n\n") || "Reviewed with cmux-localreview.";
  const flag = decision === "approved" ? "--approve" : "--request-changes";
  const result = await runCommand(["gh", "pr", "review", item.remoteUrl, flag, "--body", body], item.workspacePath);
  if (result.exitCode !== 0) throw new Error(`GitHub review submission failed: ${result.stderr.trim()}`);
}
