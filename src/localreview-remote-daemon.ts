#!/usr/bin/env bun
import { main } from "./localreview-remote.ts";

main([process.argv[0] ?? "bun", process.argv[1] ?? "localreview-remote-daemon", "daemon", ...process.argv.slice(2)]).catch((error: unknown) => {
  console.error(error instanceof Error ? error.message : String(error));
  process.exit(1);
});
