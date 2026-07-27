import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

export interface ResolvedAgentCommand {
  command: string;
  args: string[];
}

/**
 * Resolves the `--agent <provider|cmd>` CLI value (SPEC.md §4) to a
 * spawnable command. 'claude' (the default) resolves to the locally
 * installed @zed-industries/claude-code-acp binary; anything else is
 * treated as an arbitrary command already on PATH (or an explicit path).
 */
export function resolveAgentCommand(agent: string): ResolvedAgentCommand {
  if (agent === "claude") {
    const binPath = join(
      dirname(fileURLToPath(import.meta.url)),
      "..",
      "..",
      "node_modules",
      ".bin",
      "claude-code-acp",
    );
    return { command: binPath, args: [] };
  }
  const [command, ...args] = agent.trim().split(/\s+/);
  return { command: command ?? agent, args };
}
