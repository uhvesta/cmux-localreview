import { useCallback, useEffect, useState } from 'react';

import { setApiBase } from './apiBase';
import App from './App';

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
  const [repos, setRepos] = useState<RepoSummary[] | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [selectedRepoId, setSelectedRepoId] = useState<string | null>(null);
  const [sidebarCollapsed, setSidebarCollapsed] = useState(false);
  const [flatMode, setFlatMode] = useState(false);

  useEffect(() => {
    fetchRepos()
      .then((list) => {
        setRepos(list);
        if (list.length > 0 && !selectedRepoId) {
          setSelectedRepoId(list[0]!.id);
        }
      })
      .catch((err: unknown) => {
        setError(err instanceof Error ? err.message : 'Failed to load workspace repos');
      });
    // Only run on mount; repo list is stable for the lifetime of the page load.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  const selectRepo = useCallback((id: string) => {
    setApiBase(`/api/repos/${id}`);
    setSelectedRepoId(id);
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
        {selectedRepoId && <App key={selectedRepoId} />}
      </div>
    </div>
  );
}
