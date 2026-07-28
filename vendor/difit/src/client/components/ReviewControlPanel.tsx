import { useCallback, useEffect, useState } from 'react';

import { daemonFetch } from '../services/daemonAuth';

type QueueStatus = 'queued' | 'in_review' | 'changes_requested' | 'approved' | 'completed';

interface QueueItem {
  id: string;
  title: string;
  body: string;
  workspacePath: string;
  kind: 'local' | 'remote';
  status: QueueStatus;
  position: number;
  agentId: string | null;
  agentProvider: string | null;
  updatedAt: number;
}

interface Agent {
  id: string;
  provider: string;
  workspace_path: string | null;
  review_session_id: string | null;
  status: string;
  updated_at: number;
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

export function ReviewControlPanel({ collapsed, onToggleCollapsed, refreshNonce }: ReviewControlPanelProps) {
  const [tab, setTab] = useState<PanelTab>('queue');
  const [queue, setQueue] = useState<QueueItem[]>([]);
  const [agents, setAgents] = useState<Agent[]>([]);
  const [error, setError] = useState<string | null>(null);
  const [loading, setLoading] = useState(false);

  const refresh = useCallback(async () => {
    setLoading(true);
    setError(null);
    try {
      const [queueResponse, agentsResponse] = await Promise.all([
        daemonFetch('/api/queue'),
        daemonFetch('/api/agents'),
      ]);
      if (!queueResponse.ok || !agentsResponse.ok) {
        throw new Error(
          queueResponse.status === 401 || agentsResponse.status === 401
            ? 'Open this review with localreview-open to connect the daemon controls.'
            : 'Queue or agent registry is unavailable for this review.',
        );
      }
      const queueData = (await queueResponse.json()) as { items?: QueueItem[] };
      const agentData = (await agentsResponse.json()) as { agents?: Agent[] };
      setQueue(Array.isArray(queueData.items) ? queueData.items : []);
      setAgents(Array.isArray(agentData.agents) ? agentData.agents : []);
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to load review controls.');
    } finally {
      setLoading(false);
    }
  }, []);

  useEffect(() => {
    void refresh();
  }, [refresh, refreshNonce]);

  const openNext = useCallback(async () => {
    try {
      const response = await daemonFetch('/api/queue/open-next', { method: 'POST' });
      if (!response.ok) throw new Error('No queued review could be opened.');
      await refresh();
      window.location.reload();
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to open the next review.');
    }
  }, [refresh]);

  const decide = useCallback(
    async (item: QueueItem, decision: Extract<QueueStatus, 'approved' | 'changes_requested' | 'completed'>) => {
      try {
        const response = await daemonFetch(`/api/queue/${encodeURIComponent(item.id)}/decision`, {
          method: 'POST',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify({ decision }),
        });
        if (!response.ok) throw new Error(`Failed to mark review ${decision.replace('_', ' ')}.`);
        await refresh();
      } catch (err) {
        setError(err instanceof Error ? err.message : 'Failed to update review status.');
      }
    },
    [refresh],
  );

  if (collapsed) {
    return (
      <button
        onClick={onToggleCollapsed}
        title="Open review queue and agents"
        style={{ ...buttonStyle, position: 'fixed', left: 12, bottom: 12, zIndex: 60, borderRadius: 20 }}
      >
        Review{queue.length ? ` (${queue.filter((item) => item.status === 'queued').length})` : ''}
      </button>
    );
  }

  return (
    <section
      style={{
        position: 'fixed', left: 0, bottom: 0, width: 340, maxHeight: '52vh', zIndex: 55,
        display: 'flex', flexDirection: 'column', background: 'var(--bg-primary, #161b22)',
        border: '1px solid rgba(127,127,127,0.35)', borderLeft: 'none', borderBottom: 'none',
        borderTopRightRadius: 8,
      }}
      aria-label="Review controls"
    >
      <div style={{ display: 'flex', alignItems: 'center', gap: 6, padding: '8px 10px', borderBottom: '1px solid rgba(127,127,127,0.3)' }}>
        <button onClick={() => setTab('queue')} style={{ ...buttonStyle, background: tab === 'queue' ? 'rgba(127,127,127,0.22)' : 'transparent' }}>Queue</button>
        <button onClick={() => setTab('agents')} style={{ ...buttonStyle, background: tab === 'agents' ? 'rgba(127,127,127,0.22)' : 'transparent' }}>Agents ({agents.length})</button>
        <button onClick={() => void refresh()} style={{ ...buttonStyle, marginLeft: 'auto' }} disabled={loading}>{loading ? '…' : '↻'}</button>
        <button onClick={onToggleCollapsed} style={buttonStyle} aria-label="Close review controls">✕</button>
      </div>
      {error && <div style={{ color: '#e5534b', fontSize: 11, padding: '7px 10px' }}>{error}</div>}
      <div style={{ overflowY: 'auto', padding: 10, display: 'flex', flexDirection: 'column', gap: 8 }}>
        {tab === 'queue' ? (
          <>
            <button onClick={() => void openNext()} style={{ ...buttonStyle, alignSelf: 'flex-start' }}>Open next queued review</button>
            {queue.length === 0 && <span style={{ fontSize: 12, opacity: 0.65 }}>No queued reviews.</span>}
            {queue.map((item) => (
              <article key={item.id} style={{ border: '1px solid rgba(127,127,127,0.28)', borderRadius: 5, padding: 8, fontSize: 12 }}>
                <div style={{ display: 'flex', gap: 6, alignItems: 'baseline' }}>
                  <strong style={{ overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>{item.title}</strong>
                  <span style={{ marginLeft: 'auto', fontSize: 10, opacity: 0.65 }}>{item.status.replace('_', ' ')}</span>
                </div>
                <div style={{ fontSize: 10, opacity: 0.6, marginTop: 3, overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>{item.workspacePath}</div>
                {item.status === 'in_review' && (
                  <div style={{ display: 'flex', gap: 5, marginTop: 7 }}>
                    <button onClick={() => void decide(item, 'approved')} style={buttonStyle}>Approve</button>
                    <button onClick={() => void decide(item, 'changes_requested')} style={buttonStyle}>Request changes</button>
                    <button onClick={() => void decide(item, 'completed')} style={buttonStyle}>Complete</button>
                  </div>
                )}
              </article>
            ))}
          </>
        ) : (
          <>
            {agents.length === 0 && <span style={{ fontSize: 12, opacity: 0.65 }}>No registered agents.</span>}
            {agents.map((agent) => (
              <article key={agent.id} style={{ border: '1px solid rgba(127,127,127,0.28)', borderRadius: 5, padding: 8, fontSize: 12 }}>
                <strong>{agent.provider}</strong> <span style={{ fontSize: 10, opacity: 0.65 }}>{agent.status}</span>
                <div style={{ fontFamily: 'monospace', fontSize: 10, opacity: 0.7, marginTop: 4 }}>{agent.id}</div>
                {agent.workspace_path && <div style={{ fontSize: 10, opacity: 0.6, marginTop: 3 }}>{agent.workspace_path}</div>}
              </article>
            ))}
          </>
        )}
      </div>
    </section>
  );
}
