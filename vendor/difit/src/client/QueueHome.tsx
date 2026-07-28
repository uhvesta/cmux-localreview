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
  identityKey?: string | null;
  reviewTopic?: string | null;
  removedAt?: number | null;
  removedReason?: string | null;
}

interface FederationRuntime {
  state: 'disconnected' | 'connecting' | 'connected' | 'error' | 'disabled';
  localPort: number | null;
  cachedResponses: number;
  lastConnectedAt: number | null;
  lastError: string | null;
  available?: boolean;
  message?: string;
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
interface FederationQueue { node: FederationNode; items: QueueItem[]; error?: string; runtime?: FederationRuntime; }

interface FederationNodeWithRuntime extends FederationNode { runtime?: FederationRuntime; }

type GitHubCapability = 'read' | 'write' | 'copilot';
interface GitHubCapabilityStatus {
  configured: boolean;
  clientId?: string;
  browserOAuthReady?: boolean;
  authenticated: boolean;
  login?: string;
  error?: string;
  loginState: 'idle' | 'waiting' | 'succeeded' | 'failed';
  message?: string;
}
interface GitHubAuthStatus {
  // Browser OAuth is the default through a stable registered callback; device
  // code remains the explicit SSH/headless fallback. Keep this a string so
  // older daemons still render safely.
  provider: string;
  capabilities: Record<GitHubCapability, GitHubCapabilityStatus>;
}

const capabilityLabels: Record<GitHubCapability, { title: string; description: string }> = {
  read: { title: 'PR read', description: 'Resolve and mirror pull requests for local review.' },
  write: { title: 'PR publish', description: 'Publish explicit approval, changes, or comments.' },
  copilot: { title: 'Copilot /ask', description: 'Start fresh, read-only Copilot SDK conversations.' },
};

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

export function QueueCard({ item, remoteNode, onOpenWorkspace, onRequeue, onRemove }: { item: QueueItem; remoteNode?: FederationNode; onOpenWorkspace?: (item: QueueItem) => void; onRequeue?: (item: QueueItem) => void; onRemove?: (item: QueueItem) => void }) {
  return <article style={{ border: '1px solid rgba(127,127,127,0.3)', borderRadius: 8, padding: 14, background: 'rgba(127,127,127,0.04)' }}>
    <div style={{ display: 'flex', gap: 10, alignItems: 'baseline', flexWrap: 'wrap' }}>
      <strong style={{ fontSize: 14 }}>{item.title}</strong>
      <span style={{ color: item.removedAt ? '#8b949e' : statusColor(item.status), fontSize: 11, fontWeight: 600 }}>{item.removedAt ? 'removed' : item.status.replace('_', ' ')}</span>
      <span style={{ fontSize: 11, opacity: 0.72 }}>{item.kind === 'remote' ? 'GitHub PR' : 'local snapshot'}</span>
    </div>
    {item.body && <details style={{ marginTop: 7, fontSize: 12, opacity: 0.8 }}><summary style={{ cursor: 'pointer' }}>Description</summary><p style={{ margin: '6px 0 0', whiteSpace: 'pre-wrap', maxHeight: 180, overflowY: 'auto' }}>{item.body}</p></details>}
    <div style={{ marginTop: 8, fontFamily: 'monospace', fontSize: 11, overflowWrap: 'anywhere', opacity: 0.78 }} title={item.workspacePath}>{item.workspacePath}</div>
    <div style={{ display: 'flex', flexWrap: 'wrap', gap: 7, marginTop: 9, fontSize: 11, opacity: 0.76 }}>
      <span>Copilot feedback: {item.acpState === 'unavailable' ? 'not connected' : item.acpState}</span>{item.agentProvider && <span>originating agent: {item.agentProvider}</span>}{remoteNode && <span>node: {remoteNode.label || remoteNode.sshTarget}</span>}
    </div>
    {item.acpLastError && <div style={{ marginTop: 6, color: '#f85149', fontSize: 11 }}>{item.acpLastError}</div>}
    {item.removedAt && <div style={{ marginTop: 6, color: '#8b949e', fontSize: 11 }}>Removed from the active queue{item.removedReason ? `: ${item.removedReason}` : '.'} Resubmit this workspace and topic to create a new review round.</div>}
    {(onOpenWorkspace || onRequeue || onRemove) && <div style={{ display: 'flex', gap: 7, marginTop: 12 }}>
      {onOpenWorkspace && !item.removedAt && (item.status === 'queued' || item.status === 'in_review') && <button onClick={() => onOpenWorkspace(item)} style={buttonStyle}>Open workspace</button>}
      {onRequeue && !item.removedAt && item.status !== 'queued' && item.status !== 'in_review' && <button onClick={() => onRequeue(item)} style={buttonStyle}>Requeue for another pass</button>}
      {onRemove && !item.removedAt && <button onClick={() => onRemove(item)} style={{ ...buttonStyle, color: '#f85149' }}>Remove</button>}
    </div>}
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
  const [openingLocalPR, setOpeningLocalPR] = useState(false);
  const [localPath, setLocalPath] = useState('');
  const [localTitle, setLocalTitle] = useState('');
  const [localTopic, setLocalTopic] = useState('');
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
  const [githubCapability, setGithubCapability] = useState<GitHubCapability>('read');
  const [githubClientId, setGithubClientId] = useState('');
  const [githubAction, setGithubAction] = useState<GitHubCapability | null>(null);
  const [githubFlow, setGithubFlow] = useState<'loopback' | 'device'>('loopback');
  const [deviceCode, setDeviceCode] = useState<{ capability: GitHubCapability; userCode: string; verificationUri: string } | null>(null);
  const [loopbackLogin, setLoopbackLogin] = useState<{ capability: GitHubCapability; authorizationUrl: string } | null>(null);
  const [showHistory, setShowHistory] = useState(true);

  useEffect(() => { captureDaemonTokenFromLocation(); }, []);
  const refresh = useCallback(async () => {
    setLoading(true); setError(null);
    try {
      const [queueResponse, workspacesResponse, federationResponse, nodesResponse] = await Promise.all([daemonFetch(`/api/queue${showHistory ? '?history=true' : ''}`), daemonFetch('/api/workspaces'), daemonFetch('/api/federation/queue'), daemonFetch('/api/federation/nodes')]);
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
  }, [showHistory]);
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
    if (loopbackLogin && githubAuth?.capabilities[loopbackLogin.capability]?.authenticated) setLoopbackLogin(null);
  }, [githubAuth, loopbackLogin]);
  useEffect(() => {
    const waiting = loopbackLogin !== null || githubAuth && (Object.keys(githubAuth.capabilities) as GitHubCapability[]).some((capability) => githubAuth.capabilities[capability].loginState === 'waiting');
    if (!waiting) return;
    const timer = window.setInterval(() => {
      // Device flow needs a poll request.  Browser-loopback flow completes in
      // its dedicated listener, so only refresh durable status here.
      if (githubAuth) for (const capability of Object.keys(githubAuth.capabilities) as GitHubCapability[]) {
        if (githubAuth.capabilities[capability].loginState === 'waiting') void daemonFetch(`/api/github/auth/${capability}/poll`, { method: 'POST' }).then(() => refreshGitHubAuth());
      }
      void refreshGitHubAuth();
    }, 2_000);
    return () => window.clearInterval(timer);
  }, [githubAuth, loopbackLogin, refreshGitHubAuth]);

