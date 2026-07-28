// Minimal Electron shell: the Go daemon remains the sole application server.
import { app, BrowserWindow, dialog, shell } from "electron";
import { existsSync, readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";
import { dirname, join, resolve } from "node:path";
import { spawn } from "node:child_process";

let daemon;
let reviewWindow;
let quitting = false;
const desktopDirectory = dirname(fileURLToPath(import.meta.url));
const checkoutDirectory = resolve(desktopDirectory, "..");

function dataDirectory() {
  return process.env.CMUX_LOCALREVIEW_DATA_DIR || join(app.getPath("userData"), "localreview-data");
}

function daemonPath() {
  if (process.env.LOCALREVIEWD_PATH) return resolve(process.env.LOCALREVIEWD_PATH);
  const name = process.platform === "win32" ? "localreviewd.exe" : "localreviewd";
  if (app.isPackaged) return join(process.resourcesPath, name);
  // rules_go writes executable binaries below <target>_/<binary>. Do not use
  // process.cwd(): Electron can be launched from any directory.
  return join(checkoutDirectory, "bazel-bin", "cmd", "localreviewd", "localreviewd_", name);
}

function sleep(milliseconds) { return new Promise((resolveSleep) => setTimeout(resolveSleep, milliseconds)); }

function discoveryPath(dataDir) { return join(dataDir, "daemon.json"); }

function readDiscovery(dataDir) {
  const path = discoveryPath(dataDir);
  if (!existsSync(path)) return undefined;
  try {
    const parsed = JSON.parse(readFileSync(path, "utf8"));
    if (Number.isInteger(parsed.port) && parsed.port > 0 && Number.isInteger(parsed.pid) && parsed.pid > 0 && typeof parsed.token === "string" && parsed.token) return parsed;
  } catch { /* daemon is still atomically replacing its discovery file */ }
  return undefined;
}

async function waitForDaemon(dataDir, expectedPID, timeoutMs = 15_000) {
  const end = Date.now() + timeoutMs;
  while (Date.now() < end) {
    const discovered = readDiscovery(dataDir);
    // A profile can retain a discovery file after a crash. Only trust the
    // discovery record written by the sidecar this Electron process spawned.
    if (discovered && discovered.pid === expectedPID) {
      try {
        const response = await fetch(`http://127.0.0.1:${discovered.port}/health`);
        const health = response.ok ? await response.json() : undefined;
        if (health?.ok && health.pid === expectedPID) return discovered;
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

function focusReviewWindow() {
  if (!reviewWindow || reviewWindow.isDestroyed()) return false;
  if (reviewWindow.isMinimized()) reviewWindow.restore();
  reviewWindow.focus();
  return true;
}

async function openReviewWindow() {
  if (focusReviewWindow()) return;
  const dataDir = dataDirectory();
  const binary = daemonPath();
  if (!existsSync(binary)) throw new Error(`localreviewd was not found at ${binary}. Set LOCALREVIEWD_PATH while developing.`);
  daemon = spawn(binary, ["--port=0", `--data-dir=${dataDir}`, `--parent-pid=${process.pid}`], { stdio: ["ignore", "pipe", "pipe"] });
  daemon.once("error", (error) => { if (!quitting) console.error("localreviewd failed to start", error); });
  daemon.once("exit", (code, signal) => {
    if (quitting) return;
    daemon = undefined;
    if (reviewWindow && !reviewWindow.isDestroyed()) {
      void dialog.showMessageBox(reviewWindow, { type: "error", title: "cmux local review", message: "The local review daemon stopped", detail: `Exit code: ${code ?? "none"}; signal: ${signal ?? "none"}` });
    }
  });
  const discovered = await waitForDaemon(dataDir, daemon.pid);
  reviewWindow = new BrowserWindow({
    width: 1440, height: 960, minWidth: 960, minHeight: 640,
    backgroundColor: "#0d1117",
    webPreferences: { contextIsolation: true, nodeIntegration: false, sandbox: true, webSecurity: true },
  });
  const daemonOrigin = `http://127.0.0.1:${discovered.port}`;
  reviewWindow.webContents.session.setPermissionRequestHandler((_contents, _permission, callback) => callback(false));
  reviewWindow.webContents.setWindowOpenHandler(({ url }) => { void shell.openExternal(url); return { action: "deny" }; });
  reviewWindow.webContents.on("will-navigate", (event, url) => {
    if (new URL(url).origin === daemonOrigin) return;
    event.preventDefault();
    void shell.openExternal(url);
  });
  reviewWindow.on("closed", () => { reviewWindow = undefined; });
  await reviewWindow.loadURL(`${daemonOrigin}/queue#daemonToken=${encodeURIComponent(discovered.token)}`);
}

if (!app.requestSingleInstanceLock()) {
  app.quit();
}
app.on("second-instance", () => { void openReviewWindow(); });
app.whenReady().then(openReviewWindow).catch(async (error) => {
  await dialog.showMessageBox({ type: "error", title: "cmux local review", message: "Could not start localreviewd", detail: String(error) });
  app.quit();
});
app.on("window-all-closed", () => { if (process.platform !== "darwin") app.quit(); });
app.on("activate", () => { void openReviewWindow(); });
app.on("before-quit", () => { quitting = true; stopDaemon(); });
