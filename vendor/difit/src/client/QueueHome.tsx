import { useCallback, useEffect, useState } from 'react';

import { captureDaemonTokenFromLocation, daemonFetch, saveDaemonToken } from './services/daemonAuth';

type QueueStatus = 'queued' | 'in_review' | 'changes_requested' | 'approved' | 'completed';

interface QueueItem {
  id: string;
  title: string;
  body: string;
  workspacePath: string;
  kind: 'local' | 'remote';
  status: QueueStatus;
  position: number;
  agentProvider: string | null;
  acpState: 'unavailable' | 'connecting' | 'idle' | 'busy' | 'error';
  acpLastError: string | null;
}

interface FederationRuntime {
  state: 'disconnected' | 'connecting' | 'connected' | 'error' | 'disabled';
  localPort: number | null;
  cachedResponses: number;
  lastConnectedAt: number | null;
  lastError: string | null;
}
interface FederationNode {
  id: string;
  label: string;
  sshTarget: string;
  remotePort: number;
  enabled: boolean;
  lastConnectedAt?: number | null;
  lastError?: string | null;
}
interface FederationQueue { node: FederationNode; items: QueueItem[]; error?: string; }

interface FederationNodeWithRuntime extends FederationNode { runtime?: FederationRuntime; }

interface GitHubAuthStatus {
  gh: { installed: boolean; authenticated: boolean; login?: string; error?: string };
  copilot: { installed: boolean; version?: string };
  login: { state: 'idle' | 'waiting' | 'succeeded' | 'failed'; message?: string };
}

const buttonStyle = {
  fontSize: 12, padding: '6px 9px', borderRadius: 5, border: '1px solid rgba(127,127,127,0.45)',
  background: 'transparent', color: 'inherit', cursor: 'pointer',
} as const;

function statusColor(status: QueueStatus): string {
  if (status === 'approved') return '#2ea043';
  if (status === 'changes_requested') return '#d29922';
  if (status === 'completed') return '#8b949e';
  return '#58a6ff';
}

function QueueCard({ item, remoteNode, onOpenWorkspace }: { item: QueueItem; remoteNode?: FederationNode; onOpenWorkspace?: (item: QueueItem) => void }) {
  return <article style={{ border: '1px solid rgba(127,127,127,0.3)', borderRadius: 8, padding: 14, background: 'rgba(127,127,127,0.04)' }}>
    <div style={{ display: 'flex', gap: 10, alignItems: 'baseline', flexWrap: 'wrap' }}>
      <strong style={{ fontSize: 14 }}>{item.title}</strong>
      <span style={{ color: statusColor(item.status), fontSize: 11, fontWeight: 600 }}>{item.status.replace('_', ' ')}</span>
      <span style={{ fontSize: 11, opacity: 0.72 }}>{item.kind === 'remote' ? 'GitHub PR' : 'local snapshot'}</span>
    </div>
    {item.body && <details style={{ marginTop: 7, fontSize: 12, opacity: 0.8 }}><summary style={{ cursor: 'pointer' }}>Description</summary><p style={{ margin: '6px 0 0', whiteSpace: 'pre-wrap', maxHeight: 180, overflowY: 'auto' }}>{item.body}</p></details>}
    <div style={{ marginTop: 8, fontFamily: 'monospace', fontSize: 11, overflowWrap: 'anywhere', opacity: 0.78 }} title={item.workspacePath}>{item.workspacePath}</div>
    <div style={{ display: 'flex', flexWrap: 'wrap', gap: 7, marginTop: 9, fontSize: 11, opacity: 0.76 }}>
      <span>ACP: {item.acpState}</span>{item.agentProvider && <span>agent: {item.agentProvider}</span>}{remoteNode && <span>node: {remoteNode.label || remoteNode.sshTarget}</span>}
    </div>
    {item.acpLastError && <div style={{ marginTop: 6, color: '#f85149', fontSize: 11 }}>{item.acpLastError}</div>}
    {onOpenWorkspace && <div style={{ display: 'flex', gap: 7, marginTop: 12 }}><button onClick={() => onOpenWorkspace(item)} style={buttonStyle}>Open workspace</button></div>}
  </article>;
}

