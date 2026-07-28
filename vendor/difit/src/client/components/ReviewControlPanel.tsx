import { useCallback, useEffect, useMemo, useState } from 'react';

import { daemonFetch } from '../services/daemonAuth';

type QueueStatus = 'queued' | 'in_review' | 'changes_requested' | 'approved' | 'completed';
type AcpState = 'unavailable' | 'connecting' | 'idle' | 'busy' | 'error';

interface QueueItem {
  id: string;
  title: string;
  body: string;
  workspacePath: string;
  kind: 'local' | 'remote';
  remoteUrl: string | null;
  status: QueueStatus;
  position: number;
  agentId: string | null;
  agentProvider: string | null;
  copilotSessionId: string | null;
  acpHost: string | null;
  acpPort: number | null;
  acpSessionId: string | null;
  agentKind: string | null;
  acpState: AcpState;
  acpLastError: string | null;
  snapshotManifestPath: string | null;
  snapshotManifest: Record<string, unknown> | null;
  feedbackTarget: string | null;
  baseRef: string | null;
  provenance: Record<string, unknown> | null;
  sourceFingerprint: string | null;
  supersedesId: string | null;
  identityKey: string | null;
  reviewTopic: string | null;
  removedAt: number | null;
  removedReason: string | null;
  decisionBody: string | null;
  createdAt: number;
  updatedAt: number;
}

interface Feedback {
  id: number;
  body: string;
  path: string | null;
  line: number | null;
  createdAt: number;
  deliveredAt: number | null;
}

interface Decision {
  id: number;
  status: QueueStatus | 'requeued';
  body: string | null;
  createdAt: number;
}

interface QueueDetail { item: QueueItem; feedback: Feedback[]; decisions: Decision[]; }

interface ReproductionPlan {
  snapshot: { id: string; manifestPath: string; repositories: number } | null;
  existingAcp: { host: string; port: number; sessionId: string; state: AcpState; error: string | null; canAttemptResume: boolean } | null;
  copilotSessionId: string | null;
  commands: { reproduceSnapshot: string; reproduceCopilot: string; freshAcp: string } | null;
  notes: string[];
}

interface RemoteStatus {
  pullRequest: {
    number: number;
    repository: string;
    state: string;
    isDraft: boolean;
    headRefName: string;
    headSha: string;
    baseRefName: string;
    baseSha: string;
  };
  cache: { mirrorPath: string; mirrorPresent: boolean; worktreePath: string; worktreePresent: boolean };
  recovery: { refresh: string; cleanup: string; reAdd: string };
}

interface Agent {
  id: string;
  provider: string;
  workspacePath: string | null;
  reviewSessionId: string | null;
  status: string;
  metadata: { surfaceId?: string; lastError?: string };
}

type PanelTab = 'queue' | 'agents';

interface ReviewControlPanelProps {
  collapsed: boolean;
  onToggleCollapsed: () => void;
  refreshNonce: number;
}

const buttonStyle = {
  fontSize: 11,
  padding: '4px 8px',
  borderRadius: 4,
  border: '1px solid rgba(127,127,127,0.4)',
  background: 'transparent',
  color: 'inherit',
  cursor: 'pointer',
} as const;
const muted = { fontSize: 10, opacity: 0.66 } as const;
const cardStyle = { border: '1px solid rgba(127,127,127,0.28)', borderRadius: 5, padding: 8, fontSize: 12 } as const;

function readable(value: string | null | undefined) { return value || '—'; }
function time(value: number | null | undefined) { return value ? new Date(value).toLocaleString() : '—'; }
function acpLabel(item: QueueItem) {
  return item.acpHost && item.acpPort && item.acpSessionId
    ? `${item.acpState} · ${item.acpHost}:${item.acpPort} · ${item.acpSessionId}`
    : 'unavailable';
}

