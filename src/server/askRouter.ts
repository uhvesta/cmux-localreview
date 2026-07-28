import { createHash } from "node:crypto";
import { realpathSync } from "node:fs";
import { homedir } from "node:os";
import { join, relative, resolve } from "node:path";
import type { Database } from "bun:sqlite";
import { CopilotClient, type CopilotSession, type ModelInfo, type PermissionRequest } from "@github/copilot-sdk";
import express, { type Response, type Router } from "express";

import {
  createAskConversation,
  getAskConversation,
  insertAskMessage,
  listAskConversations,
  listAskMessages,
  updateAskConversation,
  updateAskMessage,
} from "./askStore.ts";

type ActiveAsk = { session: CopilotSession; model: string | null; sending: boolean };

export interface AskRouterOptions {
  db: Database;
  workspaceRoot: string;
}

/**
 * Owns the Copilot SDK connection for one review workspace.  The client runs
 * in that workspace but its persistent runtime state is kept outside the
 * repository.  The permission handler is intentionally read-only: no shell,
 * write, network, MCP, memory, or extension request can be approved here.
 */
export class AskService {
  private readonly workspaceRoot: string;
  private readonly realWorkspaceRoot: string;
  private readonly client: CopilotClient;
  private readonly active = new Map<string, ActiveAsk>();

  constructor(private readonly db: Database, workspaceRoot: string) {
    this.workspaceRoot = resolve(workspaceRoot);
    this.realWorkspaceRoot = realpathSync(this.workspaceRoot);
    const key = createHash("sha256").update(this.realWorkspaceRoot).digest("hex").slice(0, 16);
    this.client = new CopilotClient({
      workingDirectory: this.realWorkspaceRoot,
      baseDirectory: join(homedir(), ".local", "share", "cmux-localreview", "copilot", key),
      logLevel: "warning",
    });
  }

  async listModels(): Promise<ModelInfo[]> {
    await this.client.start();
    return this.client.listModels();
  }

  async close(): Promise<void> {
    for (const { session } of this.active.values()) {
      await session.disconnect().catch(() => undefined);
    }
    this.active.clear();
    await this.client.stop();
  }

  async abort(conversationId: string): Promise<boolean> {
    const active = this.active.get(conversationId);
    if (!active?.sending) return false;
    await active.session.abort();
    return true;
  }

  private mayRead(request: PermissionRequest): boolean {
    if (request.kind !== "read" || request.requestSandboxBypass) return false;
    try {
      const requested = realpathSync(resolve(this.realWorkspaceRoot, request.path));
      const rel = relative(this.realWorkspaceRoot, requested);
      return rel === "" || (!rel.startsWith("..") && !rel.includes(`${process.platform === "win32" ? "\\\\" : "/"}..`));
    } catch {
      // A missing path is not a valid read, and failure to resolve a symlink
      // must never broaden access beyond the review workspace.
      return false;
    }
  }

  private readonly readOnlyPermissions = (request: PermissionRequest) => {
    if (this.mayRead(request)) return { kind: "approve-once" } as const;
    return { kind: "reject", feedback: "The /ask review channel has read-only access to this workspace." } as const;
  };

  private async sessionFor(conversationId: string): Promise<ActiveAsk> {
    const conversation = getAskConversation(this.db, conversationId);
    if (!conversation) throw new Error("Unknown /ask conversation");
    const running = this.active.get(conversationId);
    if (running) return running;

    await this.client.start();
    const config = {
      model: conversation.model ?? undefined,
      onPermissionRequest: this.readOnlyPermissions,
      enableConfigDiscovery: false,
      skipCustomInstructions: true,
      systemMessage: {
        mode: "append" as const,
        content:
          "You are a code-review assistant. Analyze the current workspace and answer questions, but never modify files, run commands, access the network, or use external tools.",
      },
    };
    const session = conversation.copilotSessionId
      ? await this.client.resumeSession(conversation.copilotSessionId, config)
      : await this.client.createSession(config);
    const active: ActiveAsk = { session, model: conversation.model, sending: false };
    this.active.set(conversationId, active);
    if (conversation.copilotSessionId !== session.sessionId) {
      updateAskConversation(this.db, conversationId, { copilotSessionId: session.sessionId });
    }
    return active;
  }

  async send(
    conversationId: string,
    prompt: string,
    onDelta: (delta: string) => void,
  ): Promise<{ messageId: number; content: string; aborted: boolean }> {
    const active = await this.sessionFor(conversationId);
    if (active.sending) throw new Error("This /ask conversation is already responding");
    const assistant = insertAskMessage(this.db, {
      conversationId,
      role: "assistant",
      body: "",
      pending: true,
    });
    active.sending = true;
    let content = "";
    let aborted = false;
    const unsubscribeDelta = active.session.on("assistant.message_delta", (event) => {
      content += event.data.deltaContent;
      updateAskMessage(this.db, assistant.id, { body: content, pending: true });
      onDelta(event.data.deltaContent);
    });
    const unsubscribeFinal = active.session.on("assistant.message", (event) => {
      content = event.data.content;
    });
    const unsubscribeIdle = active.session.on("session.idle", (event) => {
      aborted = event.data.aborted === true;
    });

    try {
      const final = await active.session.sendAndWait({ prompt });
      if (final?.data.content) content = final.data.content;
      return { messageId: assistant.id, content, aborted };
    } finally {
      unsubscribeDelta();
      unsubscribeFinal();
      unsubscribeIdle();
      active.sending = false;
      updateAskMessage(this.db, assistant.id, { body: content, pending: false });
    }
  }

