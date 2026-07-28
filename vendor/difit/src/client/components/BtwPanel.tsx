import { useCallback, useEffect, useRef, useState } from 'react';
import ReactMarkdown from 'react-markdown';
import remarkGfm from 'remark-gfm';

import { daemonFetch } from '../services/daemonAuth';

const BTW_DRAFT_STORAGE_KEY = 'cmux-localreview.btw-draft-v1';

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
  transport: 'copilot';
  repoId: number | null;
  filePath: string | null;
  startLine: number | null;
  endLine: number | null;
  targetAgentId: string | null;
  createdAt: number;
  questions: BtwQuestionDTO[];
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

export function BtwPanel({ selectedRepoId, refreshNonce, collapsed, onToggleCollapsed }: BtwPanelProps) {
  const [threads, setThreads] = useState<BtwThreadDTO[]>([]);
  const [question, setQuestion] = useState(() => readStoredString(BTW_DRAFT_STORAGE_KEY));
  const [retryQuestion, setRetryQuestion] = useState<string | null>(null);
  const [asking, setAsking] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const askInFlightRef = useRef(false);

  const refresh = useCallback(() => {
    fetchThreads()
      .then(setThreads)
      .catch((err: unknown) => setError(err instanceof Error ? err.message : 'Failed to load /btw threads'));
  }, []);

  useEffect(() => {
    refresh();
  }, [refresh, refreshNonce]);

  // WebSocket updates make the answer appear immediately. The short polling
  // fallback covers a reconnecting browser, so an accepted native SDK question
  // cannot remain visually stuck at "thinking" just because that one socket
  // event was missed.
  useEffect(() => {
    if (!threads.some((thread) => thread.questions.some((item) => item.answer?.pending))) return;
    const timer = window.setInterval(refresh, 350);
    return () => window.clearInterval(timer);
  }, [refresh, threads]);

  useEffect(() => {
    try {
      window.localStorage.setItem(BTW_DRAFT_STORAGE_KEY, question);
    } catch {
      // Draft retention is best-effort.
    }
  }, [question]);

  const ask = useCallback(async (questionOverride?: string) => {
    const body = (questionOverride ?? question).trim();
    if (!body || askInFlightRef.current) return;
    askInFlightRef.current = true;
    setAsking(true);
    setError(null);
    try {
      const res = await daemonFetch('/api/btw/ask', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({
          transport: 'copilot',
          repoId: selectedRepoId ?? undefined,
          question: body,
        }),
      });
      const data = (await res.json()) as { error?: string };
      if (!res.ok) throw new Error(data.error ?? `Failed to ask: ${res.status}`);
      setQuestion('');
      setRetryQuestion(null);
      refresh();
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to ask /btw question');
      setRetryQuestion(body);
      setQuestion((current) => current || body);
    } finally {
      askInFlightRef.current = false;
      setAsking(false);
    }
  }, [question, selectedRepoId, refresh]);

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
                    Copilot is preparing a response…
                  </div>
                )}
              </div>
            ))}
            <div style={{ fontSize: 10, opacity: 0.5 }}>
              via Copilot SDK
            </div>
          </div>
        ))}
      </div>

      <div style={{ padding: 10, borderTop: '1px solid rgba(127,127,127,0.3)' }}>
        {error && (
          <div role="alert" style={{ fontSize: 11, color: '#e5534b', marginBottom: 6 }}>
            {error} {retryQuestion && <button type="button" onClick={() => void ask(retryQuestion)} disabled={asking} style={{ marginLeft: 5, fontSize: 11, padding: '2px 5px', borderRadius: 4, border: '1px solid rgba(127,127,127,0.4)', background: 'transparent', color: 'inherit', cursor: 'pointer' }}>Retry last question</button>}
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
          <span style={{ fontSize: 11, opacity: 0.72 }}>Copilot SDK · private side conversation</span>
          <button
            onClick={() => void ask()}
            disabled={asking || !question.trim()}
            style={{
              fontSize: 12,
              padding: '4px 10px',
              borderRadius: 4,
              border: '1px solid rgba(127,127,127,0.4)',
              background: 'transparent',
              color: 'inherit',
              cursor: asking ? 'default' : 'pointer',
              opacity: asking || !question.trim() ? 0.6 : 1,
            }}
          >
            {asking ? 'Asking…' : 'Ask'}
          </button>
        </div>
      </div>
    </div>
  );
}