function runtimeLabel(runtime?: FederationRuntime): string {
  if (!runtime) return 'not checked';
  if (runtime.state === 'connected') return runtime.localPort ? `connected · localhost:${runtime.localPort}` : 'connected';
  if (runtime.state === 'connecting') return 'connecting…';
  if (runtime.state === 'error') return 'needs attention';
  return runtime.state;
}

/** Daemon-wide landing page; remains available before any review workspace is open. */
export function QueueHome() {
  const [items, setItems] = useState<QueueItem[]>([]);
  const [federation, setFederation] = useState<FederationQueue[]>([]);
  const [activeWorkspace, setActiveWorkspace] = useState<string | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [loading, setLoading] = useState(true);
  const [opening, setOpening] = useState(false);
  const [remoteUrl, setRemoteUrl] = useState('');
  const [submittingRemote, setSubmittingRemote] = useState(false);
  const [localPath, setLocalPath] = useState('');
  const [localTitle, setLocalTitle] = useState('');
  const [submittingLocal, setSubmittingLocal] = useState(false);
  const [nodes, setNodes] = useState<FederationNodeWithRuntime[]>([]);
  const [nodeLabel, setNodeLabel] = useState('');
  const [nodeTarget, setNodeTarget] = useState('');
  const [nodePort, setNodePort] = useState('57140');
  const [nodeToken, setNodeToken] = useState('');
  const [savingNode, setSavingNode] = useState(false);
  const [nodeAction, setNodeAction] = useState<string | null>(null);
  const [daemonToken, setDaemonToken] = useState('');
  const [authRequired, setAuthRequired] = useState(false);
  const [githubAuth, setGithubAuth] = useState<GitHubAuthStatus | null>(null);
  const [githubAuthLoading, setGithubAuthLoading] = useState(false);
  const [startingGitHubAuth, setStartingGitHubAuth] = useState(false);

  useEffect(() => { captureDaemonTokenFromLocation(); }, []);
  const refresh = useCallback(async () => {
    setLoading(true); setError(null);
    try {
      const [queueResponse, workspacesResponse, federationResponse, nodesResponse] = await Promise.all([daemonFetch('/api/queue'), daemonFetch('/api/workspaces'), daemonFetch('/api/federation/queue'), daemonFetch('/api/federation/nodes')]);
      if (!queueResponse.ok || !workspacesResponse.ok) {
        const needsToken = queueResponse.status === 401 || workspacesResponse.status === 401;
        setAuthRequired(needsToken);
        throw new Error(needsToken ? 'This local daemon requires its browser token.' : 'The local review queue is unavailable.');
      }
      setAuthRequired(false);
      const queue = (await queueResponse.json()) as { items?: QueueItem[] };
      const workspaces = (await workspacesResponse.json()) as { activeWorkspace?: string | null };
      setItems(Array.isArray(queue.items) ? queue.items : []); setActiveWorkspace(workspaces.activeWorkspace ?? null);
      setLocalPath((current) => current || workspaces.activeWorkspace || '');
      if (federationResponse.ok) { const aggregate = (await federationResponse.json()) as { nodes?: FederationQueue[] }; setFederation(Array.isArray(aggregate.nodes) ? aggregate.nodes : []); } else setFederation([]);
      if (nodesResponse.ok) {
        const nodeData = (await nodesResponse.json()) as { nodes?: FederationNode[] };
        const listed = Array.isArray(nodeData.nodes) ? nodeData.nodes : [];
        const withRuntime = await Promise.all(listed.map(async (node) => {
          const status = await daemonFetch(`/api/federation/nodes/${encodeURIComponent(node.id)}/status`);
          if (!status.ok) return node;
          const data = (await status.json()) as { runtime?: FederationRuntime };
          return { ...node, runtime: data.runtime };
        }));
        setNodes(withRuntime);
      }
    } catch (err) { setError(err instanceof Error ? err.message : 'Unable to load the queue.'); } finally { setLoading(false); }
  }, []);
  useEffect(() => { void refresh(); }, [refresh]);

  const refreshGitHubAuth = useCallback(async () => {
    setGithubAuthLoading(true);
    try {
      const response = await daemonFetch('/api/github/auth/status');
      if (!response.ok) return;
      setGithubAuth((await response.json()) as GitHubAuthStatus);
    } finally { setGithubAuthLoading(false); }
  }, []);
  useEffect(() => { void refreshGitHubAuth(); }, [refreshGitHubAuth]);
  useEffect(() => {
    if (githubAuth?.login.state !== 'waiting') return;
    const timer = window.setInterval(() => { void refreshGitHubAuth(); }, 1_500);
    return () => window.clearInterval(timer);
  }, [githubAuth?.login.state, refreshGitHubAuth]);

  const startGitHubAuth = useCallback(async () => {
    setStartingGitHubAuth(true); setError(null);
    try {
      const response = await daemonFetch('/api/github/auth/start', { method: 'POST' });
      const body = (await response.json().catch(() => null)) as { login?: GitHubAuthStatus['login']; error?: string } | null;
      if (!response.ok) throw new Error(body?.error ?? 'Could not start GitHub sign-in.');
      if (body?.login && githubAuth) setGithubAuth({ ...githubAuth, login: body.login });
      await refreshGitHubAuth();
    } catch (err) { setError(err instanceof Error ? err.message : 'Could not start GitHub sign-in.'); }
    finally { setStartingGitHubAuth(false); }
  }, [githubAuth, refreshGitHubAuth]);

  const cancelGitHubAuth = useCallback(async () => {
    await daemonFetch('/api/github/auth/cancel', { method: 'POST' });
    await refreshGitHubAuth();
  }, [refreshGitHubAuth]);

  const openWorkspace = useCallback(async (item: QueueItem) => {
    setOpening(true); setError(null);
    try {
      const response = await daemonFetch('/api/workspaces/open', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ workspacePath: item.workspacePath }) });
      if (!response.ok) { const body = (await response.json().catch(() => null)) as { error?: string } | null; throw new Error(body?.error ?? 'Could not open this workspace.'); }
      window.location.assign('/review');
    } catch (err) { setError(err instanceof Error ? err.message : 'Could not open this workspace.'); setOpening(false); }
  }, []);
  const openNext = useCallback(async () => {
    setOpening(true); setError(null);
    try { const response = await daemonFetch('/api/queue/open-next', { method: 'POST' }); if (!response.ok) throw new Error('There are no queued reviews to open.'); window.location.assign('/review'); }
    catch (err) { setError(err instanceof Error ? err.message : 'Could not open the next review.'); setOpening(false); }
  }, []);

  const submitRemote = useCallback(async (event: React.FormEvent<HTMLFormElement>) => {
    event.preventDefault();
    const value = remoteUrl.trim();
    if (!value) return;
    try {
      const parsed = new URL(value);
      if (!/^https?:$/.test(parsed.protocol) || !/\/pull\/\d+\/?$/.test(parsed.pathname)) {
        throw new Error('Enter a GitHub pull-request URL, for example https://github.com/owner/repo/pull/123.');
      }
      setSubmittingRemote(true); setError(null);
      const response = await daemonFetch('/api/queue', {
        method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ remoteUrl: value }),
      });
      const body = (await response.json().catch(() => null)) as { error?: string } | null;
      if (!response.ok) throw new Error(body?.error ?? 'Could not add that pull request.');
      setRemoteUrl('');
      await refresh();
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Could not add that pull request.');
    } finally { setSubmittingRemote(false); }
  }, [refresh, remoteUrl]);

  const submitLocal = useCallback(async (event: React.FormEvent<HTMLFormElement>) => {
    event.preventDefault();
    const workspacePath = localPath.trim();
    if (!workspacePath) { setError('Enter the local workspace path to snapshot.'); return; }
    setSubmittingLocal(true); setError(null);
    try {
      const response = await daemonFetch('/api/queue', {
        method: 'POST', headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ workspacePath, title: localTitle.trim() || undefined }),
      });
      const body = (await response.json().catch(() => null)) as { error?: string } | null;
      if (!response.ok) throw new Error(body?.error ?? 'Could not snapshot and queue that workspace.');
      setLocalTitle(''); await refresh();
    } catch (err) { setError(err instanceof Error ? err.message : 'Could not snapshot and queue that workspace.'); }
    finally { setSubmittingLocal(false); }
  }, [localPath, localTitle, refresh]);

  const addNode = useCallback(async (event: React.FormEvent<HTMLFormElement>) => {
    event.preventDefault();
    const remotePort = Number(nodePort);
    if (!nodeLabel.trim() || !nodeTarget.trim() || !nodeToken.trim() || !Number.isInteger(remotePort)) {
      setError('Give the remote daemon a name, SSH target, port, and the daemon token from that machine.');
      return;
    }
    setSavingNode(true); setError(null);
    try {
      const response = await daemonFetch('/api/federation/nodes', {
        method: 'POST', headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ label: nodeLabel.trim(), sshTarget: nodeTarget.trim(), remotePort, token: nodeToken.trim() }),
      });
      const body = (await response.json().catch(() => null)) as { error?: string } | null;
      if (!response.ok) throw new Error(body?.error ?? 'Could not save the remote daemon.');
      setNodeLabel(''); setNodeTarget(''); setNodePort('57140'); setNodeToken('');
      await refresh();
    } catch (err) { setError(err instanceof Error ? err.message : 'Could not save the remote daemon.'); }
    finally { setSavingNode(false); }
  }, [nodeLabel, nodePort, nodeTarget, nodeToken, refresh]);

  const actOnNode = useCallback(async (node: FederationNode, action: 'connect' | 'disconnect' | 'delete') => {
    const label = node.label || node.sshTarget;
    if (action === 'delete' && !window.confirm(`Remove ${label}? This removes only this machine's saved SSH connection.`)) return;
    setNodeAction(`${node.id}:${action}`); setError(null);
    try {
      const response = await daemonFetch(`/api/federation/nodes/${encodeURIComponent(node.id)}${action === 'delete' ? '' : `/${action}`}`, { method: action === 'delete' ? 'DELETE' : 'POST' });
      const body = (await response.json().catch(() => null)) as { error?: string } | null;
      if (!response.ok) throw new Error(body?.error ?? `Could not ${action} ${label}.`);
      await refresh();
    } catch (err) { setError(err instanceof Error ? err.message : `Could not ${action} ${label}.`); }
    finally { setNodeAction(null); }
  }, [refresh]);

  const queuedCount = items.filter((item) => item.status === 'queued').length;
  const localItems = items.filter((item) => item.kind === 'local');
  const remoteItems = items.filter((item) => item.kind === 'remote');
  return <main style={{ minHeight: '100vh', boxSizing: 'border-box', maxWidth: 1080, margin: '0 auto', padding: '28px 24px 64px', fontFamily: 'system-ui, sans-serif' }}>
    <header style={{ display: 'flex', gap: 14, justifyContent: 'space-between', alignItems: 'flex-start', flexWrap: 'wrap', marginBottom: 26 }}>
      <div><div style={{ fontSize: 12, fontWeight: 700, letterSpacing: '0.08em', opacity: 0.62 }}>CMUX LOCAL REVIEW</div><h1 style={{ margin: '5px 0 0', fontSize: 28 }}>Queue Home</h1><p style={{ margin: '7px 0 0', opacity: 0.72, fontSize: 13 }}>{activeWorkspace ? <>Active workspace: <code>{activeWorkspace}</code></> : 'No review workspace is open. Select a queue item to begin.'}</p></div>
      <div style={{ display: 'flex', gap: 8, alignItems: 'center' }}>{activeWorkspace && <button onClick={() => window.location.assign('/review')} style={buttonStyle}>Current review</button>}<button onClick={() => void refresh()} disabled={loading || opening} style={buttonStyle}>{loading ? 'Loading…' : 'Refresh'}</button><button onClick={() => void openNext()} disabled={opening || queuedCount === 0} style={{ ...buttonStyle, borderColor: '#2ea043' }}>Open next ({queuedCount})</button></div>
    </header>
    {error && <div role="alert" style={{ marginBottom: 16, padding: '10px 12px', borderRadius: 6, color: '#f85149', border: '1px solid rgba(248,81,73,0.5)' }}>{error}</div>}
    {authRequired && <form onSubmit={(event) => { event.preventDefault(); if (saveDaemonToken(daemonToken)) { setDaemonToken(''); void refresh(); } }} style={{ display: 'flex', gap: 8, alignItems: 'center', margin: '0 0 16px', padding: 12, border: '1px solid rgba(210,153,34,0.55)', borderRadius: 6 }} aria-label="Connect Queue Home to local daemon">
      <span style={{ fontSize: 12, opacity: 0.82 }}>Paste the daemon token from <code>localreview-open --home</code>:</span>
      <input value={daemonToken} onChange={(event) => setDaemonToken(event.target.value)} type="password" autoComplete="off" aria-label="Daemon bearer token" placeholder="Daemon token" style={{ flex: 1, minWidth: 180, padding: '7px 8px', borderRadius: 5, border: '1px solid rgba(127,127,127,0.45)', background: 'transparent', color: 'inherit' }} />
      <button type="submit" disabled={!daemonToken.trim()} style={buttonStyle}>Connect</button>
    </form>}
    {githubAuth && <section aria-label="GitHub and Copilot connection" style={{ margin: '0 0 18px', padding: 12, border: `1px solid ${githubAuth.gh.authenticated ? 'rgba(46,160,67,0.6)' : 'rgba(210,153,34,0.62)'}`, borderRadius: 8, display: 'flex', gap: 12, justifyContent: 'space-between', alignItems: 'center', flexWrap: 'wrap' }}>
      <div style={{ minWidth: 250 }}>
        <div style={{ display: 'flex', gap: 8, alignItems: 'baseline', flexWrap: 'wrap' }}><strong>GitHub &amp; Copilot</strong><span style={{ color: githubAuth.gh.authenticated ? '#2ea043' : '#d29922', fontSize: 12 }}>{githubAuth.gh.authenticated ? `Connected${githubAuth.gh.login ? ` as @${githubAuth.gh.login}` : ''}` : githubAuth.gh.installed ? 'Not connected' : 'GitHub CLI unavailable'}</span></div>
        <p style={{ margin: '4px 0 0', fontSize: 12, opacity: 0.74 }}>
          {githubAuth.login.state === 'waiting' ? githubAuth.login.message : githubAuth.gh.authenticated ? <>GitHub API calls and fresh Copilot SDK <code>/ask</code> chats use this secure GitHub CLI OAuth login. {githubAuth.copilot.installed ? 'Copilot CLI is installed.' : 'Install Copilot CLI to use /ask.'}</> : githubAuth.gh.error ?? 'Sign in with GitHub to read private PRs, publish reviews, and use /ask.'}
        </p>
        {githubAuth.login.state !== 'idle' && githubAuth.login.state !== 'waiting' && <p role="status" style={{ margin: '5px 0 0', fontSize: 12, color: githubAuth.login.state === 'failed' ? '#f85149' : '#2ea043' }}>{githubAuth.login.message}</p>}
      </div>
      <div style={{ display: 'flex', gap: 7, alignItems: 'center', flexWrap: 'wrap' }}>
        {githubAuth.login.state === 'waiting' ? <><button onClick={() => void refreshGitHubAuth()} disabled={githubAuthLoading} style={buttonStyle}>{githubAuthLoading ? 'Checking…' : 'Check sign-in'}</button><button onClick={() => void cancelGitHubAuth()} style={buttonStyle}>Cancel</button></> : <button onClick={() => void startGitHubAuth()} disabled={startingGitHubAuth || !githubAuth.gh.installed} style={{ ...buttonStyle, borderColor: '#2ea043' }}>{startingGitHubAuth ? 'Opening GitHub…' : githubAuth.gh.authenticated ? 'Reconnect GitHub' : 'Authenticate with GitHub'}</button>}
        <button onClick={() => void refreshGitHubAuth()} disabled={githubAuthLoading} style={buttonStyle}>Refresh status</button>
      </div>
      <div style={{ width: '100%', fontSize: 11, opacity: 0.64 }}>This opens GitHub’s browser sign-in and stores OAuth in your system credential store. No GitHub token is pasted into or saved by cmux-localreview.</div>
    </section>}
    <div style={{ display: 'grid', gridTemplateColumns: 'repeat(auto-fit, minmax(360px, 1fr))', gap: 22, alignItems: 'start' }}>
      <section aria-label="Local review queue">
        <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'baseline', marginBottom: 10 }}><h2 style={{ fontSize: 16, margin: 0 }}>Local</h2><span style={{ fontSize: 12, opacity: 0.62 }}>{localItems.length} snapshot{localItems.length === 1 ? '' : 's'}</span></div>
        <form onSubmit={submitLocal} style={{ display: 'grid', gridTemplateColumns: 'minmax(0, 1fr) minmax(100px, 0.45fr) auto', gap: 7, marginBottom: 10 }} aria-label="Submit local workspace">
          <input value={localPath} onChange={(event) => setLocalPath(event.target.value)} placeholder="/absolute/path/to/workspace" aria-label="Local workspace path" required style={{ minWidth: 0, padding: '7px 8px', borderRadius: 5, border: '1px solid rgba(127,127,127,0.45)', background: 'transparent', color: 'inherit' }} />
          <input value={localTitle} onChange={(event) => setLocalTitle(event.target.value)} placeholder="Review title (optional)" aria-label="Review title" style={{ minWidth: 0, padding: '7px 8px', borderRadius: 5, border: '1px solid rgba(127,127,127,0.45)', background: 'transparent', color: 'inherit' }} />
          <button type="submit" disabled={submittingLocal || !localPath.trim()} style={{ ...buttonStyle, whiteSpace: 'nowrap' }}>{submittingLocal ? 'Snapshotting…' : 'Submit local'}</button>
        </form>
        <p style={{ margin: '0 0 10px', fontSize: 12, opacity: 0.68 }}>Immutable snapshots submitted from this machine.</p>
        {loading ? <p style={{ opacity: 0.65 }}>Loading queue…</p> : localItems.length === 0 ? <div style={{ padding: 14, border: '1px dashed rgba(127,127,127,0.45)', borderRadius: 8, opacity: 0.76 }}>Nothing local is queued. Submit a path above, or run <code>localreview-submit &lt;path&gt;</code> to capture cmux and Copilot metadata atomically.</div> : <div style={{ display: 'grid', gap: 10 }}>{localItems.map((item) => <QueueCard key={item.id} item={item} onOpenWorkspace={openWorkspace} />)}</div>}
      </section>
      <section aria-label="Remote pull-request queue">
        <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'baseline', marginBottom: 10 }}><h2 style={{ fontSize: 16, margin: 0 }}>Remote</h2><span style={{ fontSize: 12, opacity: 0.62 }}>{remoteItems.length} pull request{remoteItems.length === 1 ? '' : 's'}</span></div>
        <form onSubmit={submitRemote} style={{ display: 'flex', gap: 7, marginBottom: 10 }} aria-label="Add remote pull request">
          <label style={{ position: 'absolute', width: 1, height: 1, overflow: 'hidden', clipPath: 'inset(50%)' }} htmlFor="remote-pr-url">Pull request URL</label>
          <input id="remote-pr-url" type="url" value={remoteUrl} onChange={(event) => setRemoteUrl(event.target.value)} placeholder="https://github.com/owner/repo/pull/123" style={{ minWidth: 0, flex: 1, padding: '7px 8px', borderRadius: 5, border: '1px solid rgba(127,127,127,0.45)', background: 'transparent', color: 'inherit' }} />
          <button type="submit" disabled={submittingRemote || !remoteUrl.trim()} style={{ ...buttonStyle, whiteSpace: 'nowrap' }}>{submittingRemote ? 'Adding…' : 'Add PR'}</button>
        </form>
        <p style={{ margin: '0 0 10px', fontSize: 12, opacity: 0.68 }}>Add a PR here or run <code>queue-submit &lt;PR URL&gt;</code> from the CLI.</p>
        {!loading && remoteItems.length === 0 && <div style={{ padding: 14, border: '1px dashed rgba(127,127,127,0.45)', borderRadius: 8, opacity: 0.76 }}>No remote pull requests are queued on this daemon.</div>}
        <div style={{ display: 'grid', gap: 10 }}>{remoteItems.map((item) => <QueueCard key={item.id} item={item} onOpenWorkspace={openWorkspace} />)}</div>
        <section style={{ marginTop: 16, display: 'grid', gap: 10 }} aria-label="Remote daemon nodes">
          <div><h3 style={{ margin: 0, fontSize: 13 }}>Remote daemon nodes</h3><p style={{ margin: '4px 0 0', fontSize: 11, opacity: 0.65 }}>Connections stay loopback-only: Queue Home opens an SSH tunnel only when it needs a remote queue.</p></div>
          <form onSubmit={addNode} style={{ display: 'grid', gridTemplateColumns: 'minmax(100px, 1fr) minmax(120px, 1fr) 72px minmax(120px, 1fr) auto', gap: 6 }} aria-label="Add remote daemon">
            <input value={nodeLabel} onChange={(event) => setNodeLabel(event.target.value)} placeholder="Name" aria-label="Remote daemon name" required style={{ minWidth: 0, padding: '7px 8px', borderRadius: 5, border: '1px solid rgba(127,127,127,0.45)', background: 'transparent', color: 'inherit' }} />
            <input value={nodeTarget} onChange={(event) => setNodeTarget(event.target.value)} placeholder="user@host" aria-label="SSH target" required style={{ minWidth: 0, padding: '7px 8px', borderRadius: 5, border: '1px solid rgba(127,127,127,0.45)', background: 'transparent', color: 'inherit' }} />
            <input value={nodePort} onChange={(event) => setNodePort(event.target.value)} type="number" min="1" max="65535" aria-label="Remote daemon port" required style={{ minWidth: 0, padding: '7px 8px', borderRadius: 5, border: '1px solid rgba(127,127,127,0.45)', background: 'transparent', color: 'inherit' }} />
            <input value={nodeToken} onChange={(event) => setNodeToken(event.target.value)} type="password" placeholder="Daemon token" aria-label="Remote daemon token" required autoComplete="off" style={{ minWidth: 0, padding: '7px 8px', borderRadius: 5, border: '1px solid rgba(127,127,127,0.45)', background: 'transparent', color: 'inherit' }} />
            <button type="submit" disabled={savingNode} style={{ ...buttonStyle, whiteSpace: 'nowrap' }}>{savingNode ? 'Saving…' : 'Add node'}</button>
          </form>
          {nodes.length === 0 && !loading && <div style={{ padding: 11, border: '1px dashed rgba(127,127,127,0.38)', borderRadius: 8, fontSize: 12, opacity: 0.72 }}>No remote daemon is configured. On the remote machine run <code>global-daemon</code>, then add its SSH target, loopback port, and discovery token here.</div>}
          {nodes.map((node) => {
            const aggregate = federation.find((entry) => entry.node.id === node.id);
            const actionIs = (action: string) => nodeAction === `${node.id}:${action}`;
            const state = node.runtime?.state ?? (aggregate?.error ? 'error' : 'disconnected');
            return <section key={node.id} style={{ padding: 11, border: '1px solid rgba(127,127,127,0.28)', borderRadius: 8 }}>
              <div style={{ display: 'flex', gap: 8, alignItems: 'baseline', flexWrap: 'wrap', marginBottom: 5 }}><strong style={{ fontSize: 12 }}>{node.label || node.sshTarget}</strong><span style={{ fontSize: 11, opacity: 0.65 }}>{node.sshTarget} · remote port {node.remotePort}</span><span style={{ fontSize: 11, color: state === 'connected' ? '#2ea043' : state === 'error' ? '#f85149' : undefined }}>{runtimeLabel(node.runtime)}</span></div>
              {(node.runtime?.lastError || aggregate?.error) && <div role="alert" style={{ color: '#f85149', fontSize: 11, marginBottom: 7 }}>{node.runtime?.lastError || aggregate?.error}</div>}
              <div style={{ display: 'flex', flexWrap: 'wrap', gap: 6, marginBottom: aggregate?.items.length ? 9 : 0 }}>
                {state !== 'connected' && <button onClick={() => void actOnNode(node, 'connect')} disabled={!!nodeAction} style={buttonStyle}>{actionIs('connect') ? 'Connecting…' : state === 'error' ? 'Retry tunnel' : 'Connect'}</button>}
                {state === 'connected' && <button onClick={() => void actOnNode(node, 'disconnect')} disabled={!!nodeAction} style={buttonStyle}>{actionIs('disconnect') ? 'Disconnecting…' : 'Disconnect'}</button>}
                <button onClick={() => void actOnNode(node, 'delete')} disabled={!!nodeAction} style={buttonStyle}>{actionIs('delete') ? 'Removing…' : 'Remove node'}</button>
              </div>
              {aggregate?.error ? <div role="alert" style={{ color: '#f85149', fontSize: 12 }}>{aggregate.error}</div> : aggregate && aggregate.items.length === 0 ? <div style={{ fontSize: 12, opacity: 0.65 }}>Connected queue has no items.</div> : aggregate ? <div style={{ display: 'grid', gap: 9 }}>{aggregate.items.map((item) => <QueueCard key={`${node.id}:${item.id}`} item={item} remoteNode={node} />)}</div> : null}
            </section>;
          })}
        </section>
      </section>
    </div>
  </main>;
}
