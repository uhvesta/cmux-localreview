import { createCmuxService, createSocketConnector } from "../../vendor/cmux-hub/cmux.ts";

/** Context attached to a queue item/snapshot. It deliberately contains no terminal transcript. */
export interface SubmissionProvenance {
  version: 1;
  capturedAt: string;
  workspacePath: string;
  caller: {
    cwd: string;
    cmuxSurfaceId: string | null;
    cmuxWorkspaceId: string | null;
  };
  cmux: {
    available: boolean;
    surfaces: CmuxSurfaceSummary[];
    error?: string;
  };
  /** Optional caller-provided, redacted metadata such as an originating ACP agent id. */
  supplied?: Record<string, unknown>;
}

export interface CmuxSurfaceSummary {
  id: string;
  workspaceId?: string;
  title?: string;
  focused?: boolean;
}

const SENSITIVE_KEY = /(?:authorization|cookie|password|secret|token|api[_-]?key)/i;
const MAX_CONTEXT_BYTES = 64 * 1024;

function stringValue(value: unknown): string | undefined {
  return typeof value === "string" && value.trim() ? value.trim() : undefined;
}

/** Remove values that could accidentally carry credentials before persistence. */
export function redactSubmissionMetadata(value: unknown, depth = 0): unknown {
  if (depth > 8 || value === null || typeof value === "boolean" || typeof value === "number") return value;
  if (typeof value === "string") return value.length > 4096 ? `${value.slice(0, 4096)}…` : value;
  if (Array.isArray(value)) return value.slice(0, 100).map((entry) => redactSubmissionMetadata(entry, depth + 1));
  if (typeof value === "object") {
    const output: Record<string, unknown> = {};
    for (const [key, entry] of Object.entries(value as Record<string, unknown>).slice(0, 100)) {
      output[key] = SENSITIVE_KEY.test(key) ? "[redacted]" : redactSubmissionMetadata(entry, depth + 1);
    }
    return output;
  }
  return String(value);
}

function surfaceList(value: unknown): unknown[] {
  if (Array.isArray(value)) return value;
  if (value && typeof value === "object") {
    const record = value as Record<string, unknown>;
    for (const key of ["surfaces", "items", "data", "result"]) {
      if (Array.isArray(record[key])) return record[key] as unknown[];
    }
  }
  return [];
}

/**
 * cmux's socket response shape has changed across releases, so decode only
 * common metadata fields and leave all terminal content untouched.
 */
export function summarizeCmuxSurfaces(value: unknown): CmuxSurfaceSummary[] {
  const summaries: CmuxSurfaceSummary[] = [];
  for (const raw of surfaceList(value).slice(0, 100)) {
    if (!raw || typeof raw !== "object") continue;
    const item = raw as Record<string, unknown>;
    const id = stringValue(item.surface_id) ?? stringValue(item.surfaceId) ?? stringValue(item.id);
    if (!id) continue;
    const workspaceId = stringValue(item.workspace_id) ?? stringValue(item.workspaceId);
    const title = stringValue(item.title) ?? stringValue(item.name) ?? stringValue(item.command);
    const focused = typeof item.focused === "boolean" ? item.focused : undefined;
    summaries.push({ id, ...(workspaceId ? { workspaceId } : {}), ...(title ? { title } : {}), ...(focused === undefined ? {} : { focused }) });
  }
  return summaries;
}

function compactSupplied(value: unknown): Record<string, unknown> | undefined {
  if (!value || typeof value !== "object" || Array.isArray(value)) return undefined;
  const redacted = redactSubmissionMetadata(value) as Record<string, unknown>;
  return JSON.stringify(redacted).length <= MAX_CONTEXT_BYTES ? redacted : { truncated: true };
}

export async function captureSubmissionProvenance(
  workspacePath: string,
  supplied?: unknown,
): Promise<SubmissionProvenance> {
  const caller = {
    cwd: process.cwd(),
    cmuxSurfaceId: stringValue(process.env.CMUX_SURFACE_ID) ?? null,
    cmuxWorkspaceId: stringValue(process.env.CMUX_WORKSPACE_ID) ?? null,
  };
  let cmux: SubmissionProvenance["cmux"];
  try {
    const raw = await Promise.race([
      createCmuxService(createSocketConnector()).listSurfaces(),
      new Promise<never>((_, reject) => setTimeout(() => reject(new Error("cmux discovery timed out")), 750)),
    ]);
    cmux = { available: true, surfaces: summarizeCmuxSurfaces(raw) };
  } catch (error) {
    // cmux is optional. A queue submission must work from a regular terminal.
    cmux = { available: false, surfaces: [], error: error instanceof Error ? error.message.slice(0, 300) : "cmux unavailable" };
  }
  const compact = compactSupplied(supplied);
  return { version: 1, capturedAt: new Date().toISOString(), workspacePath, caller, cmux, ...(compact ? { supplied: compact } : {}) };
}
