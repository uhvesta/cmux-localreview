import { mkdirSync, readFileSync, writeFileSync } from "node:fs";
import { dirname, join } from "node:path";
import open from "open";

import { localreviewDataDir } from "./daemonPaths.ts";
import { createSystemSecretStore, type SecretStore } from "./secretStore.ts";

export type GitHubCapability = "read" | "write" | "copilot";
const CAPABILITIES: GitHubCapability[] = ["read", "write", "copilot"];
const SERVICE = "cmux-localreview.github-app";

export interface CapabilityStatus {
  configured: boolean;
  authenticated: boolean;
  login?: string;
  error?: string;
  loginState: "idle" | "waiting" | "succeeded" | "failed";
  message?: string;
}

export interface GitHubAuthStatus {
  provider: "github-app-device-flow";
  capabilities: Record<GitHubCapability, CapabilityStatus>;
  /** Never includes a token, client secret, device code, or refresh token. */
}

interface AppConfig { clientIds: Partial<Record<GitHubCapability, string>>; }
interface TokenRecord { accessToken: string; refreshToken?: string; expiresAt?: number; login?: string; clientId?: string; }
interface PendingLogin { deviceCode: string; clientId: string; expiresAt: number; intervalMs: number; userCode: string; verificationUri: string; }
type FetchLike = typeof fetch;

function configPath(): string { return join(localreviewDataDir(), "github-apps.json"); }
function account(capability: GitHubCapability): string { return `github.com:${capability}`; }
function concise(value: string, max = 500): string { return value.replace(/\s+/g, " ").trim().slice(0, max); }

function readConfig(path = configPath()): AppConfig {
  try {
    const parsed = JSON.parse(readFileSync(path, "utf8")) as AppConfig;
    return { clientIds: parsed.clientIds ?? {} };
  } catch { return { clientIds: {} }; }
}

function writeConfig(config: AppConfig, path = configPath()): void {
  mkdirSync(dirname(path), { recursive: true, mode: 0o700 });
  writeFileSync(path, `${JSON.stringify(config, null, 2)}\n`, { encoding: "utf8", mode: 0o600 });
}

async function json(response: Response): Promise<Record<string, unknown>> {
  const value = await response.json().catch(() => ({}));
  return value && typeof value === "object" ? value as Record<string, unknown> : {};
}

/**
 * Dedicated GitHub App device-flow authority. Each capability should use a
 * separate GitHub App registration with only its required fine-grained
 * permissions. There is intentionally no gh, PAT, environment-token, or
 * Copilot-CLI credential fallback.
 */
export class GitHubAuthService {
  private config: AppConfig;
  private readonly pending = new Map<GitHubCapability, PendingLogin>();
  private readonly state = new Map<GitHubCapability, CapabilityStatus["loginState"]>();
  private readonly messages = new Map<GitHubCapability, string>();

  constructor(
    private readonly secrets: SecretStore = createSystemSecretStore(),
    private readonly fetcher: FetchLike = fetch,
    private readonly launch: (url: string) => Promise<unknown> = (url) => open(url),
    private readonly configFile = configPath(),
  ) { this.config = readConfig(configFile); }

  async configure(capability: GitHubCapability, clientId: string): Promise<void> {
    if (!CAPABILITIES.includes(capability)) throw new Error("Unknown GitHub capability");
    if (!/^Iv1\.[A-Za-z0-9]+$/.test(clientId.trim()) && clientId.trim().length < 8) throw new Error("GitHub App client ID looks invalid");
    const normalized = clientId.trim();
    const changed = this.config.clientIds[capability] !== normalized;
    this.config.clientIds[capability] = normalized;
    writeConfig(this.config, this.configFile);
    // A token issued to a different App registration must never inherit this
    // capability merely because its public client id was later replaced.
    if (changed) await this.secrets.remove(SERVICE, account(capability));
    this.pending.delete(capability);
    this.state.set(capability, "idle"); this.messages.delete(capability);
  }

  private clientId(capability: GitHubCapability): string {
    const id = this.config.clientIds[capability];
    if (!id) throw new Error(`Configure the ${capability} GitHub App client ID before connecting it.`);
    return id;
  }

  private async readToken(capability: GitHubCapability): Promise<TokenRecord | undefined> {
    const raw = await this.secrets.get(SERVICE, account(capability));
    if (!raw) return undefined;
    try {
      const value = JSON.parse(raw) as TokenRecord;
      return typeof value.accessToken === "string" ? value : undefined;
    } catch { return undefined; }
  }

  private async writeToken(capability: GitHubCapability, token: TokenRecord): Promise<void> {
    await this.secrets.set(SERVICE, account(capability), JSON.stringify(token));
  }

  async start(capability: GitHubCapability): Promise<{ userCode: string; verificationUri: string; expiresAt: number }> {
    const clientId = this.clientId(capability);
    const response = await this.fetcher("https://github.com/login/device/code", {
      method: "POST", headers: { Accept: "application/json", "Content-Type": "application/x-www-form-urlencoded" },
      body: new URLSearchParams({ client_id: clientId }),
    });
    const body = await json(response);
    if (!response.ok || typeof body.device_code !== "string" || typeof body.user_code !== "string" || typeof body.verification_uri !== "string") {
      throw new Error(concise(String(body.error_description ?? body.error ?? `GitHub device authorization failed (${response.status})`)));
    }
    const pending: PendingLogin = {
      deviceCode: body.device_code, clientId, userCode: body.user_code, verificationUri: body.verification_uri,
      expiresAt: Date.now() + Number(body.expires_in ?? 900) * 1_000,
      intervalMs: Math.max(1_000, Number(body.interval ?? 5) * 1_000),
    };
    this.pending.set(capability, pending); this.state.set(capability, "waiting");
    this.messages.set(capability, "Complete the GitHub App authorization in the opened browser, then return here to finish connecting.");
    await this.launch(pending.verificationUri);
    return { userCode: pending.userCode, verificationUri: pending.verificationUri, expiresAt: pending.expiresAt };
  }

