import { runCommand, type CommandResult } from "./gitExec.ts";

/**
 * A deliberately small OS-secret-store boundary. There is no plaintext-file
 * fallback: a daemon that cannot reach a supported credential manager simply
 * cannot hold GitHub credentials. The public GitHub App client IDs live in a
 * separate configuration file; only issued user tokens arrive here.
 */
export interface SecretStore {
  get(service: string, account: string): Promise<string | undefined>;
  set(service: string, account: string, value: string): Promise<void>;
  remove(service: string, account: string): Promise<void>;
}

export class SecretStoreUnavailableError extends Error {
  constructor(message: string) { super(message); this.name = "SecretStoreUnavailableError"; }
}

function unavailable(result: CommandResult): never {
  throw new SecretStoreUnavailableError(result.stderr.trim() || result.stdout.trim() || "The operating-system secret store is unavailable.");
}

/** macOS Keychain / Linux libsecret adapter; credentials are never persisted in SQLite. */
export function createSystemSecretStore(run: (command: string[]) => Promise<CommandResult> = runCommand): SecretStore {
  const isMac = process.platform === "darwin";
  const command = (args: string[]) => run(args);
  if (isMac) {
    return {
      async get(service, account) {
        const result = await command(["security", "find-generic-password", "-s", service, "-a", account, "-w"]);
        if (result.exitCode === 44) return undefined; // errSecItemNotFound
        if (result.exitCode !== 0) unavailable(result);
        return result.stdout.trim() || undefined;
      },
      async set(service, account, value) {
        const result = await command(["security", "add-generic-password", "-U", "-s", service, "-a", account, "-w", value]);
        if (result.exitCode !== 0) unavailable(result);
      },
      async remove(service, account) {
        const result = await command(["security", "delete-generic-password", "-s", service, "-a", account]);
        if (result.exitCode !== 0 && result.exitCode !== 44) unavailable(result);
      },
    };
  }
  if (process.platform === "linux") {
    const storeLinux = async (service: string, account: string, value: string) => {
      const child = Bun.spawn({ cmd: ["secret-tool", "store", "--label", service, "service", service, "account", account], stdin: "pipe", stdout: "pipe", stderr: "pipe" });
      child.stdin.write(`${value}\n`);
      child.stdin.end();
      const [stdout, stderr, exitCode] = await Promise.all([new Response(child.stdout).text(), new Response(child.stderr).text(), child.exited]);
      if (exitCode !== 0) unavailable({ stdout, stderr, exitCode });
    };
    return {
      async get(service, account) {
        const result = await command(["secret-tool", "lookup", "service", service, "account", account]);
        if (result.exitCode !== 0) return undefined;
        return result.stdout.trim() || undefined;
      },
      async set(service, account, value) { await storeLinux(service, account, value); },
      async remove(service, account) {
        const result = await command(["secret-tool", "clear", "service", service, "account", account]);
        if (result.exitCode !== 0) unavailable(result);
      },
    };
  }
  throw new SecretStoreUnavailableError(`No supported secret store for ${process.platform}. Use macOS Keychain or Linux libsecret; plaintext token storage is intentionally unsupported.`);
}
