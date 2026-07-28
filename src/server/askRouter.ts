import { realpathSync } from "node:fs";
import { relative, resolve } from "node:path";
import type { Database } from "bun:sqlite";
import { CopilotClient, RuntimeConnection, type CopilotSession, type ModelInfo, type PermissionRequest } from "@github/copilot-sdk";
import express, { type Response, type Router } from "express";

import {
  createAskConversation,
  archiveActiveAskConversations,
  getActiveAskConversation,
  getAskConversation,
  insertAskMessage,
  listAskConversations,
  listAskMessages,
  resumeAskConversation,
  updateAskConversation,
  updateAskMessage,
} from "./askStore.ts";
import {
  createQuestionSet,
  deleteQuestionSet,
  getQuestionSet,
  listQuestionSets,
  updateQuestionSet,
} from "./askQuestionSetStore.ts";
import type { AskLocation } from "./askStore.ts";

type ReasoningEffort = "low" | "medium" | "high" | "xhigh";
type ContextTier = "default" | "long_context";
type ActiveAsk = { session: CopilotSession; model: string | null; sending: boolean };

export interface AskRouterOptions {
  db: Database;
  workspaceRoot: string;
  /** The review round currently open in the workspace reviewer. */
  getReviewSessionId?: () => number;
  /** Maps the mounted diff application's public repo id to its actual root. */
  repoRoots?: ReadonlyMap<string, string>;
}

/**
 * Keep the human's saved transcript terse, but give Copilot the exact anchor
 * when a question came from a line/range in the reviewer. A persistent inline
 * conversation repeats its anchor on follow-up turns so the model never has
 * to infer what "this" refers to from an earlier answer.
 */
