import { useCallback, useEffect, useRef, useState } from 'react';

import { setApiBase } from './apiBase';
import App from './App';
import { AskPanel } from './components/AskPanel';
import { BtwPanel } from './components/BtwPanel';
import { ReviewControlPanel } from './components/ReviewControlPanel';
import { captureDaemonTokenFromLocation } from './services/daemonAuth';

const WORKSPACE_UI_STATE_KEY = 'cmux-localreview.workspace-ui-v1';

interface WorkspaceUiState {
  selectedRepoId?: string;
  sidebarCollapsed?: boolean;
  flatMode?: boolean;
  btwPanelCollapsed?: boolean;
  askPanelCollapsed?: boolean;
  reviewControlsCollapsed?: boolean;
}

function getStoredWorkspaceUiState(): WorkspaceUiState {
  if (typeof window === 'undefined') return {};
  try {
    const raw = window.localStorage.getItem(WORKSPACE_UI_STATE_KEY);
    if (!raw) return {};
    const parsed = JSON.parse(raw) as WorkspaceUiState;
    return parsed && typeof parsed === 'object' ? parsed : {};
  } catch {
    return {};
  }
}

function saveWorkspaceUiState(state: WorkspaceUiState): void {
  try {
    window.localStorage.setItem(WORKSPACE_UI_STATE_KEY, JSON.stringify(state));
  } catch {
    // Local storage is a best-effort enhancement; keep the review usable when
    // a browser profile disables it.
  }
}

interface RepoSummary {
  id: string;
  workspaceRelativePath: string;
  remoteUrl: string | null;
  changeCount: number;
  files: string[];
}

async function fetchRepos(): Promise<RepoSummary[]> {
  const response = await fetch('/api/repos');
  if (!response.ok) {
    throw new Error(`Failed to fetch workspace repos: ${response.status}`);
  }
  const data = (await response.json()) as { repos: RepoSummary[] };
  return data.repos;
}

/**
 * Workspace-level shell: fetches the repo list, renders a picker sidebar,
 * and mounts the (per-repo) difit <App /> for whichever repo is selected.
 * Sets the module-level API base (see ./apiBase.ts) before the per-repo App
 * mounts so every fetch inside it — and every hook/service it calls — is
 * automatically namespaced to that repo's `/api/repos/<id>` routes.
 */
