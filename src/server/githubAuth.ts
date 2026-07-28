import { runCommand, type CommandResult } from "./gitExec.ts";

export interface GitHubAuthStatus {
  gh: {
    installed: boolean;
    authenticated: boolean;
    login?: string;
    error?: string;
  };
  copilot: {
    installed: boolean;
    version?: string;
  };
  login: {
    state: "idle" | "waiting" | "succeeded" | "failed";
    message?: string;
  };
}

interface LoginChild {
  stdout: ReadableStream<Uint8Array>;
  stderr: ReadableStream<Uint8Array>;
  exited: Promise<number>;
  kill: () => void;
}

export interface GitHubAuthDependencies {
  run?: (command: string[]) => Promise<CommandResult>;
  spawn?: (command: string[]) => LoginChild;
}

function concise(value: string, max = 600): string {
  return value.replace(/\s+/g, " ").trim().slice(0, max);
}

/**
 * Owns the local, browser-based GitHub CLI sign-in. Credentials never cross
 * this process boundary: gh writes its OAuth credential to the operating
 * system credential store, which is also a supported Copilot SDK source.
 */
export class GitHubAuthService {
  private readonly run: (command: string[]) => Promise<CommandResult>;
  private readonly spawn: (command: string[]) => LoginChild;
  private login: GitHubAuthStatus["login"] = { state: "idle" };
  private loginChild: LoginChild | undefined;

  constructor(dependencies: GitHubAuthDependencies = {}) {
    this.run = dependencies.run ?? ((command) => runCommand(command));
    this.spawn = dependencies.spawn ?? ((command) => Bun.spawn({
      cmd: command,
      // All choices are supplied as flags. `--web` opens GitHub's official
      // OAuth/device browser flow, so there is no token field in this UI.
      stdin: "ignore",
      stdout: "pipe",
      stderr: "pipe",
    }));
  }

  private async runSafe(command: string[]): Promise<CommandResult> {
    try {
      return await this.run(command);
    } catch (error) {
      // Bun throws before returning a subprocess when a helper is absent.
      // Treat that as an actionable unavailable state, not a daemon failure.
      return { exitCode: 127, stdout: "", stderr: String(error) };
    }
  }

  async status(): Promise<GitHubAuthStatus> {
    const [ghVersion, ghStatus, copilotVersion] = await Promise.all([
      this.runSafe(["gh", "--version"]),
      this.runSafe(["gh", "auth", "status", "--hostname", "github.com"]),
      this.runSafe(["copilot", "--version"]),
    ]);
    const ghInstalled = ghVersion.exitCode === 0;
    const authenticated = ghStatus.exitCode === 0;
    let login: string | undefined;
    let error: string | undefined;
    if (authenticated) {
      const viewer = await this.runSafe(["gh", "api", "user", "--hostname", "github.com", "--jq", ".login"]);
      if (viewer.exitCode === 0) login = viewer.stdout.trim() || undefined;
      else error = concise(viewer.stderr || viewer.stdout) || "GitHub login could not be verified.";
    } else if (ghInstalled) {
      error = concise(ghStatus.stderr || ghStatus.stdout) || "Not signed in to github.com.";
    } else {
      error = concise(ghVersion.stderr || ghVersion.stdout) || "GitHub CLI is not installed.";
    }

    return {
      gh: { installed: ghInstalled, authenticated, login, error },
      copilot: {
        installed: copilotVersion.exitCode === 0,
        version: copilotVersion.exitCode === 0 ? concise(copilotVersion.stdout) || undefined : undefined,
      },
      login: this.login,
    };
  }

  start(): GitHubAuthStatus["login"] {
    if (this.login.state === "waiting") return this.login;
    try {
      const child = this.spawn([
        "gh", "auth", "login",
        "--hostname", "github.com",
        "--git-protocol", "ssh",
        "--web",
        "--scopes", "repo,read:org",
      ]);
      this.loginChild = child;
      this.login = {
        state: "waiting",
        message: "GitHub opened its secure sign-in page in your browser. Finish the sign-in there, then this page will refresh automatically.",
      };
      void Promise.all([new Response(child.stdout).text(), new Response(child.stderr).text(), child.exited])
        .then(([, stderr, exitCode]) => {
          this.loginChild = undefined;
          this.login = exitCode === 0
            ? { state: "succeeded", message: "GitHub sign-in completed. Copilot /ask can reuse this credential." }
            : { state: "failed", message: concise(stderr) || `GitHub sign-in exited with status ${exitCode}.` };
        })
        .catch((error) => {
          this.loginChild = undefined;
          this.login = { state: "failed", message: `GitHub sign-in failed: ${String(error)}` };
        });
    } catch (error) {
      this.login = { state: "failed", message: `Could not start GitHub sign-in: ${String(error)}` };
    }
    return this.login;
  }

  cancel(): GitHubAuthStatus["login"] {
    if (this.login.state === "waiting") {
      this.loginChild?.kill();
      this.loginChild = undefined;
      this.login = { state: "idle", message: "GitHub sign-in was cancelled." };
    }
    return this.login;
  }
}