  async setModel(conversationId: string, model: string): Promise<void> {
    const conversation = getAskConversation(this.db, conversationId);
    if (!conversation) throw new Error("Unknown /ask conversation");
    const active = this.active.get(conversationId);
    if (active) {
      if (active.sending) throw new Error("Cannot change models while /ask is responding");
      await active.session.setModel(model);
      active.model = model;
    }
    updateAskConversation(this.db, conversationId, { model });
  }
}

function bodyString(value: unknown): string | undefined {
  return typeof value === "string" && value.trim() ? value : undefined;
}

function writeSse(res: Response, event: string, data: unknown): void {
  res.write(`event: ${event}\ndata: ${JSON.stringify(data)}\n\n`);
}

export function createAskRouter(options: AskRouterOptions): { router: Router; askService: AskService } {
  const router = express.Router();
  router.use(express.json({ limit: "1mb" }));
  const askService = new AskService(options.db, options.workspaceRoot);

  router.get("/api/ask/models", async (_req, res) => {
    try {
      res.json({ models: await askService.listModels() });
    } catch (error) {
      res.status(503).json({ error: `Copilot model catalog unavailable: ${String(error)}` });
    }
  });

  router.get("/api/ask/conversations", (req, res) => {
    const queueItemId = bodyString(req.query.queueItemId);
    res.json({ conversations: listAskConversations(options.db, queueItemId) });
  });

  router.post("/api/ask/conversations", async (req, res) => {
    const model = bodyString(req.body?.model);
    const queueItemId = bodyString(req.body?.queueItemId);
    try {
      if (model) {
        const available = await askService.listModels();
        if (!available.some((candidate) => candidate.id === model)) {
          res.status(400).json({ error: `Unknown Copilot model: ${model}` });
          return;
        }
      }
      res.status(201).json({ conversation: createAskConversation(options.db, { model, queueItemId }) });
    } catch (error) {
      res.status(503).json({ error: `Unable to initialize Copilot: ${String(error)}` });
    }
  });

  router.get("/api/ask/conversations/:id", (req, res) => {
    const conversation = getAskConversation(options.db, req.params.id);
    if (!conversation) {
      res.status(404).json({ error: "Unknown /ask conversation" });
      return;
    }
    res.json({ conversation, messages: listAskMessages(options.db, conversation.id) });
  });

  router.post("/api/ask/conversations/:id/model", async (req, res) => {
    const model = bodyString(req.body?.model);
    if (!model) {
      res.status(400).json({ error: "model is required" });
      return;
    }
    try {
      const models = await askService.listModels();
      if (!models.some((candidate) => candidate.id === model)) {
        res.status(400).json({ error: `Unknown Copilot model: ${model}` });
        return;
      }
      await askService.setModel(req.params.id, model);
      res.json({ conversation: getAskConversation(options.db, req.params.id) });
    } catch (error) {
      res.status(400).json({ error: String(error) });
    }
  });

  router.post("/api/ask/conversations/:id/cancel", async (req, res) => {
    try {
      res.json({ cancelled: await askService.abort(req.params.id) });
    } catch (error) {
      res.status(500).json({ error: `Unable to cancel /ask response: ${String(error)}` });
    }
  });

  router.post("/api/ask/conversations/:id/messages", async (req, res) => {
    const conversation = getAskConversation(options.db, req.params.id);
    const prompt = bodyString(req.body?.prompt);
    if (!conversation) {
      res.status(404).json({ error: "Unknown /ask conversation" });
      return;
    }
    if (!prompt) {
      res.status(400).json({ error: "prompt is required" });
      return;
    }

    insertAskMessage(options.db, { conversationId: conversation.id, role: "user", body: prompt });
    res.status(200);
    res.set({
      "Cache-Control": "no-cache, no-transform",
      Connection: "keep-alive",
      "Content-Type": "text/event-stream",
      "X-Accel-Buffering": "no",
    });
    res.flushHeaders();
    writeSse(res, "started", { conversationId: conversation.id });
    try {
      const result = await askService.send(conversation.id, prompt, (delta) => {
        writeSse(res, "delta", { delta });
      });
      writeSse(res, "done", result);
    } catch (error) {
      writeSse(res, "error", { error: String(error) });
    } finally {
      res.end();
    }
  });

  return { router, askService };
}