  async poll(capability: GitHubCapability): Promise<CapabilityStatus> {
    const pending = this.pending.get(capability);
    if (!pending) return (await this.status()).capabilities[capability];
    if (Date.now() >= pending.expiresAt) {
      this.pending.delete(capability); this.state.set(capability, "failed"); this.messages.set(capability, "This GitHub authorization code expired. Start again.");
      return (await this.status()).capabilities[capability];
    }
    const response = await this.fetcher("https://github.com/login/oauth/access_token", {
      method: "POST", headers: { Accept: "application/json", "Content-Type": "application/x-www-form-urlencoded" },
      body: new URLSearchParams({ client_id: pending.clientId, device_code: pending.deviceCode, grant_type: "urn:ietf:params:oauth:grant-type:device_code" }),
    });
    const body = await json(response);
    if (body.error === "authorization_pending" || body.error === "slow_down") return (await this.status()).capabilities[capability];
    if (!response.ok || typeof body.access_token !== "string") {
      this.pending.delete(capability); this.state.set(capability, "failed"); this.messages.set(capability, concise(String(body.error_description ?? body.error ?? "GitHub authorization failed.")));
      return (await this.status()).capabilities[capability];
    }
    const token: TokenRecord = {
      accessToken: body.access_token,
      refreshToken: typeof body.refresh_token === "string" ? body.refresh_token : undefined,
      expiresAt: typeof body.expires_in === "number" ? Date.now() + body.expires_in * 1_000 : undefined,
      clientId: pending.clientId,
    };
    const viewer = await this.viewer(token.accessToken);
    token.login = viewer.login;
    await this.writeToken(capability, token);
    this.pending.delete(capability); this.state.set(capability, "succeeded"); this.messages.set(capability, `Connected as @${viewer.login}.`);
    return (await this.status()).capabilities[capability];
  }

  private async viewer(token: string): Promise<{ login: string }> {
    const response = await this.fetcher("https://api.github.com/user", { headers: { Accept: "application/vnd.github+json", Authorization: `Bearer ${token}`, "X-GitHub-Api-Version": "2026-03-10" } });
    const body = await json(response);
    if (!response.ok || typeof body.login !== "string") throw new Error(concise(String(body.message ?? "GitHub could not verify this account.")));
    return { login: body.login };
  }

  /** Loads an app-owned token and refreshes it when the GitHub App issued one. */
  async token(capability: GitHubCapability): Promise<string> {
    const token = await this.readToken(capability);
    if (!token) throw new Error(`The ${capability} GitHub App is not connected.`);
    if (token.clientId && token.clientId !== this.clientId(capability)) {
      await this.secrets.remove(SERVICE, account(capability));
      throw new Error(`The ${capability} GitHub App registration changed. Connect it again.`);
    }
    if (!token.expiresAt || token.expiresAt > Date.now() + 60_000) return token.accessToken;
    if (!token.refreshToken) throw new Error(`The ${capability} GitHub App token expired. Connect it again.`);
    const response = await this.fetcher("https://github.com/login/oauth/access_token", {
      method: "POST", headers: { Accept: "application/json", "Content-Type": "application/x-www-form-urlencoded" },
      body: new URLSearchParams({ client_id: this.clientId(capability), grant_type: "refresh_token", refresh_token: token.refreshToken }),
    });
    const body = await json(response);
    if (!response.ok || typeof body.access_token !== "string") throw new Error(concise(String(body.error_description ?? body.error ?? `The ${capability} GitHub App token could not refresh.`)));
    const next: TokenRecord = { accessToken: body.access_token, refreshToken: typeof body.refresh_token === "string" ? body.refresh_token : undefined, expiresAt: typeof body.expires_in === "number" ? Date.now() + body.expires_in * 1_000 : undefined, login: token.login, clientId: this.clientId(capability) };
    await this.writeToken(capability, next);
    return next.accessToken;
  }

  async disconnect(capability: GitHubCapability): Promise<void> {
    await this.secrets.remove(SERVICE, account(capability));
    this.pending.delete(capability); this.state.set(capability, "idle"); this.messages.set(capability, "Disconnected locally. Remove the GitHub App installation in GitHub settings to revoke it at GitHub too.");
  }

  async status(): Promise<GitHubAuthStatus> {
    const capabilities = {} as Record<GitHubCapability, CapabilityStatus>;
    for (const capability of CAPABILITIES) {
      const configured = !!this.config.clientIds[capability];
      const state = this.state.get(capability) ?? "idle";
      const base: CapabilityStatus = { configured, authenticated: false, loginState: state, message: this.messages.get(capability) };
      if (!configured) { base.error = `Configure a dedicated GitHub App for ${capability}.`; capabilities[capability] = base; continue; }
      try {
        const token = await this.readToken(capability);
        if (token) { const viewer = await this.viewer(token.accessToken); base.authenticated = true; base.login = viewer.login; }
      } catch (error) { base.error = concise(String(error)); }
      capabilities[capability] = base;
    }
    return { provider: "github-app-device-flow", capabilities };
  }
}