export function formatAskPrompt(
  workspaceRoot: string,
  question: string,
  location?: AskLocation,
  repoRoots?: ReadonlyMap<string, string>,
): string {
  if (!location?.filePath) return question;
  const repositoryRoot = location.repoId ? repoRoots?.get(location.repoId) : undefined;
  const root = resolve(repositoryRoot ?? workspaceRoot);
  const candidatePath = resolve(root, location.filePath);
  const candidateRelative = relative(root, candidatePath);
  const workspaceRelative = relative(workspaceRoot, candidatePath);
  const withinRoot = candidateRelative === "" || (!candidateRelative.startsWith("..") && !candidateRelative.includes(`${process.platform === "win32" ? "\\\\" : "/"}..`));
  const withinWorkspace = workspaceRelative === "" || (!workspaceRelative.startsWith("..") && !workspaceRelative.includes(`${process.platform === "win32" ? "\\\\" : "/"}..`));
  const lineRange = location.startLine
    ? location.endLine && location.endLine !== location.startLine
      ? `L${location.startLine}-L${location.endLine}`
      : `L${location.startLine}`
    : "unspecified line";
  return [
    "This is an inline code-review question. Treat the following location as authoritative context; answer the question without making edits.",
    `Workspace root: ${workspaceRoot}`,
    `Repository id: ${location.repoId ?? "current workspace repository"}`,
    `Repository root: ${repositoryRoot ?? workspaceRoot}`,
    `File (repository-relative): ${location.filePath}`,
    `File (workspace-relative): ${withinWorkspace ? workspaceRelative || "." : location.filePath}`,
    `File (absolute): ${withinRoot && withinWorkspace ? candidatePath : "unresolved — do not read outside this workspace"}`,
    `Side: ${location.side ?? "current"}`,
    `Lines: ${lineRange}`,
    location.selectedCode ? `Selected code:\n\`\`\`\n${location.selectedCode}\n\`\`\`` : "",
    "Question:",
    question,
  ].filter(Boolean).join("\n\n");
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
  // The browser receives SSE `started` before a cold Copilot runtime has
  // necessarily created its session. Remember an immediate Stop so it wins
  // once initialization completes instead of misleadingly returning false.
  private readonly pendingAborts = new Set<string>();

  constructor(private readonly db: Database, workspaceRoot: string, private readonly repoRoots?: ReadonlyMap<string, string>) {
    this.workspaceRoot = resolve(workspaceRoot);
    this.realWorkspaceRoot = realpathSync(this.workspaceRoot);
    this.client = new CopilotClient({
      workingDirectory: this.realWorkspaceRoot,
      // Use the user's installed Copilot CLI rather than the SDK's bundled
      // runtime. The installed CLI is the process that owns the user's normal
      // keychain-backed login, which makes fresh SDK /ask sessions behave the
      // same way as `copilot -p` and avoids a second, unauthenticated runtime.
      connection: RuntimeConnection.forStdio({ path: process.env.COPILOT_CLI_PATH ?? "copilot" }),
      // Do not give the SDK a private COPILOT_HOME.  Copilot CLI credentials
      // live in the user's normal ~/.copilot directory; a private base
      // directory looks like a clean installation and makes an otherwise
      // authenticated CLI report "Not authenticated" from /ask.  Conversation
      // identity is persisted in our own database, so sharing that runtime
      // home does not merge localreview conversations.
      useLoggedInUser: true,
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
    this.pendingAborts.clear();
    await this.client.stop();
  }

  async abort(conversationId: string): Promise<boolean> {
    const active = this.active.get(conversationId);
    if (!active?.sending) {
      this.pendingAborts.add(conversationId);
      return true;
    }
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
      reasoningEffort: conversation.reasoningEffort ?? undefined,
      contextTier: conversation.contextTier ?? undefined,
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
    location?: AskLocation,
  ): Promise<{ messageId: number; content: string; aborted: boolean }> {
    const conversation = getAskConversation(this.db, conversationId);
    if (!conversation) throw new Error("Unknown /ask conversation");
    const active = await this.sessionFor(conversationId);
    if (this.pendingAborts.delete(conversationId)) {
      const assistant = insertAskMessage(this.db, {
        conversationId,
        role: "assistant",
        body: "",
        pending: false,
        location,
      });
      return { messageId: assistant.id, content: "", aborted: true };
    }
    if (active.sending) throw new Error("This /ask conversation is already responding");
    const assistant = insertAskMessage(this.db, {
      conversationId,
      role: "assistant",
      body: "",
      pending: true,
      location,
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
      const final = await active.session.sendAndWait({
        prompt: formatAskPrompt(this.realWorkspaceRoot, prompt, location ?? conversation.context ?? undefined, this.repoRoots),
      });
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

  async setModel(conversationId: string, model: string, settings: { reasoningEffort?: ReasoningEffort; contextTier?: ContextTier } = {}): Promise<void> {
    const conversation = getAskConversation(this.db, conversationId);
    if (!conversation) throw new Error("Unknown /ask conversation");
    const active = this.active.get(conversationId);
    if (active) {
      if (active.sending) throw new Error("Cannot change models while /ask is responding");
      await active.session.setModel(model, settings);
      active.model = model;
    }
    updateAskConversation(this.db, conversationId, {
      model,
      reasoningEffort: settings.reasoningEffort ?? null,
      contextTier: settings.contextTier ?? null,
    });
  }
}

function bodyString(value: unknown): string | undefined {
  return typeof value === "string" && value.trim() ? value : undefined;
}

function reasoningEffort(value: unknown): ReasoningEffort | undefined {
  return value === "low" || value === "medium" || value === "high" || value === "xhigh" ? value : undefined;
}

function contextTier(value: unknown): ContextTier | undefined {
  return value === "default" || value === "long_context" ? value : undefined;
}

function validateModelSettings(models: ModelInfo[], model: string, effort?: ReasoningEffort): string | undefined {
  const selected = models.find((candidate) => candidate.id === model);
  if (!selected) return `Unknown Copilot model: ${model}`;
  if (effort && (!selected.capabilities?.supports?.reasoningEffort || !selected.supportedReasoningEfforts?.includes(effort))) {
    return `${selected.name ?? selected.id} does not support ${effort} reasoning.`;
  }
  return undefined;
}

const MAX_QUESTION_SET_QUESTIONS = 100;
const MAX_QUESTION_BODY_LENGTH = 12_000;

/** Converts an ordered checklist into one deliberate, review-friendly turn. */
export function combinedQuestionPrompt(questions: string[]): string {
  return [
    "Please answer these review questions in order. Label each answer with its question number.",
    "",
    ...questions.map((question, index) => `${index + 1}. ${question}`),
  ].join("\n");
}

function questionBodies(value: unknown): string[] | undefined {
  if (!Array.isArray(value) || value.length > MAX_QUESTION_SET_QUESTIONS) return undefined;
  const questions = value.map((question) => (typeof question === "string" ? question.trim() : ""));
  if (questions.some((question) => !question || question.length > MAX_QUESTION_BODY_LENGTH)) return undefined;
  return questions;
}

function askLocation(value: unknown): AskLocation | undefined {
  if (!value || typeof value !== "object" || Array.isArray(value)) return undefined;
  const source = value as Record<string, unknown>;
  const text = (key: string, max = 20_000) => typeof source[key] === "string" && source[key].length <= max ? source[key] : undefined;
  const line = (key: string) => Number.isInteger(source[key]) && (source[key] as number) > 0 ? source[key] as number : undefined;
  const side = source.side === "base" || source.side === "current" ? source.side : undefined;
  const location: AskLocation = {
    repoId: text("repoId", 512), filePath: text("filePath", 4_096), side,
    startLine: line("startLine"), endLine: line("endLine"), selectedCode: text("selectedCode"),
  };
  if (location.endLine && (!location.startLine || location.endLine < location.startLine)) return undefined;
  return Object.values(location).some((part) => part !== undefined) ? location : undefined;
}

function locationError(workspaceRoot: string, repoRoots: ReadonlyMap<string, string> | undefined, location?: AskLocation): string | undefined {
  if (!location?.filePath) return undefined;
  const repositoryRoot = location.repoId ? repoRoots?.get(location.repoId) : undefined;
  if (location.repoId && !repositoryRoot) return "Unknown repository for this inline /ask location";
  const root = resolve(repositoryRoot ?? workspaceRoot);
  const absolutePath = resolve(root, location.filePath);
  const workspaceRelative = relative(workspaceRoot, absolutePath);
  const repositoryRelative = relative(root, absolutePath);
  const within = (value: string) => value === "" || (!value.startsWith("..") && !value.includes(`${process.platform === "win32" ? "\\\\" : "/"}..`));
  return within(workspaceRelative) && within(repositoryRelative)
    ? undefined
    : "Inline /ask file path must remain inside its repository and workspace";
}

function isHistoricalConversation(conversation: ReturnType<typeof getAskConversation>, activeReviewSessionId: number): boolean {
  return Boolean(conversation && (conversation.archivedAt !== null || (conversation.reviewSessionId !== null && conversation.reviewSessionId !== activeReviewSessionId)));
}

function writeSse(res: Response, event: string, data: unknown): void {
  res.write(`event: ${event}\ndata: ${JSON.stringify(data)}\n\n`);
}

export function createAskRouter(options: AskRouterOptions): { router: Router; askService: AskService } {
  const router = express.Router();
  router.use(express.json({ limit: "1mb" }));
  const askService = new AskService(options.db, options.workspaceRoot, options.repoRoots);
  const reviewSessionId = () => options.getReviewSessionId?.() ?? 0;

  router.get("/api/ask/models", async (_req, res) => {
    try {
      res.json({ models: await askService.listModels() });
    } catch (error) {
      res.status(503).json({ error: `Copilot model catalog unavailable: ${String(error)}` });
    }
  });

  router.get("/api/ask/conversations", (req, res) => {
    const queueItemId = bodyString(req.query.queueItemId);
    const includeHistory = req.query.history === "1" || req.query.history === "true";
    const activeReviewSessionId = reviewSessionId();
    res.json({
      activeReviewSessionId,
      conversations: listAskConversations(options.db, {
        queueItemId,
        reviewSessionId: includeHistory || !activeReviewSessionId ? undefined : activeReviewSessionId,
        includeArchived: includeHistory,
      }),
    });
  });

  router.get("/api/ask/question-sets", (_req, res) => {
    res.json({ questionSets: listQuestionSets(options.db) });
  });

  router.post("/api/ask/question-sets", (req, res) => {
    const name = bodyString(req.body?.name);
    const hasQuestions = Object.hasOwn(req.body ?? {}, "questions");
    const questions = hasQuestions ? questionBodies(req.body?.questions) : [];
    if (!name) {
      res.status(400).json({ error: "name is required" });
      return;
    }
    if (!questions) {
      res.status(400).json({ error: `questions must be an ordered array of 1–${MAX_QUESTION_SET_QUESTIONS} non-empty strings` });
      return;
    }
    res.status(201).json({ questionSet: createQuestionSet(options.db, { name, questions }) });
  });

  router.get("/api/ask/question-sets/:id", (req, res) => {
    const questionSet = getQuestionSet(options.db, req.params.id);
    if (!questionSet) {
      res.status(404).json({ error: "Unknown question set" });
      return;
    }
    res.json({ questionSet });
  });

  router.put("/api/ask/question-sets/:id", (req, res) => {
    const hasName = Object.hasOwn(req.body ?? {}, "name");
    const hasQuestions = Object.hasOwn(req.body ?? {}, "questions");
    const name = hasName ? bodyString(req.body?.name) : undefined;
    const questions = hasQuestions ? questionBodies(req.body?.questions) : undefined;
    if ((hasName && !name) || (hasQuestions && !questions)) {
      res.status(400).json({ error: "name and questions, when supplied, must be valid" });
      return;
    }
    if (!hasName && !hasQuestions) {
      res.status(400).json({ error: "name or questions is required" });
      return;
    }
    const questionSet = updateQuestionSet(options.db, req.params.id, { name, questions });
    if (!questionSet) {
      res.status(404).json({ error: "Unknown question set" });
      return;
    }
    res.json({ questionSet });
  });

  router.delete("/api/ask/question-sets/:id", (req, res) => {
    if (!deleteQuestionSet(options.db, req.params.id)) {
      res.status(404).json({ error: "Unknown question set" });
      return;
    }
    res.status(204).end();
  });

  router.post("/api/ask/conversations", async (req, res) => {
    const model = bodyString(req.body?.model);
    const effort = reasoningEffort(req.body?.reasoningEffort);
    const tier = contextTier(req.body?.contextTier);
    const queueItemId = bodyString(req.body?.queueItemId);
    const context = askLocation(req.body?.context);
    const invalidLocation = locationError(options.workspaceRoot, options.repoRoots, context);
    if (invalidLocation) {
      res.status(400).json({ error: invalidLocation });
      return;
    }
    try {
      if (model) {
        const available = await askService.listModels();
        const validation = validateModelSettings(available, model, effort);
        if (validation) {
          res.status(400).json({ error: validation });
          return;
        }
      }
      res.status(201).json({ conversation: createAskConversation(options.db, { model, reasoningEffort: effort, contextTier: tier, queueItemId, context, reviewSessionId: reviewSessionId() || undefined }) });
    } catch (error) {
      res.status(503).json({ error: `Unable to initialize Copilot: ${String(error)}` });
    }
  });

  // Every inline question shares one durable review conversation. Location
  // remains on each message (for exact inline rendering), while one Copilot
  // session retains the context of questions asked anywhere in the review.
  router.post("/api/ask/inline-conversations", async (req, res) => {
    const context = askLocation(req.body?.context);
    const model = bodyString(req.body?.model);
    if (!context?.filePath || !context.startLine) {
      res.status(400).json({ error: "An inline /ask conversation needs filePath and startLine" });
      return;
    }
    const invalidLocation = locationError(options.workspaceRoot, options.repoRoots, context);
    if (invalidLocation) {
      res.status(400).json({ error: invalidLocation });
      return;
    }
    const activeReviewSessionId = reviewSessionId();
    const existing = activeReviewSessionId
      ? getActiveAskConversation(options.db, activeReviewSessionId)
      : listAskConversations(options.db).find((conversation) => !conversation.context?.filePath);
    if (existing) {
      res.json({ conversation: existing, reused: true, shared: true });
      return;
    }
    try {
      if (model) {
        const available = await askService.listModels();
        const validation = validateModelSettings(available, model);
        if (validation) {
          res.status(400).json({ error: validation });
          return;
        }
      }
      res.status(201).json({ conversation: createAskConversation(options.db, { model, reviewSessionId: activeReviewSessionId || undefined }), reused: false, shared: true });
    } catch (error) {
      res.status(503).json({ error: `Unable to initialize Copilot: ${String(error)}` });
    }
  });

  // Transcript reads are deliberately side-effect-free. In particular, page
  // reloads, reopening an inline thread, and polling the side panel must not
  // create/resume a Copilot runtime or replay any prior prompt.
  router.get("/api/ask/conversations/:id", (req, res) => {
    const conversation = getAskConversation(options.db, req.params.id);
    if (!conversation) {
      res.status(404).json({ error: "Unknown /ask conversation" });
      return;
    }
    res.json({ conversation, messages: listAskMessages(options.db, conversation.id) });
  });

  // Starts a clean Copilot context for this review round. The previous
  // transcript remains durable and browsable, but is no longer the target of
  // new inline /ask comments.
  router.post("/api/ask/conversations/fresh", async (req, res) => {
    const model = bodyString(req.body?.model);
    const effort = reasoningEffort(req.body?.reasoningEffort);
    const tier = contextTier(req.body?.contextTier);
    const activeReviewSessionId = reviewSessionId();
    try {
      if (model) {
        const models = await askService.listModels();
        const validation = validateModelSettings(models, model, effort);
        if (validation) { res.status(400).json({ error: validation }); return; }
      }
      if (activeReviewSessionId) archiveActiveAskConversations(options.db, activeReviewSessionId);
      res.status(201).json({
        conversation: createAskConversation(options.db, {
          model,
          reasoningEffort: effort,
          contextTier: tier,
          reviewSessionId: activeReviewSessionId || undefined,
        }),
      });
    } catch (error) {
      res.status(503).json({ error: `Unable to initialize Copilot: ${String(error)}` });
    }
  });

  router.post("/api/ask/conversations/:id/resume", (req, res) => {
    const conversation = getAskConversation(options.db, req.params.id);
    if (!conversation) { res.status(404).json({ error: "Unknown /ask conversation" }); return; }
    if (conversation.reviewSessionId !== reviewSessionId()) {
      res.status(409).json({ error: "This Ask session belongs to a previous review round. It remains read-only history." });
      return;
    }
    res.json({ conversation: resumeAskConversation(options.db, conversation.id) });
  });

  router.post("/api/ask/conversations/:id/model", async (req, res) => {
    const model = bodyString(req.body?.model);
    if (!model) {
      res.status(400).json({ error: "model is required" });
      return;
    }
    if (isHistoricalConversation(getAskConversation(options.db, req.params.id), reviewSessionId())) {
      res.status(409).json({ error: "This Ask session is historical and read-only." });
      return;
    }
    try {
      const models = await askService.listModels();
      const validation = validateModelSettings(models, model);
      if (validation) {
        res.status(400).json({ error: validation });
        return;
      }
      await askService.setModel(req.params.id, model);
      res.json({ conversation: getAskConversation(options.db, req.params.id) });
    } catch (error) {
      res.status(400).json({ error: String(error) });
    }
  });

  router.post("/api/ask/conversations/:id/settings", async (req, res) => {
    const conversation = getAskConversation(options.db, req.params.id);
    const model = bodyString(req.body?.model) ?? conversation?.model ?? "auto";
    if (!conversation) {
      res.status(404).json({ error: "Unknown /ask conversation" });
      return;
    }
    if (isHistoricalConversation(conversation, reviewSessionId())) {
      res.status(409).json({ error: "This Ask session is historical and read-only." });
      return;
    }
    const effort = Object.hasOwn(req.body ?? {}, "reasoningEffort")
      ? reasoningEffort(req.body?.reasoningEffort)
      : conversation.reasoningEffort ?? undefined;
    const tier = Object.hasOwn(req.body ?? {}, "contextTier")
      ? contextTier(req.body?.contextTier)
      : conversation.contextTier ?? undefined;
    try {
      const models = await askService.listModels();
      const validation = validateModelSettings(models, model, effort);
      if (validation) {
        res.status(400).json({ error: validation });
        return;
      }
      await askService.setModel(req.params.id, model, { reasoningEffort: effort, contextTier: tier });
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
    const location = askLocation(req.body?.location);
    if (!conversation) {
      res.status(404).json({ error: "Unknown /ask conversation" });
      return;
    }
    if (isHistoricalConversation(conversation, reviewSessionId())) {
      res.status(409).json({ error: "This Ask session is historical. Start a fresh session or resume it before asking again." });
      return;
    }
    const invalidLocation = locationError(options.workspaceRoot, options.repoRoots, location);
    if (invalidLocation) {
      res.status(400).json({ error: invalidLocation });
      return;
    }
    if (!prompt) {
      res.status(400).json({ error: "prompt is required" });
      return;
    }

    insertAskMessage(options.db, { conversationId: conversation.id, role: "user", body: prompt, location });
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
      const result = await askService.send(
        conversation.id,
        prompt,
        (delta) => { writeSse(res, "delta", { delta }); },
        location,
      );
      writeSse(res, "done", result);
    } catch (error) {
      writeSse(res, "error", { error: String(error) });
    } finally {
      res.end();
    }
  });

  router.post("/api/ask/question-sets/:id/send", async (req, res) => {
    const conversationId = bodyString(req.body?.conversationId);
    const mode = req.body?.mode === "sequential" ? "sequential" : req.body?.mode === "combined" ? "combined" : undefined;
    const questionSet = getQuestionSet(options.db, req.params.id);
    const conversation = conversationId ? getAskConversation(options.db, conversationId) : undefined;
    if (!conversationId || !mode) {
      res.status(400).json({ error: "conversationId and mode ('combined' or 'sequential') are required" });
      return;
    }
    if (!questionSet) {
      res.status(404).json({ error: "Unknown question set" });
      return;
    }
    if (!conversation) {
      res.status(404).json({ error: "Unknown /ask conversation" });
      return;
    }
    if (isHistoricalConversation(conversation, reviewSessionId())) {
      res.status(409).json({ error: "This Ask session is historical. Start a fresh session or resume it before asking again." });
      return;
    }
    const questions = questionSet.questions.map((question) => question.body);
    if (questions.length === 0) {
      res.status(400).json({ error: "Question sets need at least one question before sending" });
      return;
    }

    const prompts = mode === "combined" ? [combinedQuestionPrompt(questions)] : questions;
    res.status(200);
    res.set({
      "Cache-Control": "no-cache, no-transform",
      Connection: "keep-alive",
      "Content-Type": "text/event-stream",
      "X-Accel-Buffering": "no",
    });
    res.flushHeaders();
    writeSse(res, "started", { conversationId: conversation.id, questionSetId: questionSet.id, mode, count: prompts.length });
    try {
      for (const [index, prompt] of prompts.entries()) {
        insertAskMessage(options.db, { conversationId: conversation.id, role: "user", body: prompt });
        writeSse(res, "question_started", { index, prompt });
        const result = await askService.send(conversation.id, prompt, (delta) => {
          writeSse(res, "delta", { index, delta });
        });
        writeSse(res, "question_done", { index, ...result });
      }
      writeSse(res, "done", { conversationId: conversation.id, count: prompts.length });
    } catch (error) {
      writeSse(res, "error", { error: String(error) });
    } finally {
      res.end();
    }
  });

  return { router, askService };
}