  const configureGitHubApp = useCallback(async (event: React.FormEvent<HTMLFormElement>) => {
    event.preventDefault();
    if (!githubClientId.trim()) return;
    setGithubAction(githubCapability); setError(null);
    try {
      const response = await daemonFetch('/api/github/auth/configure', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ capability: githubCapability, clientId: githubClientId.trim() }) });
      const body = (await response.json().catch(() => null)) as { error?: string } | null;
      if (!response.ok) throw new Error(body?.error ?? 'Could not save this GitHub App client ID.');
      setGithubClientId('');
      await refreshGitHubAuth();
    } catch (err) { setError(err instanceof Error ? err.message : 'Could not save this GitHub App client ID.'); }
    finally { setGithubAction(null); }
  }, [githubCapability, githubClientId, refreshGitHubAuth]);

  const startGitHubAuth = useCallback(async (capability: GitHubCapability) => {
    setGithubAction(capability); setError(null);
    try {
      const response = await daemonFetch(`/api/github/auth/${capability}/start`, { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ flow: githubFlow }) });
      const body = (await response.json().catch(() => null)) as { flow?: 'loopback' | 'device'; authorizationUrl?: string; userCode?: string; verificationUri?: string; error?: string } | null;
      if (!response.ok || !body?.flow) throw new Error(body?.error ?? 'Could not start GitHub App authorization.');
      setDeviceCode(null); setLoopbackLogin(null);
      if (body.flow === 'loopback') {
        if (!body.authorizationUrl) throw new Error('The daemon did not return a GitHub browser authorization URL.');
        setLoopbackLogin({ capability, authorizationUrl: body.authorizationUrl });
        // Opening is a convenience only: the durable link below is the
        // recovery path for popup blockers and makes the flow transparent.
        window.open(body.authorizationUrl, '_blank', 'noopener,noreferrer');
      } else {
        if (!body.userCode || !body.verificationUri) throw new Error('The daemon did not return a GitHub device code.');
        setDeviceCode({ capability, userCode: body.userCode, verificationUri: body.verificationUri });
      }
      await refreshGitHubAuth();
    } catch (err) { setError(err instanceof Error ? err.message : 'Could not start GitHub App authorization.'); }
    finally { setGithubAction(null); }
  }, [githubFlow, refreshGitHubAuth]);

  const disconnectGitHubApp = useCallback(async (capability: GitHubCapability) => {
    setGithubAction(capability);
    try { await daemonFetch(`/api/github/auth/${capability}`, { method: 'DELETE' }); await refreshGitHubAuth(); }
    finally { setGithubAction(null); }
  }, [refreshGitHubAuth]);

  const openWorkspace = useCallback(async (item: QueueItem) => {
    setOpening(true); setError(null);
    try {
      const response = await daemonFetch(`/api/queue/${encodeURIComponent(item.id)}/open`, { method: 'POST' });
      if (!response.ok) { const body = (await response.json().catch(() => null)) as { error?: string } | null; throw new Error(body?.error ?? 'Could not open this workspace.'); }
      const body = await response.json() as { reviewUrl?: string };
      window.location.assign(body.reviewUrl ?? `/review?queueItem=${encodeURIComponent(item.id)}`);
    } catch (err) { setError(err instanceof Error ? err.message : 'Could not open this workspace.'); setOpening(false); }
  }, []);
  const openNext = useCallback(async () => {
    setOpening(true); setError(null);
    try { const response = await daemonFetch('/api/queue/open-next', { method: 'POST' }); if (!response.ok) throw new Error('There are no queued reviews to open.'); const body = await response.json() as { reviewUrl?: string }; window.location.assign(body.reviewUrl ?? '/review'); }
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

  // This is deliberately not a queue operation. It materializes the PR in
  // the daemon-owned cache and opens a local-only reviewer, where /ask can
  // inspect the diff. Formal comments, queue feedback, and GitHub publishing
  // remain unavailable unless the reviewer explicitly chooses Add PR later.
  const reviewRemoteLocally = useCallback(async () => {
    const value = remoteUrl.trim();
    if (!value) return;
    try {
      const parsed = new URL(value);
      if (!/^https?:$/.test(parsed.protocol) || !/\/pull\/\d+\/?$/.test(parsed.pathname)) {
        throw new Error('Enter a GitHub pull-request URL, for example https://github.com/owner/repo/pull/123.');
      }
      setOpeningLocalPR(true); setError(null);
      const response = await daemonFetch('/api/local-review/pr', {
        method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ remoteUrl: value }),
      });
      const body = (await response.json().catch(() => null)) as { reviewUrl?: string; error?: string } | null;
      if (!response.ok || !body?.reviewUrl) throw new Error(body?.error ?? 'Could not open that pull request for local review.');
      if (!body.reviewUrl.includes('localOnly=1')) throw new Error('The daemon did not create a local-only review.');
      window.location.assign(body.reviewUrl);
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Could not open that pull request for local review.');
      setOpeningLocalPR(false);
    }
  }, [remoteUrl]);

  const submitLocal = useCallback(async (event: React.FormEvent<HTMLFormElement>) => {
    event.preventDefault();
    const workspacePath = localPath.trim();
    if (!workspacePath) { setError('Enter the local workspace path to snapshot.'); return; }
    setSubmittingLocal(true); setError(null);
    try {
      // Local Queue Home submission is an immutable capture, not a bare
      // queue-row insert.  The generic /api/queue route remains available for
      // imported/remote metadata, but using it here would make the
      // “Snapshotting…” UI label misleading and leave no reproducible bundle.
      const response = await daemonFetch('/api/queue/hook', {
        method: 'POST', headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ workspacePath, title: localTitle.trim() || undefined, topic: localTopic.trim() || undefined }),
      });
      const body = (await response.json().catch(() => null)) as { error?: string } | null;
      if (!response.ok) throw new Error(body?.error ?? 'Could not snapshot and queue that workspace.');
      setLocalTitle(''); setLocalTopic(''); await refresh();
    } catch (err) { setError(err instanceof Error ? err.message : 'Could not snapshot and queue that workspace.'); }
    finally { setSubmittingLocal(false); }
  }, [localPath, localTitle, localTopic, refresh]);

  const removeQueueItem = useCallback(async (item: QueueItem) => {
    if (!window.confirm(`Remove “${item.title}” from the active queue? Its snapshot and decision history stay available only in queue history; resubmitting this topic creates a new review round.`)) return;
    setError(null);
    try {
      const response = await daemonFetch(`/api/queue/${encodeURIComponent(item.id)}`, { method: 'DELETE' });
      const body = (await response.json().catch(() => null)) as { error?: string } | null;
      if (!response.ok) throw new Error(body?.error ?? 'Could not remove this queue item.');
      await refresh();
    } catch (err) { setError(err instanceof Error ? err.message : 'Could not remove this queue item.'); }
  }, [refresh]);
  const requeueItem = useCallback(async (item: QueueItem) => {
    setError(null);
    try {
      const response = await daemonFetch(`/api/queue/${encodeURIComponent(item.id)}/requeue`, { method: 'POST' });
      const body = (await response.json().catch(() => null)) as { error?: string } | null;
      if (!response.ok) throw new Error(body?.error ?? 'Could not requeue this review item.');
      await refresh();
    } catch (err) { setError(err instanceof Error ? err.message : 'Could not requeue this review item.'); }
  }, [refresh]);

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
      <div style={{ display: 'flex', gap: 8, alignItems: 'center', flexWrap: 'wrap' }}>{activeWorkspace && <button onClick={() => window.location.assign('/review')} style={buttonStyle}>Current review</button>}<label style={{ fontSize: 12, display: 'flex', gap: 4, alignItems: 'center' }}><input type="checkbox" checked={showHistory} onChange={(event) => setShowHistory(event.target.checked)} /> Show review history</label><button onClick={() => void refresh()} disabled={loading || opening} style={buttonStyle}>{loading ? 'Loading…' : 'Refresh'}</button><button onClick={() => void openNext()} disabled={opening || queuedCount === 0} style={{ ...buttonStyle, borderColor: '#2ea043' }}>Open next ({queuedCount})</button></div>
    </header>
    {error && <div role="alert" style={{ marginBottom: 16, padding: '10px 12px', borderRadius: 6, color: '#f85149', border: '1px solid rgba(248,81,73,0.5)' }}><div>{error}</div><div style={{ display: 'flex', alignItems: 'center', gap: 8, flexWrap: 'wrap', marginTop: 8 }}><button onClick={() => void refresh()} disabled={loading || opening} style={buttonStyle}>{loading ? 'Retrying…' : 'Retry Queue Home'}</button><span style={{ fontSize: 11, opacity: 0.82 }}>If it persists, run <code>localreview daemon status</code> and then <code>localreview daemon run --port 0</code>.</span></div></div>}
    {authRequired && <form onSubmit={(event) => { event.preventDefault(); if (saveDaemonToken(daemonToken)) { setDaemonToken(''); void refresh(); } }} style={{ display: 'flex', gap: 8, alignItems: 'center', margin: '0 0 16px', padding: 12, border: '1px solid rgba(210,153,34,0.55)', borderRadius: 6 }} aria-label="Connect Queue Home to local daemon">
      <span style={{ fontSize: 12, opacity: 0.82 }}>Paste the daemon token from <code>localreview-open --home</code>:</span>
      <input value={daemonToken} onChange={(event) => setDaemonToken(event.target.value)} type="password" autoComplete="off" aria-label="Daemon bearer token" placeholder="Daemon token" style={{ flex: 1, minWidth: 180, padding: '7px 8px', borderRadius: 5, border: '1px solid rgba(127,127,127,0.45)', background: 'transparent', color: 'inherit' }} />
      <button type="submit" disabled={!daemonToken.trim()} style={buttonStyle}>Connect</button>
    </form>}
    {githubAuth && <section aria-label="GitHub OAuth connections" style={{ margin: '0 0 18px', padding: 12, border: '1px solid rgba(127,127,127,0.42)', borderRadius: 8 }}>
      <div style={{ display: 'flex', justifyContent: 'space-between', gap: 10, alignItems: 'baseline', flexWrap: 'wrap' }}><div><strong>GitHub connections</strong><p style={{ margin: '4px 0 0', fontSize: 12, opacity: 0.74 }}>Dedicated GitHub OAuth connections using PKCE. Browser sign-in is the default; device code is the SSH/headless fallback. The daemon keeps access tokens in the system secret store—this page never receives a GitHub token.</p></div><button onClick={() => void refreshGitHubAuth()} disabled={githubAuthLoading} style={buttonStyle}>{githubAuthLoading ? 'Checking…' : 'Refresh status'}</button></div>
      <div style={{ display: 'grid', gridTemplateColumns: 'repeat(auto-fit, minmax(225px, 1fr))', gap: 9, marginTop: 10 }}>
        {(Object.keys(capabilityLabels) as GitHubCapability[]).map((capability) => {
          const status = githubAuth.capabilities[capability];
          const waiting = status.loginState === 'waiting';
          return <article key={capability} style={{ border: '1px solid rgba(127,127,127,0.3)', padding: 10, borderRadius: 6 }}>
            <div style={{ display: 'flex', justifyContent: 'space-between', gap: 7 }}><strong style={{ fontSize: 13 }}>{capabilityLabels[capability].title}</strong><span style={{ fontSize: 11, color: status.authenticated ? '#2ea043' : status.configured ? '#d29922' : '#8b949e' }}>{status.authenticated ? `Connected${status.login ? ` @${status.login}` : ''}` : status.configured ? (waiting ? 'Waiting…' : 'Not connected') : 'Not configured'}</span></div>
            <p style={{ margin: '5px 0 9px', fontSize: 11, opacity: 0.7 }}>{status.message ?? status.error ?? capabilityLabels[capability].description}{status.configured && status.clientId ? <> OAuth client: <code>{status.clientId}</code>.</> : null}</p>
            <div style={{ display: 'flex', gap: 7, flexWrap: 'wrap' }}>
              {status.configured && !status.authenticated && <button onClick={() => void startGitHubAuth(capability)} disabled={githubAction !== null} style={{ ...buttonStyle, borderColor: '#2ea043' }}>{githubAction === capability ? 'Opening…' : waiting ? `Restart ${githubFlow === 'loopback' ? 'browser login' : 'device flow'}` : 'Connect'}</button>}
              {status.authenticated && <button onClick={() => void disconnectGitHubApp(capability)} disabled={githubAction !== null} style={buttonStyle}>{githubAction === capability ? 'Disconnecting…' : 'Disconnect'}</button>}
            </div>
          </article>;
        })}
      </div>
      <label style={{ display: 'flex', gap: 6, alignItems: 'center', marginTop: 11, fontSize: 12 }}>
        Authorization method
        <select value={githubFlow} onChange={(event) => setGithubFlow(event.target.value as 'loopback' | 'device')} style={{ ...buttonStyle, padding: '5px 7px' }} aria-label="GitHub authorization method">
          <option value="loopback">Browser OAuth (recommended; registered callback required)</option>
          <option value="device">Device code (SSH/headless fallback)</option>
        </select>
      </label>
      <form onSubmit={configureGitHubApp} style={{ display: 'flex', gap: 7, flexWrap: 'wrap', marginTop: 11 }} aria-label="Configure GitHub OAuth client ID">
        <select value={githubCapability} onChange={(event) => setGithubCapability(event.target.value as GitHubCapability)} aria-label="GitHub OAuth capability" style={{ ...buttonStyle, padding: '6px 7px' }}>{(Object.keys(capabilityLabels) as GitHubCapability[]).map((capability) => <option key={capability} value={capability}>{capabilityLabels[capability].title}</option>)}</select>
        <input value={githubClientId} onChange={(event) => setGithubClientId(event.target.value)} placeholder="GitHub OAuth client ID (Iv1.… )" autoComplete="off" aria-label="GitHub OAuth client ID" style={{ flex: 1, minWidth: 220, padding: '6px 8px', borderRadius: 5, border: '1px solid rgba(127,127,127,0.45)', background: 'transparent', color: 'inherit' }} />
        <button type="submit" disabled={!githubClientId.trim() || githubAction !== null} style={buttonStyle}>{githubAction === githubCapability ? 'Saving…' : 'Save OAuth client ID'}</button>
      </form>
      <details style={{ marginTop: 9, fontSize: 11, opacity: 0.78 }}>
        <summary style={{ cursor: 'pointer' }}>Set up browser OAuth safely</summary>
        <ol style={{ margin: '7px 0 0', paddingInlineStart: 19 }}>
          <li>Create a dedicated GitHub <strong>OAuth App</strong> registration for the selected capability—not a PAT, <code>gh</code> login, or GitHub App installation.</li>
          <li>Set its authorization callback URL exactly to <code>http://127.0.0.1:8787/oauth/callback</code>. Enable Device Flow only when this machine needs the SSH/headless fallback.</li>
          <li>Save only the public client ID above, select <strong>Browser OAuth</strong>, then click Connect. The loopback flow uses PKCE; no client secret is entered, stored, or shipped.</li>
          <li>Review the GitHub consent screen before approving. Tokens are saved only by the daemon in the OS secret store and are never returned to this page.</li>
          <li>This setup never falls back to <code>gh login</code>, a PAT, environment credentials, or an existing Copilot CLI session.</li>
        </ol>
      </details>
      {loopbackLogin && <div role="status" style={{ marginTop: 10, padding: 10, border: '1px solid rgba(88,166,255,0.5)', borderRadius: 6, fontSize: 12 }}>Continue <strong>{capabilityLabels[loopbackLogin.capability].title}</strong> in the GitHub browser tab. If it did not open, <a href={loopbackLogin.authorizationUrl} target="_blank" rel="noreferrer">open the secure authorization page</a>. This page will refresh after the loopback callback completes.</div>}
      {deviceCode && <div role="status" style={{ marginTop: 10, padding: 10, border: '1px solid rgba(88,166,255,0.5)', borderRadius: 6, fontSize: 12 }}>Authorize <strong>{capabilityLabels[deviceCode.capability].title}</strong> at <a href={deviceCode.verificationUri} target="_blank" rel="noreferrer">github.com/login/device</a> with code <code style={{ fontWeight: 700, fontSize: 14 }}>{deviceCode.userCode}</code>. This page checks automatically; no token is shown or pasted.</div>}
    </section>}
    <div style={{ display: 'grid', gridTemplateColumns: 'repeat(auto-fit, minmax(360px, 1fr))', gap: 22, alignItems: 'start' }}>
      <section aria-label="Local review queue">
        <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'baseline', marginBottom: 10 }}><h2 style={{ fontSize: 16, margin: 0 }}>Local</h2><span style={{ fontSize: 12, opacity: 0.62 }}>{localItems.length} snapshot{localItems.length === 1 ? '' : 's'}</span></div>
        <form onSubmit={submitLocal} style={{ display: 'grid', gridTemplateColumns: 'minmax(0, 1fr) minmax(100px, 0.45fr) minmax(100px, 0.35fr) auto', gap: 7, marginBottom: 10 }} aria-label="Submit local workspace">
          <input value={localPath} onChange={(event) => setLocalPath(event.target.value)} placeholder="/absolute/path/to/workspace" aria-label="Local workspace path" required style={{ minWidth: 0, padding: '7px 8px', borderRadius: 5, border: '1px solid rgba(127,127,127,0.45)', background: 'transparent', color: 'inherit' }} />
          <input value={localTitle} onChange={(event) => setLocalTitle(event.target.value)} placeholder="Review title (optional)" aria-label="Review title" style={{ minWidth: 0, padding: '7px 8px', borderRadius: 5, border: '1px solid rgba(127,127,127,0.45)', background: 'transparent', color: 'inherit' }} />
          <input value={localTopic} onChange={(event) => setLocalTopic(event.target.value)} placeholder="Stable topic (optional)" aria-label="Stable review topic" style={{ minWidth: 0, padding: '7px 8px', borderRadius: 5, border: '1px solid rgba(127,127,127,0.45)', background: 'transparent', color: 'inherit' }} />
          <button type="submit" disabled={submittingLocal || !localPath.trim()} style={{ ...buttonStyle, whiteSpace: 'nowrap' }}>{submittingLocal ? 'Snapshotting…' : 'Submit local'}</button>
        </form>
        <p style={{ margin: '0 0 10px', fontSize: 12, opacity: 0.68 }}>Immutable snapshots submitted from this machine. Use a stable topic when one workspace carries more than one review stream.</p>
        {loading ? <p role="status" style={{ opacity: 0.65 }}>Loading queue…</p> : localItems.length === 0 ? <div style={{ padding: 14, border: '1px dashed rgba(127,127,127,0.45)', borderRadius: 8, opacity: 0.76 }}>Nothing local is queued. Submit a path above, or run <code>localreview-submit &lt;path&gt; --topic &lt;name&gt;</code> to capture cmux and Copilot metadata atomically.</div> : <div style={{ display: 'grid', gap: 10 }}>{localItems.map((item) => <QueueCard key={item.id} item={item} onOpenWorkspace={openWorkspace} onRequeue={requeueItem} onRemove={removeQueueItem} />)}</div>}
      </section>
      <section aria-label="Remote pull-request queue">
        <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'baseline', marginBottom: 10 }}><h2 style={{ fontSize: 16, margin: 0 }}>Remote</h2><span style={{ fontSize: 12, opacity: 0.62 }}>{remoteItems.length} pull request{remoteItems.length === 1 ? '' : 's'}</span></div>
        <form onSubmit={submitRemote} style={{ display: 'flex', gap: 7, marginBottom: 7 }} aria-label="Add remote pull request">
          <label style={{ position: 'absolute', width: 1, height: 1, overflow: 'hidden', clipPath: 'inset(50%)' }} htmlFor="remote-pr-url">Pull request URL</label>
          <input id="remote-pr-url" type="url" value={remoteUrl} onChange={(event) => setRemoteUrl(event.target.value)} placeholder="https://github.com/owner/repo/pull/123" style={{ minWidth: 0, flex: 1, padding: '7px 8px', borderRadius: 5, border: '1px solid rgba(127,127,127,0.45)', background: 'transparent', color: 'inherit' }} />
          <button type="submit" disabled={submittingRemote || openingLocalPR || !remoteUrl.trim()} style={{ ...buttonStyle, whiteSpace: 'nowrap' }}>{submittingRemote ? 'Adding…' : 'Add PR'}</button>
          <button type="button" onClick={() => void reviewRemoteLocally()} disabled={submittingRemote || openingLocalPR || !remoteUrl.trim()} style={{ ...buttonStyle, borderColor: '#58a6ff', whiteSpace: 'nowrap' }}>{openingLocalPR ? 'Opening…' : 'Review locally'}</button>
        </form>
        <p style={{ margin: '0 0 10px', fontSize: 12, opacity: 0.68 }}><strong>Review locally</strong> mirrors the PR for diff and <code>/ask</code> only: it creates no queue item and cannot publish feedback. <strong>Add PR</strong> is the separate formal queue action. CLI: <code>localreview open --pr &lt;PR URL&gt;</code> or <code>localreview queue-submit &lt;PR URL&gt;</code>.</p>
        {!loading && remoteItems.length === 0 && <div style={{ padding: 14, border: '1px dashed rgba(127,127,127,0.45)', borderRadius: 8, opacity: 0.76 }}>No remote pull requests are queued on this daemon.</div>}
        <div style={{ display: 'grid', gap: 10 }}>{remoteItems.map((item) => <QueueCard key={item.id} item={item} onOpenWorkspace={openWorkspace} onRequeue={requeueItem} onRemove={removeQueueItem} />)}</div>
        <section style={{ marginTop: 16, display: 'grid', gap: 10 }} aria-label="Remote daemon nodes">
          <div><h3 style={{ margin: 0, fontSize: 13 }}>Remote daemon nodes</h3><p style={{ margin: '4px 0 0', fontSize: 11, opacity: 0.65 }}>Save loopback-only SSH node details here. This native build clearly marks SSH federation unavailable until a transport-enabled release is installed; it does not fabricate a tunnel or remote queue.</p></div>
          <form onSubmit={addNode} style={{ display: 'grid', gridTemplateColumns: 'minmax(100px, 1fr) minmax(120px, 1fr) 72px minmax(120px, 1fr) auto', gap: 6 }} aria-label="Add remote daemon">
            <input value={nodeLabel} onChange={(event) => setNodeLabel(event.target.value)} placeholder="Name" aria-label="Remote daemon name" required style={{ minWidth: 0, padding: '7px 8px', borderRadius: 5, border: '1px solid rgba(127,127,127,0.45)', background: 'transparent', color: 'inherit' }} />
            <input value={nodeTarget} onChange={(event) => setNodeTarget(event.target.value)} placeholder="user@host" aria-label="SSH target" required style={{ minWidth: 0, padding: '7px 8px', borderRadius: 5, border: '1px solid rgba(127,127,127,0.45)', background: 'transparent', color: 'inherit' }} />
            <input value={nodePort} onChange={(event) => setNodePort(event.target.value)} type="number" min="1" max="65535" aria-label="Remote daemon port" required style={{ minWidth: 0, padding: '7px 8px', borderRadius: 5, border: '1px solid rgba(127,127,127,0.45)', background: 'transparent', color: 'inherit' }} />
            <input value={nodeToken} onChange={(event) => setNodeToken(event.target.value)} type="password" placeholder="Daemon token" aria-label="Remote daemon token" required autoComplete="off" style={{ minWidth: 0, padding: '7px 8px', borderRadius: 5, border: '1px solid rgba(127,127,127,0.45)', background: 'transparent', color: 'inherit' }} />
            <button type="submit" disabled={savingNode} style={{ ...buttonStyle, whiteSpace: 'nowrap' }}>{savingNode ? 'Saving…' : 'Add node'}</button>
          </form>
          {nodes.length === 0 && !loading && <div style={{ padding: 11, border: '1px dashed rgba(127,127,127,0.38)', borderRadius: 8, fontSize: 12, opacity: 0.72 }}>No remote daemon is configured. On the remote machine run <code>localreviewd</code>, then add its SSH target, loopback port, and discovery token here. A transport-enabled release is required before this daemon can connect.</div>}
          {nodes.map((node) => {
            const aggregate = federation.find((entry) => entry.node.id === node.id);
            const actionIs = (action: string) => nodeAction === `${node.id}:${action}`;
            const runtime = node.runtime ?? aggregate?.runtime;
            const state = runtime?.state ?? (aggregate?.error ? 'error' : 'disconnected');
            const transportAvailable = runtime?.available === true;
            return <section key={node.id} style={{ padding: 11, border: '1px solid rgba(127,127,127,0.28)', borderRadius: 8 }}>
              <div style={{ display: 'flex', gap: 8, alignItems: 'baseline', flexWrap: 'wrap', marginBottom: 5 }}><strong style={{ fontSize: 12 }}>{node.label || node.sshTarget}</strong><span style={{ fontSize: 11, opacity: 0.65 }}>{node.sshTarget} · remote port {node.remotePort}</span><span style={{ fontSize: 11, color: state === 'connected' ? '#2ea043' : state === 'error' ? '#f85149' : undefined }}>{runtimeLabel(runtime)}</span></div>
              {(runtime?.lastError || aggregate?.error || runtime?.message) && <div role="alert" style={{ color: transportAvailable ? '#d29922' : '#f85149', fontSize: 11, marginBottom: 7 }}>{runtime?.lastError || aggregate?.error || runtime?.message}</div>}
              <div style={{ display: 'flex', flexWrap: 'wrap', gap: 6, marginBottom: aggregate?.items.length ? 9 : 0 }}>
                {transportAvailable && state !== 'connected' && <button onClick={() => void actOnNode(node, 'connect')} disabled={!!nodeAction} style={buttonStyle}>{actionIs('connect') ? 'Connecting…' : state === 'error' ? 'Retry tunnel' : 'Connect'}</button>}
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
