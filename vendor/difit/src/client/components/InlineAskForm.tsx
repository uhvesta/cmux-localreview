import { useEffect, useRef, useState } from 'react';
import ReactMarkdown from 'react-markdown';
import remarkGfm from 'remark-gfm';

import { daemonFetch } from '../services/daemonAuth';

export interface InlineAskLocation {
  repoId?: string;
  filePath: string;
  side: 'base' | 'current';
  startLine: number;
  endLine: number;
  selectedCode: string;
}

interface AskMessage {
  id: number;
  role: 'user' | 'assistant' | 'system';
  body: string;
  pending: boolean;
  location?: InlineAskLocation | null;
}

interface InlineAskFormProps {
  location: InlineAskLocation;
  /** A `/ask question` submitted from the regular inline composer. */
  initialPrompt?: string;
  /** Clears the one-shot draft in the parent before a remount can replay it. */
  onInitialPromptSent?: () => void;
  /** An answer reaches formal review only through this explicit callback. */
  onConvertToReviewComment: (body: string) => Promise<void>;
  onClose: () => void;
}

function label(location: InlineAskLocation): string {
  return `${location.filePath}:${location.startLine}${location.endLine === location.startLine ? '' : `–${location.endLine}`}`;
}

function isThisInlineThread(message: AskMessage, location: InlineAskLocation): boolean {
  const anchor = message.location;
  return Boolean(anchor && anchor.repoId === location.repoId && anchor.filePath === location.filePath
    && anchor.side === location.side && anchor.startLine === location.startLine && anchor.endLine === location.endLine);
}

/**
 * A durable inline Copilot research transcript.  It deliberately talks only to
 * /api/ask; no queue feedback/export path reads these messages.  Converting a
 * specific answer is an affirmative, one-way action by the reviewer.
 */
