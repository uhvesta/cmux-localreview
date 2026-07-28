#!/usr/bin/env bun
import { isAbsolute, resolve } from "node:path";
import { Command } from "commander";

import { startGlobalDaemon } from "./global-daemon.ts";

export interface TunnelOptions {
  sshTarget: string;
  remotePort: number;
  localPort: number;
}

function shellQuote(value: string): string {
  return `'${value.replaceAll("'", "'\\\"'\\\"'")}'`;
}

export function validPort(value: number, label: string): number {
  if (!Number.isInteger(value) || value < 1 || value > 65_535) throw new Error(`${label} must be an integer from 1 through 65535`);
  return value;
}

export function remoteDataDirectory(value?: string): string | undefined {
  if (!value?.trim()) return undefined;
  return isAbsolute(value) ? value : resolve(value);
}

/** A copy/pasteable local-only SSH forward; it never exposes the remote daemon publicly. */
export function tunnelCommand(options: TunnelOptions): string {
  if (!options.sshTarget.trim()) throw new Error("--ssh-target is required");
  const remotePort = validPort(options.remotePort, "--remote-port");
  const localPort = validPort(options.localPort, "--local-port");
  return `ssh -N -o ExitOnForwardFailure=yes -o BatchMode=yes -L 127.0.0.1:${localPort}:127.0.0.1:${remotePort} ${shellQuote(options.sshTarget)}`;
}

export function remoteEnvironment(dataDir?: string): Record<string, string> {
  const resolved = remoteDataDirectory(dataDir);
  return {
    ...(resolved ? { CMUX_LOCALREVIEW_DATA_DIR: resolved } : {}),
  };
}

function printEnvironment(dataDir?: string, shell = false): void {
  const environment = remoteEnvironment(dataDir);
  if (shell) {
    for (const [key, value] of Object.entries(environment)) console.log(`export ${key}=${shellQuote(value)}`);
    return;
  }
  console.log(JSON.stringify(environment, null, 2));
}

export async function main(argv = process.argv): Promise<void> {
  const program = new Command();
  program
    .name("localreview-remote")
    .description("Prepare a loopback-only cmux-localreview daemon on a remote host and generate safe SSH forwards")
    .command("env")
    .description("print remote daemon environment; use --shell to emit export statements")
    .option("--data-dir <path>", "remote daemon state directory (defaults to ~/.local/share/cmux-localreview)")
    .option("--shell", "emit POSIX shell exports")
    .action((options: { dataDir?: string; shell?: boolean }) => printEnvironment(options.dataDir, options.shell));

  program
    .command("daemon")
    .description("start an authenticated loopback-only daemon on this machine")
    .requiredOption("--port <port>", "loopback TCP port used by the SSH forward", Number)
    .option("--data-dir <path>", "remote daemon state directory (defaults to ~/.local/share/cmux-localreview)")
    .option("--json", "print discovery metadata as JSON")
    .action(async (options: { port: number; dataDir?: string; json?: boolean }) => {
      const port = validPort(options.port, "--port");
      const dataDir = remoteDataDirectory(options.dataDir);
      if (dataDir) process.env.CMUX_LOCALREVIEW_DATA_DIR = dataDir;
      const daemon = await startGlobalDaemon({ host: "127.0.0.1", port });
      const output = {
        host: "127.0.0.1",
        port: daemon.discovery.port,
        dataDir: dataDir ?? "~/.local/share/cmux-localreview",
        discoveryPath: dataDir ? `${dataDir}/daemon.json` : "~/.local/share/cmux-localreview/daemon.json",
        tunnelHint: `Run localreview-remote tunnel --ssh-target <user@host> --remote-port ${daemon.discovery.port} --local-port ${daemon.discovery.port} on the local machine.`,
      };
      console.log(options.json ? JSON.stringify(output, null, 2) : `Remote daemon listening on ${output.host}:${output.port}\n${output.tunnelHint}`);
      await new Promise<void>((resolveSignal) => {
        process.once("SIGINT", resolveSignal);
        process.once("SIGTERM", resolveSignal);
      });
      await daemon.close();
    });

  program
    .command("tunnel")
    .description("print the local SSH command that forwards one remote loopback daemon port")
    .requiredOption("--ssh-target <user@host>", "SSH destination")
    .requiredOption("--remote-port <port>", "remote daemon port", Number)
    .option("--local-port <port>", "local listening port (defaults to remote port)", Number)
    .option("--json", "print structured tunnel details")
    .action((options: { sshTarget: string; remotePort: number; localPort?: number; json?: boolean }) => {
      const localPort = options.localPort ?? options.remotePort;
      const command = tunnelCommand({ sshTarget: options.sshTarget, remotePort: options.remotePort, localPort });
      if (options.json) {
        console.log(JSON.stringify({ sshTarget: options.sshTarget, remotePort: options.remotePort, localPort, command }, null, 2));
        return;
      }
      console.log(command);
    });
  await program.parseAsync(argv);
}

if (import.meta.main) {
  main().catch((error: unknown) => {
    console.error(error instanceof Error ? error.message : String(error));
    process.exit(1);
  });
}
