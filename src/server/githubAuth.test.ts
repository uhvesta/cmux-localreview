import { describe, expect, test } from "bun:test";

import { GitHubAuthService, type GitHubCapability } from "./githubAuth.ts";
import type { SecretStore } from "./secretStore.ts";

function memorySecrets(): SecretStore & { values: Map<string, string> } {
  const values = new Map<string, string>();
  const key = (service: string, account: string) => `${service}\0${account}`;
  return { values, get: async (service, account) => values.get(key(service, account)), set: async (service, account, value) => { values.set(key(service, account), value); }, remove: async (service, account) => { values.delete(key(service, account)); } };
}

describe("GitHubAuthService", () => {
  test("uses separate GitHub App device flows and stores issued tokens only in the secret store", async () => {
    const secrets = memorySecrets();
    let exchanges = 0;
    const fetcher: typeof fetch = (async (input: string | URL | Request) => {
      const url = String(input);
      if (url.endsWith("/login/device/code")) return new Response(JSON.stringify({ device_code: "opaque-device-code", user_code: "ABCD-EFGH", verification_uri: "https://github.com/login/device", expires_in: 900, interval: 1 }), { status: 200 });
      if (url.endsWith("/login/oauth/access_token")) { exchanges++; return new Response(JSON.stringify({ access_token: "ghu_app_user_token", expires_in: 28_800, refresh_token: "ghr_refresh" }), { status: 200 }); }
      if (url.endsWith("/user")) return new Response(JSON.stringify({ login: "octocat" }), { status: 200 });
      return new Response(JSON.stringify({ message: "unexpected" }), { status: 500 });
    }) as typeof fetch;
    const service = new GitHubAuthService(secrets, fetcher, async () => undefined, "/tmp/cmux-localreview-github-auth-test.json");
    await service.configure("read", "Iv1.readClient");
    await service.configure("write", "Iv1.writeClient");
    await service.configure("copilot", "Iv1.copilotClient");

    const device = await service.start("copilot");
    expect(device).toMatchObject({ userCode: "ABCD-EFGH", verificationUri: "https://github.com/login/device" });
    const connected = await service.poll("copilot");
    expect(connected).toMatchObject({ authenticated: true, login: "octocat", loginState: "succeeded" });
    expect(await service.token("copilot")).toBe("ghu_app_user_token");
    expect(exchanges).toBe(1);
    expect([...secrets.values.values()].join("\n")).toContain("ghu_app_user_token");
    const status = await service.status();
    expect(status.capabilities.read.authenticated).toBe(false);
    expect(status.capabilities.write.authenticated).toBe(false);
    expect(status.capabilities.copilot.authenticated).toBe(true);

    await service.disconnect("copilot");
    await expect(service.token("copilot")).rejects.toThrow("not connected");
  });

  test("revokes a saved capability token when its GitHub App client ID changes", async () => {
    const secrets = memorySecrets();
    const service = new GitHubAuthService(secrets, fetch, async () => undefined, "/tmp/cmux-localreview-github-auth-client-change.json");
    await service.configure("read", "Iv1.firstClient");
    await secrets.set("cmux-localreview.github-app", "github.com:read", JSON.stringify({ accessToken: "old", clientId: "Iv1.firstClient" }));
    await service.configure("read", "Iv1.secondClient");
    await expect(service.token("read")).rejects.toThrow("not connected");
  });

  test("requires configured capability instead of falling back to gh or another capability", async () => {
    const service = new GitHubAuthService(memorySecrets(), fetch, async () => undefined, "/tmp/cmux-localreview-github-auth-unconfigured.json");
    await expect(service.start("read" as GitHubCapability)).rejects.toThrow("Configure the read GitHub App");
  });
});