export function InlineAskForm({ location, initialPrompt, onInitialPromptSent, onConvertToReviewComment, onClose }: InlineAskFormProps) {
  const [conversationId, setConversationId] = useState<string | null>(null);
  const [messages, setMessages] = useState<AskMessage[]>([]);
  const [prompt, setPrompt] = useState('');
  const [status, setStatus] = useState<'loading' | 'idle' | 'sending' | 'cancelling' | 'error'>('loading');
  const [activity, setActivity] = useState('Opening the review’s shared Copilot conversation…');
  const [error, setError] = useState<string | null>(null);
  const [convertingId, setConvertingId] = useState<number | null>(null);
  const sentInitialPrompts = useRef(new Set<string>());

  useEffect(() => {
    let active = true;
    const open = async () => {
      setStatus('loading'); setActivity('Opening the review’s shared Copilot conversation…'); setError(null);
      try {
        const created = await daemonFetch('/api/ask/inline-conversations', {
          method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ context: location }),
        });
        const createdBody = await created.json() as { conversation?: { id?: string }; error?: string };
        const id = createdBody.conversation?.id;
        if (!created.ok || !id) throw new Error(createdBody.error ?? 'Could not open this inline /ask conversation.');
        const transcript = await daemonFetch(`/api/ask/conversations/${encodeURIComponent(id)}`);
        const transcriptBody = await transcript.json() as { messages?: AskMessage[]; error?: string };
        if (!transcript.ok) throw new Error(transcriptBody.error ?? 'Could not restore this inline /ask transcript.');
        if (!active) return;
        setConversationId(id); setMessages(Array.isArray(transcriptBody.messages) ? transcriptBody.messages : []); setStatus('idle'); setActivity('');
      } catch (err) {
        if (!active) return;
        setError(err instanceof Error ? err.message : 'Inline /ask is unavailable.'); setStatus('error'); setActivity('Copilot could not be reached.');
      }
    };
    void open();
    return () => { active = false; };
  }, [location.filePath, location.side, location.startLine, location.endLine]);

  const send = async (promptOverride?: string) => {
    const text = (promptOverride ?? prompt).trim();
    if (!text || !conversationId || status === 'sending') return;
    const optimisticUser: AskMessage = { id: -Date.now(), role: 'user', body: text, pending: false, location };
    const optimisticAssistant: AskMessage = { id: -(Date.now() + 1), role: 'assistant', body: '', pending: true, location };
    setMessages((current) => [...current, optimisticUser, optimisticAssistant]);
    setPrompt(''); setStatus('sending'); setActivity('Copilot is reading this code and the earlier conversation…'); setError(null);
    try {
      const response = await daemonFetch(`/api/ask/conversations/${encodeURIComponent(conversationId)}/messages`, {
        method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ prompt: text, location }),
      });
      if (!response.ok || !response.body) throw new Error(`Copilot is unavailable (${response.status}).`);
      const reader = response.body.getReader(); const decoder = new TextDecoder(); let buffer = '';
      const apply = (block: string) => {
        const event = block.split(/\r?\n/).find((line) => line.startsWith('event:'))?.slice(6).trim();
        const raw = block.split(/\r?\n/).filter((line) => line.startsWith('data:')).map((line) => line.slice(5).trim()).join('\n');
        if (!raw) return;
        const payload = JSON.parse(raw) as { delta?: string; content?: string; error?: string };
        if (event === 'started') setActivity('Copilot is preparing a response…');
        if (event === 'delta' && payload.delta) {
          setActivity('Copilot is responding live…');
          setMessages((current) => current.map((message) => message.id === optimisticAssistant.id ? { ...message, body: message.body + payload.delta } : message));
        }
        if (event === 'done') setMessages((current) => current.map((message) => message.id === optimisticAssistant.id ? { ...message, body: payload.content ?? message.body, pending: false } : message));
        if (event === 'error') throw new Error(payload.error ?? 'The /ask agent reported an error.');
      };
      while (true) {
        const next = await reader.read();
        buffer += decoder.decode(next.value ?? new Uint8Array(), { stream: !next.done });
        const blocks = buffer.split(/\r?\n\r?\n/); buffer = blocks.pop() ?? '';
        blocks.forEach(apply);
        if (next.done) break;
      }
      if (buffer) apply(buffer);
      const transcript = await daemonFetch(`/api/ask/conversations/${encodeURIComponent(conversationId)}`);
      if (transcript.ok) {
        const body = await transcript.json() as { messages?: AskMessage[] };
        setMessages(Array.isArray(body.messages) ? body.messages : []);
      }
      window.dispatchEvent(new CustomEvent('cmux-localreview:ask-updated', { detail: { conversationId } }));
      setStatus('idle'); setActivity('');
    } catch (err) {
      setMessages((current) => current.map((message) => message.id === optimisticAssistant.id ? { ...message, pending: false } : message));
      setError(err instanceof Error ? err.message : 'Inline /ask failed.'); setStatus('error'); setActivity('Copilot stopped before completing the response.');
    }
  };

  const cancel = async () => {
    if (!conversationId || (status !== 'sending' && status !== 'cancelling')) return;
    setStatus('cancelling'); setActivity('Stopping Copilot’s response…');
    try {
      const response = await daemonFetch(`/api/ask/conversations/${encodeURIComponent(conversationId)}/cancel`, { method: 'POST' });
      const result = await response.json() as { cancelled?: boolean; error?: string };
      if (!response.ok) throw new Error(result.error ?? 'Could not stop Copilot.');
      if (!result.cancelled) setActivity('Copilot had already finished; restoring the transcript…');
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Could not stop Copilot.');
      setStatus('sending'); setActivity('Copilot is still responding…');
    }
  };

  // `/ask question` in the normal comment composer opens this durable,
  // location-scoped chat and sends the first turn as soon as its session is
  // restored. A key keeps re-renders from sending it twice.
  useEffect(() => {
    const text = initialPrompt?.trim();
    if (!text || !conversationId || status !== 'idle') return;
    const key = `${conversationId}\u0000${text}`;
    if (sentInitialPrompts.current.has(key)) return;
    sentInitialPrompts.current.add(key);
    // The question belongs to this initial send only. Persisted transcripts
    // are restored on later opens, but must never replay an old turn.
    onInitialPromptSent?.();
    void send(text);
  }, [conversationId, initialPrompt, onInitialPromptSent, status]);

  const convert = async (message: AskMessage) => {
    if (!message.body.trim()) return;
    setConvertingId(message.id); setError(null);
    try { await onConvertToReviewComment(message.body.trim()); }
    catch (err) { setError(err instanceof Error ? err.message : 'Could not create the review comment.'); }
    finally { setConvertingId(null); }
  };

  const busy = status === 'sending' || status === 'cancelling';
  const visibleMessages = messages.filter((message) => isThisInlineThread(message, location));
  const openFullChat = () => {
    // Refresh the persistent transcript before showing the side panel. The
    // native events deliberately cross the independent diff and panel React
    // trees, so inline and side-chat views cannot drift apart.
    window.dispatchEvent(new CustomEvent('cmux-localreview:ask-updated', { detail: { conversationId } }));
    window.dispatchEvent(new CustomEvent('cmux-localreview:open-ask', { detail: { conversationId } }));
  };

  return <section className="m-2 mx-3 rounded-md border border-blue-500/50 border-l-4 border-l-blue-400 bg-github-bg-tertiary p-3 shadow-sm" aria-label={`Inline Copilot conversation for ${label(location)}`}>
    <div className="mb-2 flex items-start gap-2">
      <div><strong className="block text-sm text-blue-100">Copilot conversation</strong><span className="text-[11px] text-blue-200">Inline /ask · {label(location)} · shared review session</span></div>
      <button type="button" onClick={openFullChat} className="ml-auto text-xs underline">Open full /ask chat</button>
      <button type="button" onClick={onClose} className="text-xs underline">Close conversation</button>
    </div>
    <p className="mb-3 text-xs text-github-text-muted">This inline view shows this code location. Copilot keeps questions from every inline /ask and the side chat in one long-lived review session. This private chat never becomes review feedback unless you explicitly convert an answer.</p>
    {error && <div role="alert" className="mb-2 rounded border border-red-500/40 bg-red-950/20 p-2 text-xs text-red-200">{error}</div>}
    {(status === 'loading' || busy || status === 'error') && <div role="status" aria-live="polite" className="mb-2 flex items-center gap-2 rounded border border-blue-500/30 bg-blue-950/20 px-2 py-1.5 text-xs text-blue-100"><span aria-hidden="true" className={busy || status === 'loading' ? 'inline-block h-2 w-2 animate-pulse rounded-full bg-blue-300' : 'inline-block h-2 w-2 rounded-full bg-red-300'} />{activity}</div>}
    <div className="max-h-72 space-y-2 overflow-y-auto" aria-live="polite" aria-relevant="additions text">
      {status === 'loading' && <div className="text-xs text-github-text-muted">Restoring the shared /ask conversation…</div>}
      {visibleMessages.length === 0 && status === 'idle' && <div className="rounded border border-dashed border-github-border p-2 text-xs text-github-text-muted">Ask Copilot about this code. The answer appears here and in the full /ask transcript.</div>}
      {visibleMessages.map((message) => {
        const isCopilot = message.role === 'assistant';
        const speaker = isCopilot ? 'Copilot · SDK chat' : message.role === 'user' ? 'You' : 'System';
        return <article key={message.id} className={`rounded border p-2 text-xs ${isCopilot ? 'mr-4 border-blue-500/40 bg-blue-950/20' : 'ml-4 border-github-border bg-github-bg-secondary'}`}>
          <div className="mb-1 flex items-center gap-2 text-[10px] text-github-text-muted"><strong className={isCopilot ? 'text-blue-200' : 'text-github-text-primary'}>{speaker}</strong>{message.pending && <span className="flex items-center gap-1 text-blue-200"><span aria-hidden="true" className="inline-block h-1.5 w-1.5 animate-pulse rounded-full bg-blue-300" />Streaming…</span>}{isCopilot && !message.pending && <button type="button" onClick={() => void convert(message)} disabled={convertingId === message.id} className="ml-auto underline disabled:opacity-50">{convertingId === message.id ? 'Converting…' : 'Convert to review comment'}</button>}</div>
          <ReactMarkdown remarkPlugins={[remarkGfm]}>{message.body || (message.pending ? '_Copilot is thinking…_' : '')}</ReactMarkdown>
        </article>;
      })}
    </div>
    <label className="mt-3 block text-xs font-medium text-github-text-primary" htmlFor={`inline-ask-${conversationId ?? 'loading'}`}>Reply to Copilot</label>
    <textarea id={`inline-ask-${conversationId ?? 'loading'}`} value={prompt} onChange={(event) => setPrompt(event.target.value)} onKeyDown={(event) => { if ((event.metaKey || event.ctrlKey) && event.key === 'Enter') void send(); }} disabled={status === 'loading' || busy} placeholder="Ask a follow-up — Copilot has the full review conversation…" rows={2} className="mt-1 w-full rounded border border-github-border bg-github-bg-secondary p-2 text-xs" />
    <div className="mt-2 flex items-center gap-2"><span className="text-[10px] text-github-text-muted">⌘/Ctrl + Enter to send</span><div className="ml-auto flex gap-2">{busy && <button type="button" onClick={() => void cancel()} className="rounded border border-github-border px-2 py-1 text-xs">{status === 'cancelling' ? 'Stopping…' : 'Stop response'}</button>}<button type="button" onClick={() => void send()} disabled={!prompt.trim() || status === 'loading' || busy} className="rounded border border-blue-400 bg-blue-950/30 px-2 py-1 text-xs text-blue-100 disabled:opacity-50">{busy ? 'Copilot is replying…' : 'Send reply'}</button></div></div>
  </section>;
}
