// Minimal Electron shell: the Go daemon remains the sole application server.
import { app, BrowserWindow, dialog, shell } from "electron";
import { existsSync, readFileSync } from "node:fs";
import { join, resolve } from "node:path";
import { spawn } from "node:child_process";

let daemon;
let quitting = false;

function dataDirectory() {
  return process.env.CMUX_LOCALREVIEW_DATA_DIR || join(app.getPath("userData"), "localreview-data");
}

function daemonPath() {
  if (process.env.LOCALREVIEWD_PATH) return resolve(process.env.LOCALREVIEWD_PATH);
  const name = process.platform === "win32" ? "localreviewd.exe" : "localreviewd";
  return app.isPackaged ? join(process.resourcesPath, name) : join(process.cwd(), "bazel-bin", "cmd", "localreviewd", name);
}

function sleep(milliseconds) { return new Promise((resolveSleep) => setTimeout(resolveSleep, milliseconds)); }

function discoveryPath(dataDir) { return join(dataDir, "daemon.json"); }

function readDiscovery(dataDir) {
  const path = discoveryPath(dataDir);
  if (!existsSync(path)) return undefined;
  try {
    const parsed = JSON.parse(readFileSync(path, "utf8"));
    if (Number.isInteger(parsed.port) && parsed.port > 0 && typeof parsed.token === "string" && parsed.token) return parsed;
  } catch { /* daemon is still atomically replacing its discovery file */ }
  return undefined;
}

async function waitForDaemon(dataDir, timeoutMs = 15_000) {
  const end = Date.now() + timeoutMs;
  while (Date.now() < end) {
    const discovered = readDiscovery(dataDir);
    if (discovered) {
      try {
        const response = await fetch(`http://127.0.0.1:${discovered.port}/health`);
        if (response.ok) return discovered;
      } catch { /* child has not bound its socket yet */ }
    }
    await sleep(100);
  }
  throw new Error("localreviewd did not become healthy within 15 seconds");
}

function stopDaemon() {
  if (!daemon || daemon.killed) return;
  daemon.kill("SIGTERM");
  const force = setTimeout(() => daemon?.kill("SIGKILL"), 3_000);
  daemon.once("exit", () => clearTimeout(force));
}

async function openReviewWindow() {
  const dataDir = dataDirectory();
  const binary = daemonPath();
  if (!existsSync(binary)) throw new Error(`localreviewd was not found at ${binary}. Set LOCALREVIEWD_PATH while developing.`);
  daemon = spawn(binary, ["--port=0", `--data-dir=${dataDir}`, `--parent-pid=${process.pid}`], { stdio: ["ignore", "pipe", "pipe"] });
  daemon.once("error", (error) => { if (!quitting) console.error("localreviewd failed to start", error); });
  const discovered = await waitForDaemon(dataDir);
  const window = new BrowserWindow({
    width: 1440, height: 960, minWidth: 960, minHeight: 640,
    backgroundColor: "#0d1117",
    webPreferences: { contextIsolation: true, nodeIntegration: false, sandbox: true },
  });
  window.webContents.setWindowOpenHandler(({ url }) => { void shell.openExternal(url); return { action: "deny" }; });
  await window.loadURL(`http://127.0.0.1:${discovered.port}/queue#daemonToken=${encodeURIComponent(discovered.token)}`);
}

app.whenReady().then(openReviewWindow).catch(async (error) => {
  await dialog.showMessageBox({ type: "error", title: "cmux local review", message: "Could not start localreviewd", detail: String(error) });
  app.quit();
});
app.on("window-all-closed", () => { if (process.platform !== "darwin") app.quit(); });
app.on("before-quit", () => { quitting = true; stopDaemon(); });
