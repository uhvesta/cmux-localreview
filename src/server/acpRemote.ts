import { Socket } from "node:net";
import { Readable, Writable } from "node:stream";

import {
  ClientSideConnection,
  ndJsonStream,
  type Client,
  type PromptResponse,
  type RequestPermissionRequest,
  type RequestPermissionResponse,
  type SessionNotification,
} from "@zed-industries/agent-client-protocol";

const PROTOCOL_VERSION = 1;
const CONNECT_TIMEOUT_MS = 10_000;

export type AcpTurnState = "connecting" | "idle" | "busy" | "error";

export interface AcpEndpoint {
  host: string;
  port: number;
  sessionId: string;
  /** Original agent cwd; required by ACP session/load on reconnect. */
  cwd?: string;
}

export function isLoopbackAcpHost(host: string): boolean {
  const normalized = host.trim().toLowerCase();
  return normalized === "localhost" || normalized === "::1" || normalized === "[::1]" || /^127(?:\.\d{1,3}){3}$/.test(normalized);
}

/** Validate before opening a socket; remote ACP uses `ssh -L` to loopback. */
export function parseLoopbackAcpEndpoint(input: { host?: unknown; port?: unknown; sessionId?: unknown; cwd?: unknown }): AcpEndpoint {
  const host = typeof input.host === "string" ? input.host.trim() : "";
  const port = typeof input.port === "number" ? input.port : Number.NaN;
  const sessionId = typeof input.sessionId === "string" ? input.sessionId.trim() : "";
  if (!isLoopbackAcpHost(host)) throw new Error("ACP host must be localhost, 127.0.0.0/8, or ::1 (use an SSH local forward for remote agents)");
  if (!Number.isInteger(port) || port < 1 || port > 65_535) throw new Error("ACP port must be an integer from 1 through 65535");
  if (!sessionId || sessionId.length > 2_048) throw new Error("ACP sessionId is required");
  const cwd = typeof input.cwd === "string" && input.cwd.trim() ? input.cwd.trim() : undefined;
  return { host, port, sessionId, cwd };
}

function allowRequestedPermission(request: RequestPermissionRequest): RequestPermissionResponse {
  // This connection exists solely to deliver an explicit reviewer instruction
  // to the original agent. ACP still asks for every consequential tool call;
  // select its normal allow-once option rather than pretending this is /btw's
  // read-only question channel.
  const option = request.options.find((entry) => entry.kind.startsWith("allow"));
  return option ? { outcome: { outcome: "selected", optionId: option.optionId } } : { outcome: { outcome: "cancelled" } };
}

function connect(endpoint: AcpEndpoint, timeoutMs: number): Promise<Socket> {
  return new Promise((resolve, reject) => {
    const socket = new Socket();
    const timer = setTimeout(() => {
      socket.destroy();
      reject(new Error(`Timed out connecting to ACP at ${endpoint.host}:${endpoint.port}`));
    }, timeoutMs);
    const fail = (error: Error) => { clearTimeout(timer); reject(error); };
    socket.once("error", fail);
    socket.connect({ host: endpoint.host.replace(/^\[|\]$/g, ""), port: endpoint.port }, () => {
      clearTimeout(timer);
      socket.off("error", fail);
      socket.setNoDelay(true);
      resolve(socket);
    });
  });
}

/**
 * Client for a Copilot CLI ACP TCP listener. It attaches to an existing
 * sessionId and never calls newSession, preserving the live agent context.
 */
export class AcpRemoteSession {
  readonly endpoint: AcpEndpoint;
  private socket: Socket;
  private conn: ClientSideConnection;
  private busy = false;
  private disposed = false;
  private readonly onState: (state: AcpTurnState, error?: unknown) => void;

  private constructor(endpoint: AcpEndpoint, socket: Socket, conn: ClientSideConnection, onState: (state: AcpTurnState, error?: unknown) => void) {
    this.endpoint = endpoint;
    this.socket = socket;
    this.conn = conn;
    this.onState = onState;
  }

  static async connect(
    endpoint: AcpEndpoint,
    options: { onState: (state: AcpTurnState, error?: unknown) => void; onUpdate?: (notification: SessionNotification) => void; timeoutMs?: number },
  ): Promise<AcpRemoteSession> {
    options.onState("connecting");
    const socket = await connect(endpoint, options.timeoutMs ?? CONNECT_TIMEOUT_MS);
    const stream = ndJsonStream(
      Writable.toWeb(socket) as WritableStream<Uint8Array>,
      Readable.toWeb(socket) as ReadableStream<Uint8Array>,
    );
    const client: Client = {
      sessionUpdate: async (notification) => {
        // Session updates can arrive while a turn is active; the prompt
        // response is authoritative for returning to idle.
        options.onUpdate?.(notification);
      },
      requestPermission: async (request) => allowRequestedPermission(request),
    };
    const conn = new ClientSideConnection(() => client, stream);
    try {
      const initialized = await conn.initialize({ protocolVersion: PROTOCOL_VERSION, clientCapabilities: { fs: {} } });
      // A TCP listener accepts many client connections, but Copilot scopes a
      // live session to each connection until session/load is called. Prompting
      // with just the retained ID therefore fails with "Session not found" on
      // a fresh feedback connection. Rehydrate it when the agent advertises
      // the standard loadSession capability; the fallback keeps older ACP
      // agents (and simple fixtures) compatible.
      if (initialized.agentCapabilities?.loadSession) {
        await conn.loadSession({
          sessionId: endpoint.sessionId,
          cwd: endpoint.cwd ?? process.cwd(),
          mcpServers: [],
        });
      }
    } catch (error) {
      socket.destroy();
      options.onState("error", error);
      throw error;
    }
    const session = new AcpRemoteSession(endpoint, socket, conn, options.onState);
    socket.once("close", () => {
      if (!session.disposed) options.onState("error", new Error("ACP connection closed"));
    });
    options.onState("idle");
    return session;
  }

  get isBusy(): boolean { return this.busy; }

  async prompt(text: string): Promise<PromptResponse> {
    if (this.disposed) throw new Error("ACP connection is closed");
    if (this.busy) throw new Error("ACP session is already processing a prompt");
    this.busy = true;
    this.onState("busy");
    try {
      return await this.conn.prompt({ sessionId: this.endpoint.sessionId, prompt: [{ type: "text", text }] });
    } catch (error) {
      this.onState("error", error);
      throw error;
    } finally {
      this.busy = false;
      if (!this.disposed) this.onState("idle");
    }
  }

  async cancel(): Promise<void> {
    if (this.disposed) return;
    await this.conn.cancel({ sessionId: this.endpoint.sessionId });
  }

  dispose(): void {
    this.disposed = true;
    this.socket.destroy();
  }
}