export function WorkspaceShell() {
  const [initialUiState] = useState(getStoredWorkspaceUiState);
  const uiStateRevisionRef = useRef(0);
  const [uiStateReady, setUiStateReady] = useState(false);
  const [repos, setRepos] = useState<RepoSummary[] | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [selectedRepoId, setSelectedRepoId] = useState<string | null>(initialUiState.selectedRepoId ?? null);
  const [sidebarCollapsed, setSidebarCollapsed] = useState(initialUiState.sidebarCollapsed ?? false);
  const [flatMode, setFlatMode] = useState(initialUiState.flatMode ?? false);

  useEffect(() => {
    fetchRepos()
      .then((list) => {
        setRepos(list);
        if (list.length > 0) {
          setSelectedRepoId((current) =>
            current && list.some((repo) => repo.id === current) ? current : list[0]!.id,
          );
        }
      })
      .catch((err: unknown) => {
        setError(err instanceof Error ? err.message : 'Failed to load workspace repos');
      });
    // Only run on mount; repo list is stable for the lifetime of the page load.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  useEffect(() => {
    captureDaemonTokenFromLocation();
  }, []);

  // The server copy survives browser storage clears and allows another tab to
  // pick up the latest shell state. localStorage remains an immediate fallback
  // for a standalone difit server or an offline browser profile.
  useEffect(() => {
    let cancelled = false;
    void fetch('/api/ui-state?key=workspace-ui-v1')
      .then(async (response) => {
        if (!response.ok) return null;
        return (await response.json()) as { value?: WorkspaceUiState | null; revision?: number };
      })
      .then((remote) => {
        if (cancelled) return;
        if (remote?.value && typeof remote.value === 'object') {
          const value = remote.value;
          if (typeof value.selectedRepoId === 'string') setSelectedRepoId(value.selectedRepoId);
          if (typeof value.sidebarCollapsed === 'boolean') setSidebarCollapsed(value.sidebarCollapsed);
          if (typeof value.flatMode === 'boolean') setFlatMode(value.flatMode);
          if (typeof value.btwPanelCollapsed === 'boolean') setBtwPanelCollapsed(value.btwPanelCollapsed);
          if (typeof value.askPanelCollapsed === 'boolean') setAskPanelCollapsed(value.askPanelCollapsed);
          if (typeof value.reviewControlsCollapsed === 'boolean') setReviewControlsCollapsed(value.reviewControlsCollapsed);
        }
        uiStateRevisionRef.current = remote?.revision ?? 0;
      })
      .catch(() => undefined)
      .finally(() => { if (!cancelled) setUiStateReady(true); });
    return () => { cancelled = true; };
  }, []);

  const selectRepo = useCallback((id: string) => {
    setApiBase(`/api/repos/${id}`);
    setSelectedRepoId(id);
  }, []);

  // Live updates (SPEC.md §1/§4): one WebSocket, fanned out to a per-repo
  // "changes detected" reload banner and to the /btw panel's refetch.
  const [reloadableRepoIds, setReloadableRepoIds] = useState<Set<string>>(new Set());
  const [reloadNonce, setReloadNonce] = useState(0);
  const [btwRefreshNonce, setBtwRefreshNonce] = useState(0);
  const [btwPanelCollapsed, setBtwPanelCollapsed] = useState(initialUiState.btwPanelCollapsed ?? true);
  const [askPanelCollapsed, setAskPanelCollapsed] = useState(initialUiState.askPanelCollapsed ?? true);
  const [reviewControlsCollapsed, setReviewControlsCollapsed] = useState(
    initialUiState.reviewControlsCollapsed ?? true,
  );

  useEffect(() => {
    const value = {
      selectedRepoId: selectedRepoId ?? undefined,
      sidebarCollapsed,
      flatMode,
      btwPanelCollapsed,
      askPanelCollapsed,
      reviewControlsCollapsed,
    };
    saveWorkspaceUiState(value);
    if (!uiStateReady) return;
    void fetch('/api/ui-state', {
      method: 'PUT',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ key: 'workspace-ui-v1', value, revision: uiStateRevisionRef.current }),
    }).then(async (response) => {
      const result = (await response.json().catch(() => null)) as { revision?: number } | null;
      if (response.ok && typeof result?.revision === 'number') uiStateRevisionRef.current = result.revision;
      if (response.status === 409 && typeof result?.revision === 'number') uiStateRevisionRef.current = result.revision;
    }).catch(() => undefined);
  }, [selectedRepoId, sidebarCollapsed, flatMode, btwPanelCollapsed, askPanelCollapsed, reviewControlsCollapsed, uiStateReady]);

  useEffect(() => {
    const proto = window.location.protocol === 'https:' ? 'wss:' : 'ws:';
    const ws = new WebSocket(`${proto}//${window.location.host}/ws`);
    ws.onmessage = (event) => {
      try {
        const msg = JSON.parse(event.data as string) as { type: string; repoId?: string };
        if (msg.type === 'diff-updated' && msg.repoId) {
          setReloadableRepoIds((prev) => new Set(prev).add(msg.repoId!));
        } else if (msg.type === 'btw-update') {
          setBtwRefreshNonce((n) => n + 1);
        }
      } catch {
        // ignore malformed messages
      }
    };
    return () => ws.close();
  }, []);

  const reloadSelectedRepo = useCallback(() => {
    if (!selectedRepoId) return;
    setReloadableRepoIds((prev) => {
      const next = new Set(prev);
      next.delete(selectedRepoId);
      return next;
    });
    setReloadNonce((n) => n + 1);
  }, [selectedRepoId]);

  const [newReviewStatus, setNewReviewStatus] = useState<'idle' | 'starting' | 'error'>('idle');
  const startNewReview = useCallback(async () => {
    if (!window.confirm('Start a new review? Existing comments stay saved under the previous session.')) {
      return;
    }
    setNewReviewStatus('starting');
    try {
      const res = await fetch('/api/sessions/new', { method: 'POST' });
      if (!res.ok) throw new Error(`Failed to start a new session: ${res.status}`);
      setReloadNonce((n) => n + 1);
      setBtwRefreshNonce((n) => n + 1);
      setNewReviewStatus('idle');
    } catch (err) {
      console.error('Failed to start new review:', err);
      setNewReviewStatus('error');
      setTimeout(() => setNewReviewStatus('idle'), 2000);
    }
  }, []);

  const [exportStatus, setExportStatus] = useState<'idle' | 'copied' | 'sent' | 'error'>('idle');

  const exportReview = useCallback(async (destination: 'clipboard' | 'cmux') => {
    try {
      const response = await fetch('/api/export', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ destination }),
      });
      const data = (await response.json()) as { success?: boolean; content?: string; error?: string };
      if (!response.ok || !data.success) {
        throw new Error(data.error ?? `Export failed: ${response.status}`);
      }
      if (destination === 'clipboard' && data.content) {
        await navigator.clipboard.writeText(data.content);
      }
      setExportStatus(destination === 'clipboard' ? 'copied' : 'sent');
      setTimeout(() => setExportStatus('idle'), 2000);
    } catch (err) {
      console.error('Failed to export review:', err);
      setExportStatus('error');
      setTimeout(() => setExportStatus('idle'), 2000);
    }
  }, []);

  if (error) {
    return (
      <div style={{ padding: 24, fontFamily: 'sans-serif' }}>
        <h2>cmux-localreview</h2>
        <p style={{ color: '#c0392b' }}>{error}</p>
      </div>
    );
  }

  if (!repos) {
    return (
      <div style={{ padding: 24, fontFamily: 'sans-serif' }}>
        <p>Scanning workspace for git repositories…</p>
      </div>
    );
  }

  if (repos.length === 0) {
    return (
      <div style={{ padding: 24, fontFamily: 'sans-serif' }}>
        <h2>cmux-localreview</h2>
        <p>No git repositories found in this workspace.</p>
      </div>
    );
  }

  // Ensure the module-level API base matches the selected repo even on the
  // very first render (selectRepo hasn't run yet the instant we auto-select).
  if (selectedRepoId) {
    setApiBase(`/api/repos/${selectedRepoId}`);
  }

  return (
    <div style={{ display: 'flex', height: '100vh', width: '100vw', overflow: 'hidden' }}>
      <aside
        style={{
          width: sidebarCollapsed ? 0 : 240,
          minWidth: sidebarCollapsed ? 0 : 240,
          borderRight: sidebarCollapsed ? 'none' : '1px solid var(--border-color, #333)',
          overflowY: 'auto',
          display: 'flex',
          flexDirection: 'column',
          transition: 'width 0.15s ease, min-width 0.15s ease',
        }}
      >
        {!sidebarCollapsed && (
          <>
            <div
              style={{
                display: 'flex',
                justifyContent: 'space-between',
                alignItems: 'center',
                padding: '12px 14px',
              }}
            >
              <span style={{ fontWeight: 600, fontSize: 13, opacity: 0.7 }}>REPOSITORIES</span>
              <button
                onClick={() => setFlatMode((v) => !v)}
                title="Toggle flat all-repos file list"
                style={{
                  fontSize: 11,
                  padding: '2px 6px',
                  cursor: 'pointer',
                  background: flatMode ? 'rgba(127,127,127,0.25)' : 'transparent',
                  border: '1px solid rgba(127,127,127,0.4)',
                  borderRadius: 4,
                  color: 'inherit',
                }}
              >
                flat
              </button>
            </div>
            {!flatMode ? (
              <ul style={{ listStyle: 'none', margin: 0, padding: 0 }}>
                {repos.map((repo) => (
                  <li key={repo.id}>
                    <button
                      onClick={() => selectRepo(repo.id)}
                      style={{
                        display: 'flex',
                        justifyContent: 'space-between',
                        alignItems: 'center',
                        width: '100%',
                        textAlign: 'left',
                        padding: '8px 14px',
                        background:
                          repo.id === selectedRepoId ? 'rgba(127,127,127,0.15)' : 'transparent',
                        border: 'none',
                        cursor: 'pointer',
                        fontSize: 13,
                        color: 'inherit',
                      }}
                      title={repo.remoteUrl ?? repo.workspaceRelativePath}
                    >
                      <span
                        style={{
                          overflow: 'hidden',
                          textOverflow: 'ellipsis',
                          whiteSpace: 'nowrap',
                        }}
                      >
                        {repo.workspaceRelativePath === '.'
                          ? '(workspace root)'
                          : repo.workspaceRelativePath}
                      </span>
                      {repo.changeCount > 0 && (
                        <span
                          style={{
                            fontSize: 11,
                            borderRadius: 8,
                            padding: '1px 6px',
                            background: 'rgba(46,160,67,0.25)',
                            marginLeft: 6,
                          }}
                        >
                          {repo.changeCount}
                        </span>
                      )}
                    </button>
                  </li>
                ))}
              </ul>
            ) : (
              <div style={{ overflowY: 'auto' }}>
                {repos.map((repo) => (
                  <div key={repo.id}>
                    <div
                      style={{
                        padding: '6px 14px',
                        fontSize: 11,
                        fontWeight: 600,
                        opacity: 0.6,
                        position: 'sticky',
                        top: 0,
                      }}
                    >
                      {repo.workspaceRelativePath === '.' ? '(workspace root)' : repo.workspaceRelativePath}
                    </div>
                    <ul style={{ listStyle: 'none', margin: 0, padding: 0 }}>
                      {repo.files.map((file) => (
                        <li key={file}>
                          <button
                            onClick={() => selectRepo(repo.id)}
                            title={`${repo.workspaceRelativePath}/${file}`}
                            style={{
                              width: '100%',
                              textAlign: 'left',
                              padding: '4px 14px 4px 22px',
                              background:
                                repo.id === selectedRepoId ? 'rgba(127,127,127,0.15)' : 'transparent',
                              border: 'none',
                              cursor: 'pointer',
                              fontSize: 12,
                              color: 'inherit',
                              overflow: 'hidden',
                              textOverflow: 'ellipsis',
                              whiteSpace: 'nowrap',
                            }}
                          >
                            {file}
                          </button>
                        </li>
                      ))}
                    </ul>
                  </div>
                ))}
              </div>
            )}
            <div
              style={{
                marginTop: 'auto',
                borderTop: '1px solid rgba(127,127,127,0.3)',
                padding: 10,
                display: 'flex',
                flexDirection: 'column',
                gap: 6,
              }}
            >
              <button
                onClick={() => void startNewReview()}
                disabled={newReviewStatus === 'starting'}
                style={{
                  fontSize: 12,
                  padding: '6px 8px',
                  cursor: 'pointer',
                  border: '1px solid rgba(127,127,127,0.4)',
                  borderRadius: 4,
                  background: 'transparent',
                  color: 'inherit',
                }}
              >
                {newReviewStatus === 'starting' ? 'Starting…' : 'New Review'}
              </button>
              {newReviewStatus === 'error' && (
                <div style={{ fontSize: 11, color: '#c0392b' }}>Failed to start new review.</div>
              )}
              <div style={{ fontSize: 11, fontWeight: 600, opacity: 0.7, marginTop: 6 }}>EXPORT REVIEW</div>
              <button
                onClick={() => void exportReview('clipboard')}
                style={{
                  fontSize: 12,
                  padding: '6px 8px',
                  cursor: 'pointer',
                  border: '1px solid rgba(127,127,127,0.4)',
                  borderRadius: 4,
                  background: 'transparent',
                  color: 'inherit',
                }}
              >
                Copy all comments to clipboard
              </button>
              <button
                onClick={() => void exportReview('cmux')}
                style={{
                  fontSize: 12,
                  padding: '6px 8px',
                  cursor: 'pointer',
                  border: '1px solid rgba(127,127,127,0.4)',
                  borderRadius: 4,
                  background: 'transparent',
                  color: 'inherit',
                }}
              >
                Send all comments to cmux
              </button>
              {exportStatus === 'copied' && <div style={{ fontSize: 11, color: '#2ea043' }}>Copied.</div>}
              {exportStatus === 'sent' && <div style={{ fontSize: 11, color: '#2ea043' }}>Sent to cmux.</div>}
              {exportStatus === 'error' && <div style={{ fontSize: 11, color: '#c0392b' }}>Export failed.</div>}
            </div>
          </>
        )}
      </aside>
      <div style={{ flex: 1, minWidth: 0, position: 'relative' }}>
        <button
          onClick={() => setSidebarCollapsed((v) => !v)}
          title={sidebarCollapsed ? 'Show repo sidebar' : 'Hide repo sidebar'}
          style={{
            position: 'absolute',
            top: 8,
            left: 8,
            zIndex: 50,
            fontSize: 11,
            padding: '2px 6px',
            cursor: 'pointer',
          }}
        >
          {sidebarCollapsed ? '»' : '«'}
        </button>
        {selectedRepoId && reloadableRepoIds.has(selectedRepoId) && (
          <div
            style={{
              position: 'absolute',
              top: 8,
              left: 40,
              zIndex: 50,
              fontSize: 12,
              padding: '4px 10px',
              borderRadius: 4,
              background: '#9e6a03',
              color: 'white',
              cursor: 'pointer',
            }}
            onClick={reloadSelectedRepo}
          >
            Changes detected — click to reload
          </div>
        )}
        {selectedRepoId && <App key={`${selectedRepoId}-${reloadNonce}`} />}
      </div>
      <BtwPanel
        selectedRepoId={selectedRepoId}
        refreshNonce={btwRefreshNonce}
        collapsed={btwPanelCollapsed}
        onToggleCollapsed={() => setBtwPanelCollapsed((v) => !v)}
      />
      <AskPanel
        collapsed={askPanelCollapsed}
        onToggleCollapsed={() => setAskPanelCollapsed((v) => !v)}
      />
      <ReviewControlPanel
        collapsed={reviewControlsCollapsed}
        onToggleCollapsed={() => setReviewControlsCollapsed((v) => !v)}
        refreshNonce={btwRefreshNonce}
      />
    </div>
  );
}
