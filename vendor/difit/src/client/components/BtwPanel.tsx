import { useCallback, useEffect, useState } from 'react';
import ReactMarkdown from 'react-markdown';
import remarkGfm from 'remark-gfm';

import { daemonFetch } from '../services/daemonAuth';

const BTW_DRAFT_STORAGE_KEY = 'cmux-localreview.btw-draft-v1';
const BTW_TRANSPORT_STORAGE_KEY = 'cmux-localreview.btw-transport-v1';
const BTW_AGENT_STORAGE_KEY = 'cmux-localreview.btw-agent-v1';

function readStoredString(key: string): string {
  try {
    return window.localStorage.getItem(key) ?? '';
  } catch {
    return '';
  }
}

interface BtwAnswerDTO {
  id: number;
  body: string;
  pending: boolean;
  createdAt: number;
  updatedAt: number;
}

interface BtwQuestionDTO {
  id: number;
  body: string;
  createdAt: number;
  answer: BtwAnswerDTO | null;
}

interface BtwThreadDTO {
  id: number;
  transport: 'acp' | 'terminal';
  acpProvider: string | null;
  repoId: number | null;
  filePath: string | null;
  startLine: number | null;
  endLine: number | null;
  targetAgentId: string | null;
  createdAt: number;
  questions: BtwQuestionDTO[];
}

interface AgentDTO {
  id: string;
  provider: string;
  status: string;
  workspacePath: string | null;
  metadata: { surfaceId?: string; lastError?: string };
}

interface BtwPanelProps {
  /** Currently selected repo's route id (string hash), or null for a workspace-level question. */
  selectedRepoId: string | null;
  /** Bump to force a refetch (e.g. on a WS btw-update event). */
  refreshNonce: number;
  collapsed: boolean;
  onToggleCollapsed: () => void;
}

async function fetchThreads(): Promise<BtwThreadDTO[]> {
  const res = await daemonFetch('/api/btw/threads');
  if (!res.ok) throw new Error(`Failed to load /btw threads: ${res.status}`);
  const data = (await res.json()) as { threads: BtwThreadDTO[] };
  return data.threads;
}

async function fetchAgents(): Promise<AgentDTO[]> {
  const res = await daemonFetch('/api/agents');
  if (!res.ok) return [];
  const data = (await res.json()) as { agents?: AgentDTO[] };
  return Array.isArray(data.agents) ? data.agents : [];
}

