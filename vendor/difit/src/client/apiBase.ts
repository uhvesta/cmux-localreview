// Module-level (not React context) so plain service modules — not just
// components/hooks — can resolve URLs consistently. Set once per repo
// selection, before the diff app for that repo mounts (WorkspaceShell keys
// the mounted <App /> by repoId, so a repo switch always sets this ahead of
// any fetch triggered by the newly-mounted subtree).
let currentApiBase = '';

export function setApiBase(base: string): void {
  currentApiBase = base;
}

export function getApiBase(): string {
  return currentApiBase;
}

export function resolveApiUrl(path: string): string {
  if (!currentApiBase) return path;
  return `${currentApiBase}${path}`;
}