export function ReviewControlPanel({ collapsed, onToggleCollapsed, refreshNonce }: ReviewControlPanelProps) {
  const boundQueueItemId = typeof window === 'undefined' ? null : new URLSearchParams(window.location.search).get('queueItem');
  const [tab, setTab] = useState<PanelTab>('queue');
  const [queue, setQueue] = useState<QueueItem[]>([]);
  const [agents, setAgents] = useState<Agent[]>([]);
  const [selectedId, setSelectedId] = useState<string | null>(null);
  const [detail, setDetail] = useState<QueueDetail | null>(null);
  const [plan, setPlan] = useState<ReproductionPlan | null>(null);
  const [remoteStatus, setRemoteStatus] = useState<RemoteStatus | null>(null);
  const [feedbackBody, setFeedbackBody] = useState('');
  const [deliveryPolicy, setDeliveryPolicy] = useState<'queue' | 'interrupt'>('queue');
  const [working, setWorking] = useState<string | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [notice, setNotice] = useState<string | null>(null);
  const [loading, setLoading] = useState(false);

  const refresh = useCallback(async () => {
    setLoading(true);
    setError(null);
    try {
      const [queueResponse, agentsResponse] = await Promise.all([daemonFetch('/api/queue?history=true'), daemonFetch('/api/agents')]);
      if (!queueResponse.ok || !agentsResponse.ok) {
        throw new Error(queueResponse.status === 401 || agentsResponse.status === 401
          ? 'Open this review with localreview-open to connect the daemon controls.'
          : 'Queue or agent registry is unavailable. Start the global daemon, then refresh.');
      }
      const queueData = (await queueResponse.json()) as { items?: QueueItem[] };
      const agentData = (await agentsResponse.json()) as { agents?: Agent[] };
      const items = Array.isArray(queueData.items) ? queueData.items : [];
      setQueue(items);
      setAgents(Array.isArray(agentData.agents) ? agentData.agents : []);
      setSelectedId((current) => boundQueueItemId && items.some((item) => item.id === boundQueueItemId)
        ? boundQueueItemId
        : current && items.some((item) => item.id === current) ? current : items[0]?.id ?? null);
    } catch (err) { setError(err instanceof Error ? err.message : 'Failed to load review controls.'); }
    finally { setLoading(false); }
  }, [boundQueueItemId]);

  const selected = useMemo(() => queue.find((item) => item.id === selectedId) ?? null, [queue, selectedId]);

  const loadDetail = useCallback(async (id: string) => {
    try {
      const selectedItem = queue.find((item) => item.id === id);
      const [detailResponse, reproduceResponse, remoteResponse] = await Promise.all([
        daemonFetch(`/api/queue/${encodeURIComponent(id)}`),
        daemonFetch(`/api/queue/${encodeURIComponent(id)}/reproduce`),
        selectedItem?.kind === 'remote' ? daemonFetch(`/api/queue/${encodeURIComponent(id)}/remote-status`) : Promise.resolve(null),
      ]);
      if (!detailResponse.ok) throw new Error('Could not load this queue item.');
      setDetail(await detailResponse.json() as QueueDetail);
      setPlan(reproduceResponse.ok ? await reproduceResponse.json() as ReproductionPlan : null);
      setRemoteStatus(remoteResponse?.ok ? await remoteResponse.json() as RemoteStatus : null);
    } catch (err) { setError(err instanceof Error ? err.message : 'Could not load queue-item details.'); }
  }, [queue]);

  useEffect(() => { void refresh(); }, [refresh, refreshNonce]);
  useEffect(() => { if (selectedId) void loadDetail(selectedId); else { setDetail(null); setPlan(null); setRemoteStatus(null); } }, [selectedId, loadDetail]);

  const run = useCallback(async (key: string, operation: () => Promise<Response>, success: string) => {
    if (working) return;
    setWorking(key); setError(null); setNotice(null);
    try {
      const response = await operation();
      const payload = await response.json().catch(() => ({})) as { error?: string };
      if (!response.ok) throw new Error(payload.error ?? 'The action could not be completed.');
      setNotice(success);
      await refresh();
      if (selectedId) await loadDetail(selectedId);
    } catch (err) { setError(err instanceof Error ? err.message : 'The action could not be completed.'); }
    finally { setWorking(null); }
  }, [working, refresh, selectedId, loadDetail]);

  const copyFeedbackPrompt = useCallback(async (item: QueueItem) => {
    setWorking(`copy:${item.id}`); setError(null);
    try {
      const response = await daemonFetch(`/api/queue/${encodeURIComponent(item.id)}/feedback/prompt?includeDelivered=true`);
      if (!response.ok) throw new Error('Could not create a feedback prompt.');
      await navigator.clipboard.writeText(await response.text());
      setNotice('Feedback prompt copied. It is safe to paste into any agent manually.');
    } catch (err) { setError(err instanceof Error ? err.message : 'Could not copy feedback prompt.'); }
    finally { setWorking(null); }
  }, []);

  const copyText = useCallback(async (text: string, label: string) => {
    try {
      await navigator.clipboard.writeText(text);
      setNotice(`${label} copied to the clipboard.`);
    } catch (err) { setError(err instanceof Error ? err.message : `Could not copy ${label.toLowerCase()}.`); }
  }, []);

  const openNext = useCallback(() => run('open-next', () => daemonFetch('/api/queue/open-next', { method: 'POST' }), 'Opened the next queued review.'), [run]);
  const decide = useCallback((item: QueueItem, decision: Extract<QueueStatus, 'approved' | 'changes_requested' | 'completed'>) =>
    run(`decision:${item.id}`, () => daemonFetch(`/api/queue/${encodeURIComponent(item.id)}/decision`, {
      method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ decision }),
    }), `Marked ${decision.replace('_', ' ')}.`), [run]);
  const requeue = useCallback((item: QueueItem) => run(`requeue:${item.id}`, () => daemonFetch(`/api/queue/${encodeURIComponent(item.id)}/requeue`, { method: 'POST' }), 'Requeued at the end of the queue.'), [run]);
  const remove = useCallback((item: QueueItem) => {
    if (!window.confirm(`Remove “${item.title}” without reviewing it? You can submit the same topic again later as a fresh review round.`)) return;
    void run(`remove:${item.id}`, () => daemonFetch(`/api/queue/${encodeURIComponent(item.id)}`, { method: 'DELETE' }), 'Removed from the active queue.');
  }, [run]);
  const reorder = useCallback((item: QueueItem, position: number) => run(`reorder:${item.id}`, () => daemonFetch(`/api/queue/${encodeURIComponent(item.id)}/reorder`, {
    method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ position }),
  }), `Moved to position ${position}.`), [run]);
  const refreshRemote = useCallback((item: QueueItem) => run(`refresh:${item.id}`, () => daemonFetch(`/api/queue/${encodeURIComponent(item.id)}/refresh`, { method: 'POST' }), 'Remote PR refreshed.'), [run]);
  const publishComment = useCallback((item: QueueItem) => run(`publish-comment:${item.id}`, () => daemonFetch(`/api/queue/${encodeURIComponent(item.id)}/publish-comment`, {
    method: 'POST', headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ body: feedbackBody.trim() || 'Reviewed with cmux-localreview.' }),
  }), 'Published a non-blocking GitHub review comment.'), [feedbackBody, run]);
  const cleanupRemote = useCallback((item: QueueItem) => run(`cleanup:${item.id}`, () => daemonFetch(`/api/queue/${encodeURIComponent(item.id)}/cleanup`, {
    method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ removeMirror: false }),
  }), 'Managed remote worktree removed; the mirror cache was retained.'), [run]);
  const addFeedback = useCallback((item: QueueItem) => {
    const body = feedbackBody.trim();
    if (!body) { setError('Write feedback before adding it to the formal review queue.'); return; }
    void run(`feedback:${item.id}`, async () => {
      const response = await daemonFetch(`/api/queue/${encodeURIComponent(item.id)}/feedback`, {
        method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ body }),
      });
      if (response.ok) setFeedbackBody('');
      return response;
    }, 'Formal feedback added. It remains separate from /ask conversations.');
  }, [feedbackBody, run]);
  const deliverFeedback = useCallback((item: QueueItem) => run(`deliver:${item.id}`, () => daemonFetch(`/api/queue/${encodeURIComponent(item.id)}/deliver-feedback`, {
    method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ policy: deliveryPolicy }),
  }), `Sent undelivered feedback through ACP using the ${deliveryPolicy} policy.`), [deliveryPolicy, run]);
  const exportPackage = useCallback((item: QueueItem) => {
    const destination = window.prompt('Export review package to this new or existing directory:');
    if (!destination) return;
    void run(`export:${item.id}`, () => daemonFetch(`/api/queue/${encodeURIComponent(item.id)}/export`, {
      method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ destination }),
    }), 'Portable review package exported.');
  }, [run]);
  const reconnectAgent = useCallback((agent: Agent) => run(`reconnect:${agent.id}`, () => daemonFetch(`/api/agents/${encodeURIComponent(agent.id)}/reconnect`, { method: 'POST' }), `Reconnect requested for ${agent.provider}.`), [run]);

  if (collapsed) return <button onClick={onToggleCollapsed} title="Open review queue and agents" style={{ ...buttonStyle, position: 'fixed', left: 12, bottom: 12, zIndex: 60, borderRadius: 20 }}>Review{queue.length ? ` (${queue.filter((item) => item.status === 'queued').length})` : ''}</button>;

  const undelivered = detail?.feedback.filter((entry) => !entry.deliveredAt).length ?? 0;
  const disabled = !!working;
  return (
    <section style={{ position: 'fixed', left: 0, bottom: 0, width: 440, maxHeight: '65vh', zIndex: 55, display: 'flex', flexDirection: 'column', background: 'var(--bg-primary, #161b22)', border: '1px solid rgba(127,127,127,0.35)', borderLeft: 'none', borderBottom: 'none', borderTopRightRadius: 8 }} aria-label="Review controls">
      <div style={{ display: 'flex', alignItems: 'center', gap: 6, padding: '8px 10px', borderBottom: '1px solid rgba(127,127,127,0.3)' }}>
        <button onClick={() => setTab('queue')} style={{ ...buttonStyle, background: tab === 'queue' ? 'rgba(127,127,127,0.22)' : 'transparent' }}>Queue</button>
        <button onClick={() => setTab('agents')} style={{ ...buttonStyle, background: tab === 'agents' ? 'rgba(127,127,127,0.22)' : 'transparent' }}>Agents ({agents.length})</button>
        <button onClick={() => void refresh()} style={{ ...buttonStyle, marginLeft: 'auto' }} disabled={loading || disabled}>{loading ? 'Loading…' : 'Refresh'}</button>
        <button onClick={onToggleCollapsed} style={buttonStyle} aria-label="Close review controls">Close</button>
      </div>
      {error && <div role="alert" style={{ color: '#e5534b', fontSize: 11, padding: '7px 10px' }}>{error}</div>}
      {notice && <div role="status" style={{ color: '#56d364', fontSize: 11, padding: '7px 10px' }}>{notice}</div>}
      <div style={{ overflowY: 'auto', padding: 10, display: 'flex', flexDirection: 'column', gap: 8 }}>
        {tab === 'agents' ? <>
          {agents.length === 0 && <span style={{ fontSize: 12, opacity: 0.65 }}>No registered agents. Register an explicit cmux surface before using terminal /btw.</span>}
          {agents.map((agent) => <article key={agent.id} style={cardStyle}>
            <strong>{agent.provider}</strong> <span style={muted}>{agent.status}</span>
            <div style={{ fontFamily: 'monospace', ...muted, marginTop: 4 }}>{agent.id}</div>
            {agent.workspacePath && <div style={{ ...muted, marginTop: 3 }}>{agent.workspacePath}</div>}
            {agent.metadata?.surfaceId && <div style={{ fontFamily: 'monospace', ...muted, marginTop: 3 }}>cmux surface: {agent.metadata.surfaceId}</div>}
            {agent.metadata?.lastError && <div style={{ color: '#e5534b', ...muted, marginTop: 3 }}>{agent.metadata.lastError}</div>}
            <button onClick={() => void reconnectAgent(agent)} style={{ ...buttonStyle, marginTop: 6 }} disabled={!agent.metadata?.surfaceId || disabled}>Reconnect</button>
          </article>)}
        </> : <>
          <div style={{ display: 'flex', gap: 6, flexWrap: 'wrap' }}>
            <button onClick={() => void openNext()} style={buttonStyle} disabled={disabled || !queue.some((item) => item.status === 'queued')}>Open next queued review</button>
            <span style={{ ...muted, alignSelf: 'center' }}>{queue.filter((item) => item.status === 'queued').length} queued</span>
          </div>
          {queue.length === 0 && <span style={{ fontSize: 12, opacity: 0.65 }}>No queue items yet. Submit a local snapshot or a remote PR URL from Queue Home.</span>}
          <div style={{ maxHeight: 180, overflowY: 'auto', display: 'flex', flexDirection: 'column', gap: 5 }} aria-label="Queue items">
            {queue.map((item) => <button key={item.id} onClick={() => { if (!boundQueueItemId) setSelectedId(item.id); }} disabled={Boolean(boundQueueItemId && item.id !== boundQueueItemId)} style={{ ...cardStyle, textAlign: 'left', background: item.id === selectedId ? 'rgba(88,166,255,0.12)' : 'transparent', color: 'inherit', cursor: boundQueueItemId && item.id !== boundQueueItemId ? 'not-allowed' : 'pointer', opacity: boundQueueItemId && item.id !== boundQueueItemId ? 0.5 : 1 }}>
              <strong>{item.title}</strong> <span style={{ color: '#58a6ff', marginLeft: 6 }}>{item.status.replace('_', ' ')}</span>
              <div style={{ ...muted, marginTop: 3, overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>{item.kind === 'remote' ? item.remoteUrl : item.workspacePath}</div>
            </button>)}
          </div>
          {selected && <article style={cardStyle} aria-label={`Details for ${selected.title}`}>
            <div style={{ display: 'flex', gap: 6, alignItems: 'baseline' }}><strong>{selected.title}</strong><span style={{ marginLeft: 'auto', color: '#58a6ff' }}>{selected.status.replace('_', ' ')}</span></div>
            {selected.body && <p style={{ ...muted, whiteSpace: 'pre-wrap' }}>{selected.body}</p>}
            <div style={{ display: 'flex', flexWrap: 'wrap', gap: 5, marginTop: 8 }}>
              {selected.status === 'in_review' && <>
                <button onClick={() => void decide(selected, 'approved')} style={buttonStyle} disabled={disabled}>Approve</button>
                <button onClick={() => void decide(selected, 'changes_requested')} style={buttonStyle} disabled={disabled}>Request changes</button>
                <button onClick={() => void decide(selected, 'completed')} style={buttonStyle} disabled={disabled}>Complete</button>
              </>}
              {selected.status !== 'in_review' && <button onClick={() => void requeue(selected)} style={buttonStyle} disabled={disabled}>Requeue</button>}
              {!selected.removedAt && <button onClick={() => remove(selected)} style={{ ...buttonStyle, color: '#e5534b' }} disabled={disabled}>Remove</button>}
              <button onClick={() => void reorder(selected, Math.max(1, selected.position - 1))} style={buttonStyle} disabled={disabled || selected.position <= 1}>Move up</button>
              <button onClick={() => void reorder(selected, selected.position + 1)} style={buttonStyle} disabled={disabled}>Move down</button>
              {selected.kind === 'remote' && <><button onClick={() => void refreshRemote(selected)} style={buttonStyle} disabled={disabled}>Refresh PR</button><button onClick={() => void publishComment(selected)} style={buttonStyle} disabled={disabled}>Publish comment</button><button onClick={() => void cleanupRemote(selected)} style={buttonStyle} disabled={disabled}>Clean worktree</button></>}
              <button onClick={() => void exportPackage(selected)} style={buttonStyle} disabled={disabled || !selected.snapshotManifestPath}>Export</button>
            </div>
            <hr style={{ borderColor: 'rgba(127,127,127,0.25)', margin: '10px 0' }} />
            <strong style={{ fontSize: 11 }}>Feedback delivery</strong>
            <div style={{ ...muted, marginTop: 4 }}>ACP: {acpLabel(selected)}{selected.acpLastError ? ` — ${selected.acpLastError}` : ''}</div>
            <textarea value={feedbackBody} onChange={(event) => setFeedbackBody(event.target.value)} placeholder="Add formal review feedback (kept separate from /ask)…" aria-label="Formal feedback" rows={3} style={{ width: '100%', boxSizing: 'border-box', marginTop: 6, font: 'inherit', resize: 'vertical' }} disabled={disabled} />
            <div style={{ display: 'flex', gap: 5, alignItems: 'center', flexWrap: 'wrap', marginTop: 6 }}>
              <button onClick={() => addFeedback(selected)} style={buttonStyle} disabled={disabled || !feedbackBody.trim()}>Add feedback</button>
              <button onClick={() => void copyFeedbackPrompt(selected)} style={buttonStyle} disabled={disabled}>{working === `copy:${selected.id}` ? 'Copying…' : 'Copy feedback prompt'}</button>
              <select value={deliveryPolicy} onChange={(event) => setDeliveryPolicy(event.target.value as 'queue' | 'interrupt')} aria-label="ACP delivery policy" disabled={disabled || selected.acpState === 'unavailable' || selected.acpState === 'error'} style={{ fontSize: 11 }}><option value="queue">Queue when idle</option><option value="interrupt">Interrupt current turn</option></select>
              <button onClick={() => void deliverFeedback(selected)} style={buttonStyle} disabled={disabled || !undelivered || selected.acpState === 'unavailable' || selected.acpState === 'error' || (selected.acpState === 'busy' && deliveryPolicy !== 'interrupt')}>{working === `deliver:${selected.id}` ? 'Sending…' : selected.acpState === 'busy' ? `Interrupt & send (${undelivered})` : `Send through ACP (${undelivered})`}</button>
            </div>
            {selected.acpState === 'busy' && <div style={{ ...muted, marginTop: 5 }}>{deliveryPolicy === 'interrupt' ? 'The agent is busy. Sending will cancel its current turn, then deliver this batch once; repeated clicks remain disabled.' : 'The agent is busy. Choose “Interrupt current turn” to redirect it now, or wait for idle.'}</div>}
            {selected.acpState === 'unavailable' && <div style={{ ...muted, marginTop: 5 }}>No live ACP session was submitted. Copy the prompt or reproduce a fresh Copilot session below.</div>}
            {!!detail?.feedback.length && <details style={{ marginTop: 8 }}><summary>Formal feedback ({detail.feedback.length}; {undelivered} undelivered)</summary>{detail.feedback.map((entry) => <div key={entry.id} style={{ ...muted, marginTop: 4 }}><strong>{entry.path ? `${entry.path}${entry.line ? `:${entry.line}` : ''}: ` : ''}</strong>{entry.body} <em>({entry.deliveredAt ? `delivered ${time(entry.deliveredAt)}` : 'not delivered'})</em></div>)}</details>}
            <details style={{ marginTop: 8 }}><summary>Snapshot, provenance &amp; reproduction</summary>
              <dl style={{ ...muted, display: 'grid', gridTemplateColumns: '110px 1fr', gap: '3px 6px', margin: '7px 0' }}>
                <dt>Path</dt><dd style={{ margin: 0, overflowWrap: 'anywhere' }}>{selected.workspacePath}</dd>
                <dt>Kind / branch</dt><dd style={{ margin: 0 }}>{selected.kind} · base {readable(remoteStatus?.pullRequest.baseRefName ?? selected.baseRef)}</dd>
                <dt>Snapshot</dt><dd style={{ margin: 0, overflowWrap: 'anywhere' }}>{readable(selected.snapshotManifestPath)}</dd>
                <dt>Snapshot ID</dt><dd style={{ margin: 0 }}>{typeof selected.snapshotManifest?.id === 'string' ? selected.snapshotManifest.id : '—'}</dd>
                <dt>Source SHA</dt><dd style={{ margin: 0 }}>{readable(selected.sourceFingerprint)}</dd>
                <dt>Review stream</dt><dd style={{ margin: 0, overflowWrap: 'anywhere' }}>{readable(selected.identityKey)}</dd>
                <dt>Topic</dt><dd style={{ margin: 0 }}>{readable(selected.reviewTopic)}</dd>
                <dt>Origin agent</dt><dd style={{ margin: 0 }}>{readable(selected.agentProvider)} · {readable(selected.agentId)}</dd>
                <dt>cmux surface</dt><dd style={{ margin: 0 }}>{typeof selected.provenance?.surfaceId === 'string' ? selected.provenance.surfaceId : '—'}</dd>
                <dt>Copilot session</dt><dd style={{ margin: 0 }}>{readable(selected.copilotSessionId)}</dd>
                <dt>ACP session</dt><dd style={{ margin: 0 }}>{acpLabel(selected)}</dd>
                {selected.remoteUrl && <><dt>Remote PR</dt><dd style={{ margin: 0, overflowWrap: 'anywhere' }}>{selected.remoteUrl}</dd></>}
                {selected.supersedesId && <><dt>Supersedes</dt><dd style={{ margin: 0 }}>{selected.supersedesId}</dd></>}
              </dl>
              {typeof selected.snapshotManifest?.repos === 'object' && Array.isArray(selected.snapshotManifest.repos) && <details style={{ marginTop: 6 }}><summary>Snapshot repository SHAs ({selected.snapshotManifest.repos.length})</summary>{selected.snapshotManifest.repos.map((repo, index) => {
                const value = repo as { workspaceRelativePath?: unknown; baseSha?: unknown; snapshotSha?: unknown };
                return <div key={index} style={{ ...muted, marginTop: 3, fontFamily: 'monospace', overflowWrap: 'anywhere' }}>{String(value.workspaceRelativePath ?? '.')}: base {String(value.baseSha ?? '—')} · snapshot {String(value.snapshotSha ?? '—')}</div>;
              })}</details>}
              {remoteStatus && <details style={{ marginTop: 6 }} open><summary>Remote PR &amp; managed cache</summary><dl style={{ ...muted, display: 'grid', gridTemplateColumns: '110px 1fr', gap: '3px 6px', margin: '7px 0' }}>
                <dt>Repository / PR</dt><dd style={{ margin: 0 }}>{remoteStatus.pullRequest.repository} #{remoteStatus.pullRequest.number}</dd>
                <dt>Base</dt><dd style={{ margin: 0, fontFamily: 'monospace' }}>{remoteStatus.pullRequest.baseRefName} · {remoteStatus.pullRequest.baseSha}</dd>
                <dt>Head</dt><dd style={{ margin: 0, fontFamily: 'monospace' }}>{remoteStatus.pullRequest.headRefName} · {remoteStatus.pullRequest.headSha}</dd>
                <dt>GitHub status</dt><dd style={{ margin: 0 }}>{remoteStatus.pullRequest.state}{remoteStatus.pullRequest.isDraft ? ' · draft' : ''}</dd>
                <dt>Mirror</dt><dd style={{ margin: 0, overflowWrap: 'anywhere' }}>{remoteStatus.cache.mirrorPresent ? 'ready' : 'missing'} · {remoteStatus.cache.mirrorPath}</dd>
                <dt>Worktree</dt><dd style={{ margin: 0, overflowWrap: 'anywhere' }}>{remoteStatus.cache.worktreePresent ? 'ready' : 'missing'} · {remoteStatus.cache.worktreePath}</dd>
              </dl><div style={muted}>If the head changed, refresh before publishing. Cleaning removes only this managed worktree; the mirror stays reusable.</div></details>}
              {plan?.commands && <div style={{ marginTop: 7, display: 'grid', gap: 5 }}>{Object.entries(plan.commands).map(([key, command]) => <div key={key} style={{ display: 'flex', gap: 5, alignItems: 'center' }}><code style={{ ...muted, flex: 1, overflowWrap: 'anywhere' }}>{command}</code><button onClick={() => void copyText(command, key === 'reproduceCopilot' ? 'Copilot reproduction command' : 'Reproduction command')} style={buttonStyle}>Copy</button></div>)}</div>}
              {plan?.notes.map((note) => <div key={note} style={{ ...muted, marginTop: 4 }}>{note}</div>)}
            </details>
            {!!detail?.decisions.length && <details style={{ marginTop: 8 }}><summary>Decision history ({detail.decisions.length})</summary>{detail.decisions.map((entry) => <div key={entry.id} style={{ ...muted, marginTop: 4 }}><strong>{entry.status.replace('_', ' ')}</strong> · {time(entry.createdAt)}{entry.body ? ` — ${entry.body}` : ''}</div>)}</details>}
          </article>}
        </>}
      </div>
    </section>
  );
}
