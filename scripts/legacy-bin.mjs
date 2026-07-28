#!/usr/bin/env node
// Transitional npm-bin guard.  The native binaries are the control plane;
// this script must never import src/ or start a Bun/Node daemon.  It remains
// only long enough to give existing npm installations an actionable upgrade
// error, then package.json and this file are deleted in Phase 4.
import { basename } from "node:path";

const invoked = basename(process.argv[1] || "cmux-localreview");
const replacements = {
  "cmux-localreview": "localreview open",
  "global-daemon": "localreview daemon run",
  "queue-submit": "localreview submit",
  "localreview-submit": "localreview submit",
  "localreview-reproduce": "localreview reproduce",
  "localreview-reproduce-copilot": "localreview reproduce copilot",
  "localreview-open": "localreview open",
  "localreview-demo": "localreview open",
  "localreview-setup": "localreview setup",
  "localreview-github-app": "localreview auth login",
  "localreview-remote": "localreview remote",
  "localreview-remote-daemon": "localreview daemon run",
};

const replacement = replacements[invoked] || "localreview --help";
console.error(`${invoked} is a retired TypeScript entrypoint. Install the native release and run: ${replacement}`);
console.error("See docs/CLI-WORKFLOWS.md for installation and command migration.");
process.exitCode = 64;
