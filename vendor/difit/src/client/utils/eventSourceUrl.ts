import { resolveApiUrl } from '../apiBase';

export function resolveEventSourceUrl(path: string): string {
  // `/ask` is daemon-global, not repository-scoped. WorkspaceShell sets the
  // normal API base to `/api/repos/<id>` for diff/file routes; prepending that
  // base here turns a valid ask stream into a 404 such as
  // `/api/repos/<id>/api/ask/...`. Keep durable Copilot streams on the daemon
  // root while preserving the repository base for file-watch streams.
  const daemonGlobal = path.startsWith('/api/ask/') || path.startsWith('/api/btw/');
  if (daemonGlobal) {
    const apiUrl = import.meta.env.VITE_DIFIT_API_URL?.trim();
    if (!apiUrl) return path;
    try { return new URL(path, apiUrl).toString(); } catch { return path; }
  }
  const basedPath = resolveApiUrl(path);
  const apiUrl = import.meta.env.VITE_DIFIT_API_URL?.trim();
  if (!apiUrl) {
    return basedPath;
  }

  try {
    return new URL(basedPath, apiUrl).toString();
  } catch {
    return basedPath;
  }
}