export function BtwPanel({ selectedRepoId, refreshNonce, collapsed, onToggleCollapsed }: BtwPanelProps) {
  const [threads, setThreads] = useState<BtwThreadDTO[]>([]);
  const [question, setQuestion] = useState(() => readStoredString(BTW_DRAFT_STORAGE_KEY));
  const [transport, setTransport] = useState<'acp' | 'terminal'>(() =>
    readStoredString(BTW_TRANSPORT_STORAGE_KEY) === 'terminal' ? 'terminal' : 'acp',
  );
  const [agents, setAgents] = useState<AgentDTO[]>([]);
  const [agentId, setAgentId] = useState(() => readStoredString(BTW_AGENT_STORAGE_KEY));
  const [asking, setAsking] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const refresh = useCallback(() => {
    fetchThreads()
      .then(setThreads)
      .catch((err: unknown) => setError(err instanceof Error ? err.message : 'Failed to load /btw threads'));
  }, []);

  useEffect(() => {
    refresh();
  }, [refresh, refreshNonce]);

  useEffect(() => {
    void fetchAgents().then((available) => {
      setAgents(available);
      setAgentId((current) => {
        if (current && available.some((agent) => agent.id === current)) return current;
        // Do not choose an arbitrary terminal: an originating/registered
        // target must be explicit. A single connected agent is the one safe
        // ergonomic exception.
        const connected = available.filter((agent) => agent.status === 'connected' && agent.metadata?.surfaceId);
        return connected.length === 1 ? connected[0]!.id : '';
      });
    });
  }, [refreshNonce]);

  useEffect(() => {
    try {
      window.localStorage.setItem(BTW_DRAFT_STORAGE_KEY, question);
    } catch {
      // Draft retention is best-effort.
    }
  }, [question]);

  useEffect(() => {
    try {
      window.localStorage.setItem(BTW_TRANSPORT_STORAGE_KEY, transport);
    } catch {
      // Preference retention is best-effort.
    }
  }, [transport]);

  useEffect(() => {
    try {
      window.localStorage.setItem(BTW_AGENT_STORAGE_KEY, agentId);
    } catch {
      // Target selection is best-effort browser state; the server remains authoritative.
    }
  }, [agentId]);

  const ask = useCallback(async () => {
    const body = question.trim();
    if (!body) return;
    if (transport === 'terminal' && !agentId) {
      setError('Choose the originating registered agent before sending a terminal /btw question.');
      return;
    }
    setAsking(true);
    setError(null);
    try {
      const res = await daemonFetch('/api/btw/ask', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({
          transport,
          repoId: selectedRepoId ?? undefined,
          agentId: transport === 'terminal' ? agentId : undefined,
          question: body,
        }),
      });
      const data = (await res.json()) as { error?: string };
      if (!res.ok) throw new Error(data.error ?? `Failed to ask: ${res.status}`);
      setQuestion('');
      refresh();
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to ask /btw question');
    } finally {
      setAsking(false);
    }
  }, [question, transport, selectedRepoId, agentId, refresh]);

  if (collapsed) {
    return (
      <button
        onClick={onToggleCollapsed}
        title="Open /btw panel"
        style={{
          position: 'fixed',
          right: 12,
          bottom: 12,
          zIndex: 60,
          padding: '8px 14px',
          borderRadius: 20,
          border: '1px solid rgba(127,127,127,0.4)',
          background: 'var(--bg-primary, #161b22)',
          color: 'inherit',
          cursor: 'pointer',
        }}
      >
        /btw {threads.length > 0 ? `(${threads.length})` : ''}
      </button>
    );
  }

  return (
    <div
      style={{
        position: 'fixed',
        right: 0,
        top: 0,
        bottom: 0,
        width: 360,
        borderLeft: '1px solid rgba(127,127,127,0.3)',
        background: 'var(--bg-primary, #161b22)',
        display: 'flex',
        flexDirection: 'column',
        zIndex: 55,
      }}
    >
      <div
        style={{
          display: 'flex',
          justifyContent: 'space-between',
          alignItems: 'center',
          padding: '10px 12px',
          borderBottom: '1px solid rgba(127,127,127,0.3)',
        }}
      >
        <span style={{ fontWeight: 600, fontSize: 13 }}>/btw</span>
        <button onClick={onToggleCollapsed} style={{ fontSize: 12, cursor: 'pointer' }}>
          ✕
        </button>
      </div>

      <div style={{ flex: 1, overflowY: 'auto', padding: 10, display: 'flex', flexDirection: 'column', gap: 10 }}>
        {threads.length === 0 && (
          <div style={{ fontSize: 12, opacity: 0.6 }}>
            Ask a side question about the code under review — it won't become part of your review
            comments.
          </div>
        )}
        {threads.map((thread) => (
          <div
            key={thread.id}
            style={{
              border: '1px solid rgba(127,127,127,0.3)',
              borderRadius: 6,
              padding: 8,
              fontSize: 12,
            }}
          >
            {thread.filePath && (
              <div style={{ fontFamily: 'monospace', fontSize: 11, opacity: 0.7, marginBottom: 4 }}>
                {thread.filePath}
                {thread.startLine != null ? `:L${thread.startLine}` : ''}
              </div>
            )}
            {thread.questions.map((q) => (
              <div key={q.id} style={{ marginBottom: 6 }}>
                <div style={{ fontWeight: 600, marginBottom: 4 }}>{q.body}</div>
                {q.answer ? (
                  <div style={{ opacity: q.answer.pending ? 0.7 : 1 }}>
                    <ReactMarkdown remarkPlugins={[remarkGfm]}>
                      {q.answer.body || (q.answer.pending ? '_thinking…_' : '')}
                    </ReactMarkdown>
                    {q.answer.pending && <span style={{ fontSize: 11, opacity: 0.6 }}>streaming…</span>}
                  </div>
                ) : (
                  <div style={{ fontSize: 11, opacity: 0.6 }}>
                    Sent to terminal — waiting for the agent's answer file…
                  </div>
                )}
              </div>
            ))}
            <div style={{ fontSize: 10, opacity: 0.5 }}>
              via {thread.transport === 'acp' ? thread.acpProvider ?? 'acp' : thread.targetAgentId ? `terminal · ${thread.targetAgentId}` : 'terminal'}
            </div>
          </div>
        ))}
      </div>

      <div style={{ padding: 10, borderTop: '1px solid rgba(127,127,127,0.3)' }}>
        {error && (
          <div style={{ fontSize: 11, color: '#e5534b', marginBottom: 6 }}>
            {error}
          </div>
        )}
        <textarea
          value={question}
          onChange={(e) => setQuestion(e.target.value)}
          placeholder={
            selectedRepoId ? 'Ask about the selected repo…' : 'Ask a workspace-wide question…'
          }
          rows={3}
          style={{
            width: '100%',
            fontSize: 12,
            padding: 6,
            background: 'transparent',
            border: '1px solid rgba(127,127,127,0.4)',
            borderRadius: 4,
            color: 'inherit',
            resize: 'vertical',
          }}
        />
        <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', marginTop: 6 }}>
          <label style={{ fontSize: 11, display: 'flex', alignItems: 'center', gap: 4 }}>
            <select
              value={transport}
              onChange={(e) => setTransport(e.target.value as 'acp' | 'terminal')}
              style={{ fontSize: 11, background: 'transparent', color: 'inherit' }}
            >
              <option value="acp">ACP agent</option>
              <option value="terminal">Terminal agent</option>
            </select>
          </label>
          {transport === 'terminal' && (
            <select
              value={agentId}
              onChange={(e) => setAgentId(e.target.value)}
              aria-label="Target terminal agent"
              style={{ minWidth: 0, maxWidth: 160, fontSize: 11, background: 'transparent', color: 'inherit' }}
            >
              <option value="">Select target agent…</option>
              {agents.map((agent) => (
                <option key={agent.id} value={agent.id} disabled={agent.status !== 'connected' || !agent.metadata?.surfaceId}>
                  {agent.provider} · {agent.id.slice(0, 8)} ({agent.status})
                </option>
              ))}
            </select>
          )}
          <button
            onClick={() => void ask()}
            disabled={asking || !question.trim() || (transport === 'terminal' && !agentId)}
            style={{
              fontSize: 12,
              padding: '4px 10px',
              borderRadius: 4,
              border: '1px solid rgba(127,127,127,0.4)',
              background: 'transparent',
              color: 'inherit',
              cursor: asking ? 'default' : 'pointer',
              opacity: asking || !question.trim() || (transport === 'terminal' && !agentId) ? 0.6 : 1,
            }}
          >
            {asking ? 'Asking…' : 'Ask'}
          </button>
        </div>
      </div>
    </div>
  );
}
