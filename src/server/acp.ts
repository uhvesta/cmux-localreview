import { type ChildProcessWithoutNullStreams, spawn } from "node:child_process";
import { Readable, Writable } from "node:stream";

import {
  ClientSideConnection,
  ndJsonStream,
  type Client,
  type ContentBlock,
  type PromptResponse,
  type RequestPermissionRequest,
  type RequestPermissionResponse,
  type SessionNotification,
} from "@zed-industries/agent-client-protocol";

const PROTOCOL_VERSION = 1;
const DEFAULT_STARTUP_TIMEOUT_MS = 10_000;

export interface AcpSessionOptions {
  /** Command to spawn the agent (e.g. resolved path to `claude-code-acp`, or a custom `--agent` value). */
  command: string;
  args?: string[];
  cwd: string;
  onUpdate: (notification: SessionNotification) => void;
  startupTimeoutMs?: number;
  /** Resume a prior session (best-effort — many agents don't support `session/load`; falls back to a fresh session). */
  resumeSessionId?: string;
}

/**
 * Read-only auto-approve permission policy for /btw (SPEC.md §4): btw is a
 * question channel, not an edit channel — allow read/search/think/fetch
 * tool kinds, deny everything else (edit/delete/move/execute/unknown).
 */
function decidePermission(request: RequestPermissionRequest): RequestPermissionResponse {
  const kind = request.toolCall.kind;
  const isReadOnly = kind === "read" || kind === "search" || kind === "think" || kind === "fetch";

  const wantedPrefix = isReadOnly ? "allow" : "reject";
  const option =
    request.options.find((o) => o.kind.startsWith(wantedPrefix)) ??
    request.options.find((o) => o.kind.startsWith("reject"));

  if (!option) {
    return { outcome: { outcome: "cancelled" } };
  }
  return { outcome: { outcome: "selected", optionId: option.optionId } };
}

export class AcpStartupTimeoutError extends Error {
  constructor(message = "ACP agent did not respond to initialize/session setup in time") {
    super(message);
    this.name = "AcpStartupTimeoutError";
  }
}

/**
 * One ACP agent subprocess + session, wrapping @zed-industries/agent-client-protocol.
 * Prompts are serialized (agents reject overlapping session/prompt calls) by
 * chaining onto a promise; session/request_permission is answered with the
 * read-only auto-approve policy above.
 */
export class AcpSession {
  readonly sessionId: string;
  private child: ChildProcessWithoutNullStreams;
  private conn: ClientSideConnection;
  private promptChain: Promise<unknown> = Promise.resolve();
  private disposed = false;

  private constructor(child: ChildProcessWithoutNullStreams, conn: ClientSideConnection, sessionId: string) {
    this.child = child;
    this.conn = conn;
    this.sessionId = sessionId;
  }

  static async spawn(options: AcpSessionOptions): Promise<AcpSession> {
    const child = spawn(options.command, options.args ?? [], {
      cwd: options.cwd,
      stdio: ["pipe", "pipe", "pipe"],
    });

    child.stderr.on("data", () => {
      // ACP agents commonly log diagnostics to stderr; swallow by default.
      // (Left as a hook point — could be surfaced via a --verbose flag.)
    });

    const stream = ndJsonStream(
      Writable.toWeb(child.stdin) as WritableStream<Uint8Array>,
      Readable.toWeb(child.stdout) as ReadableStream<Uint8Array>,
    );

    let onUpdate: (n: SessionNotification) => void = options.onUpdate;
    const client: Client = {
      sessionUpdate: async (notification) => {
        onUpdate(notification);
      },
      requestPermission: async (request) => decidePermission(request),
    };

    const conn = new ClientSideConnection(() => client, stream);

    const timeoutMs = options.startupTimeoutMs ?? DEFAULT_STARTUP_TIMEOUT_MS;
    const setup = (async () => {
      await conn.initialize({ protocolVersion: PROTOCOL_VERSION, clientCapabilities: { fs: {} } });

      if (options.resumeSessionId) {
        try {
          await conn.loadSession({
            sessionId: options.resumeSessionId,
            cwd: options.cwd,
            mcpServers: [],
          });
          return options.resumeSessionId;
        } catch {
          // Agent doesn't support session/load (or the session is gone) —
          // this is expected/normal, not an error condition (SPEC.md §4).
        }
      }

      const { sessionId } = await conn.newSession({ cwd: options.cwd, mcpServers: [] });
      return sessionId;
    })();

    let timeoutHandle: ReturnType<typeof setTimeout>;
    const timeout = new Promise<never>((_, reject) => {
      timeoutHandle = setTimeout(() => reject(new AcpStartupTimeoutError()), timeoutMs);
    });

    let sessionId: string;
    try {
      sessionId = await Promise.race([setup, timeout]);
    } catch (error) {
      child.kill();
      throw error;
    } finally {
      clearTimeout(timeoutHandle!);
    }

    return new AcpSession(child, conn, sessionId);
  }

  /** Serialized: a second call while one is in flight queues behind it. */
  prompt(text: string, contentBlocks?: ContentBlock[]): Promise<PromptResponse> {
    const run = () =>
      this.conn.prompt({
        sessionId: this.sessionId,
        prompt: contentBlocks ?? [{ type: "text", text }],
      });
    const next = this.promptChain.then(run, run);
    this.promptChain = next.catch(() => undefined);
    return next;
  }

  async cancel(): Promise<void> {
    await this.conn.cancel({ sessionId: this.sessionId });
  }

  dispose(): void {
    if (this.disposed) return;
    this.disposed = true;
    this.child.kill();
  }
}
