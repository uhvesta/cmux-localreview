import { useEffect, useState } from 'react';
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
}

interface InlineAskFormProps {
  location: InlineAskLocation;
  /** An answer reaches formal review only through this explicit callback. */
  onConvertToReviewComment: (body: string) => Promise<void>;
  onClose: () => void;
}

function label(location: InlineAskLocation): string {
  return `${location.filePath}:${location.startLine}${location.endLine === location.startLine ? '' : `–${location.endLine}`}`;
}

/**
 * A durable inline Copilot research transcript.  It deliberately talks only to
 * /api/ask; no queue feedback/export path reads these messages.  Converting a
 * specific answer is an affirmative, one-way action by the reviewer.
 */
export function InlineAskForm({ location, onConvertToReviewComment, onClose }: InlineAskFormProps) {
  const [conversationId, setConversationId] = useState<string | null>(null);
  const [messages, setMessages] = useState<AskMessage[]>([]);
  const [prompt, setPrompt] = useState('');
  const [status, setStatus] = useState<'loading' | 'idle' | 'sending' | 'error'>('loading');
  const [error, setError] = useState<string | null>(null);
  const [convertingId, setConvertingId] = useState<number | null>(null);

  useEffect(() => {
    let active = true;
    const open = async () => {
      setStatus('loading'); setError(null);
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
        setConversationId(id); setMessages(Array.isArray(transcriptBody.messages) ? transcriptBody.messages : []); setStatus('idle');
      } catch (err) {
        if (!active) return;
        setError(err instanceof Error ? err.message : 'Inline /ask is unavailable.'); setStatus('error');
      }
    };
    void open();
    return () => { active = false; };
  }, [location.filePath, location.side, location.startLine, location.endLine]);

  const send = async () => {
    const text = prompt.trim();
    if (!text || !conversationId || status === 'sending') return;
    const optimisticUser: AskMessage = { id: -Date.now(), role: 'user', body: text, pending: false };
    const optimisticAssistant: AskMessage = { id: -(Date.now() + 1), role: 'assistant', body: '', pending: true };
    setMessages((current) => [...current, optimisticUser, optimisticAssistant]);
    setPrompt(''); setStatus('sending'); setError(null);
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
        if (event === 'delta' && payload.delta) setMessages((current) => current.map((message) => message.id === optimisticAssistant.id ? { ...message, body: message.body + payload.delta } : message));
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
      setStatus('idle');
    } catch (err) {
      setMessages((current) => current.map((message) => message.id === optimisticAssistant.id ? { ...message, pending: false } : message));
      setError(err instanceof Error ? err.message : 'Inline /ask failed.'); setStatus('error');
    }
  };

  const convert = async (message: AskMessage) => {
    if (!message.body.trim()) return;
    setConvertingId(message.id); setError(null);
    try { await onConvertToReviewComment(message.body.trim()); }
    catch (err) { setError(err instanceof Error ? err.message : 'Could not create the review comment.'); }
    finally { setConvertingId(null); }
  };

  return <section className="m-2 mx-3 rounded-md border border-blue-500/40 border-l-4 border-l-blue-400 bg-github-bg-tertiary p-3" aria-label={`Inline ask for ${label(location)}`}>
    <div className="mb-2 flex items-center gap-2"><strong className="text-sm text-blue-200">/ask · private Copilot research</strong><span className="text-xs text-github-text-muted">{label(location)}</span><button type="button" onClick={onClose} className="ml-auto text-xs underline">Close</button></div>
    <p className="mb-2 text-xs text-github-text-muted">This saved conversation is separate from review feedback. Only “Convert to review comment” shares a chosen answer.</p>
    {error && <div role="alert" className="mb-2 text-xs text-red-300">{error}</div>}
    <div className="max-h-56 space-y-2 overflow-y-auto">
      {status === 'loading' && <div className="text-xs text-github-text-muted">Restoring this line’s saved /ask transcript…</div>}
      {messages.map((message) => <article key={message.id} className="rounded border border-github-border bg-github-bg-secondary p-2 text-xs">
        <div className="mb-1 flex items-center gap-2 text-[10px] text-github-text-muted"><span>{message.role}{message.pending ? ' · streaming…' : ''}</span>{message.role === 'assistant' && !message.pending && <button type="button" onClick={() => void convert(message)} disabled={convertingId === message.id} className="ml-auto underline disabled:opacity-50">{convertingId === message.id ? 'Converting…' : 'Convert to review comment'}</button>}</div>
        <ReactMarkdown remarkPlugins={[remarkGfm]}>{message.body || (message.pending ? '_Thinking…_' : '')}</ReactMarkdown>
      </article>)}
    </div>
    <textarea value={prompt} onChange={(event) => setPrompt(event.target.value)} onKeyDown={(event) => { if ((event.metaKey || event.ctrlKey) && event.key === 'Enter') void send(); }} disabled={status === 'loading' || status === 'sending'} placeholder="Ask about this selected code…" rows={2} className="mt-2 w-full rounded border border-github-border bg-github-bg-secondary p-2 text-xs" />
    <div className="mt-2 flex justify-end"><button type="button" onClick={() => void send()} disabled={!prompt.trim() || status === 'loading' || status === 'sending'} className="rounded border border-github-border px-2 py-1 text-xs disabled:opacity-50">{status === 'sending' ? 'Asking…' : 'Ask'}</button></div>
  </section>;
}
