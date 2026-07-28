import { useCallback, useEffect, useRef, useState } from 'react';
import ReactMarkdown from 'react-markdown';
import remarkGfm from 'remark-gfm';

import { daemonFetch } from '../services/daemonAuth';

interface AskModel { id: string; name?: string; }
interface AskMessage { id: string; role: 'user' | 'assistant' | 'system'; body: string; pending: boolean; createdAt: number; }
interface AskConversation { id: string; model?: string | null; updatedAt?: number; }
interface QuestionSet { id: string; name: string; questions: { body: string }[]; }

interface AskPanelProps {
  collapsed: boolean;
  onToggleCollapsed: () => void;
}

const ASK_DRAFT_STORAGE_KEY = 'cmux-localreview.ask-draft-v1';
const panelButton = {
  fontSize: 11, padding: '4px 8px', borderRadius: 4, border: '1px solid rgba(127,127,127,0.4)',
  background: 'transparent', color: 'inherit', cursor: 'pointer',
} as const;

function storedDraft(): string {
  try { return window.localStorage.getItem(ASK_DRAFT_STORAGE_KEY) ?? ''; } catch { return ''; }
}

/** A review-scoped, persisted Copilot conversation. It never shares /btw messages. */
export function AskPanel({ collapsed, onToggleCollapsed }: AskPanelProps) {
  const [models, setModels] = useState<AskModel[]>([]);
  const [conversations, setConversations] = useState<AskConversation[]>([]);
  const [conversationId, setConversationId] = useState<string | null>(null);
  const [messages, setMessages] = useState<AskMessage[]>([]);
  const [questionSets, setQuestionSets] = useState<QuestionSet[]>([]);
  const [questionSetId, setQuestionSetId] = useState('');
  const [questionSetName, setQuestionSetName] = useState('');
  const [questionSetDraft, setQuestionSetDraft] = useState('');
  const [model, setModel] = useState('');
  const [prompt, setPrompt] = useState(storedDraft);
  const [sending, setSending] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [modelStatus, setModelStatus] = useState<'loading' | 'available' | 'unavailable'>('loading');
  const [editingQuestionSet, setEditingQuestionSet] = useState(false);
  const activeResponseRef = useRef<AbortController | null>(null);

  const loadConversation = useCallback(async (id: string) => {
    const response = await daemonFetch(`/api/ask/conversations/${encodeURIComponent(id)}`);
    if (!response.ok) throw new Error('Failed to load this /ask conversation.');
    const data = (await response.json()) as { conversation?: AskConversation; messages?: AskMessage[] };
    setConversationId(id);
    setMessages(Array.isArray(data.messages) ? data.messages : []);
    if (data.conversation?.model) setModel(data.conversation.model);
  }, []);

  const refresh = useCallback(async () => {
    setError(null);
    try {
      const [modelsResult, conversationsResult] = await Promise.allSettled([
        daemonFetch('/api/ask/models'),
        daemonFetch('/api/ask/conversations'),
      ]);
      const modelsResponse = modelsResult.status === 'fulfilled' ? modelsResult.value : null;
      const conversationsResponse = conversationsResult.status === 'fulfilled' ? conversationsResult.value : null;
      if (modelsResponse?.ok) {
        const modelData = (await modelsResponse.json()) as { models?: AskModel[] };
        const nextModels = Array.isArray(modelData.models) ? modelData.models : [];
        setModels(nextModels); setModelStatus('available'); setModel((current) => current || nextModels[0]?.id || '');
      } else {
        setModels([]); setModelStatus('unavailable');
        if (modelsResponse?.status === 401 || modelsResponse?.status === 503) {
          setError('Copilot SDK is unavailable or unauthenticated. Authenticate Copilot, then retry; saved transcripts remain available.');
        }
      }
      if (!conversationsResponse?.ok) throw new Error('Open this review with localreview-open to use saved /ask conversations.');
      const conversationData = (await conversationsResponse.json()) as { conversations?: AskConversation[] };
      const nextConversations = Array.isArray(conversationData.conversations) ? conversationData.conversations : [];
      setConversations(nextConversations);
      const selected = conversationId && nextConversations.some((item) => item.id === conversationId)
        ? conversationId : nextConversations[0]?.id;
      if (selected) await loadConversation(selected);
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Unable to load /ask.');
    }
  }, [conversationId, loadConversation]);

  const refreshQuestionSets = useCallback(async () => {
    const response = await daemonFetch('/api/ask/question-sets');
    if (!response.ok) throw new Error('Could not load question sets.');
    const data = (await response.json()) as { questionSets?: QuestionSet[] };
    const sets = Array.isArray(data.questionSets) ? data.questionSets : [];
    setQuestionSets(sets);
    setQuestionSetId((current) => {
      const selected = current && sets.some((set) => set.id === current) ? current : sets[0]?.id ?? '';
      const set = sets.find((candidate) => candidate.id === selected);
      setQuestionSetName(set?.name ?? '');
      setQuestionSetDraft((set?.questions ?? []).map((question) => question.body).join('\n\n'));
      return selected;
    });
  }, []);

  useEffect(() => { if (!collapsed) { void refresh(); void refreshQuestionSets().catch((err: unknown) => setError(err instanceof Error ? err.message : 'Unable to load question sets.')); } }, [collapsed, refresh, refreshQuestionSets]);
  useEffect(() => {
    try { window.localStorage.setItem(ASK_DRAFT_STORAGE_KEY, prompt); } catch { /* best effort */ }
  }, [prompt]);
  useEffect(() => () => activeResponseRef.current?.abort(), []);

  const createConversation = useCallback(async (): Promise<string> => {
    const response = await daemonFetch('/api/ask/conversations', {
      method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ model: model || undefined }),
    });
    if (!response.ok) throw new Error('Could not start a /ask conversation.');
    const data = (await response.json()) as { conversation?: AskConversation };
    if (!data.conversation?.id) throw new Error('The /ask server did not return a conversation id.');
    setConversations((current) => [data.conversation!, ...current.filter((item) => item.id !== data.conversation!.id)]);
    setConversationId(data.conversation.id);
    setMessages([]);
    return data.conversation.id;
  }, [model]);

  const updateModel = useCallback(async (nextModel: string) => {
    setModel(nextModel);
    if (!conversationId) return;
    try {
      const response = await daemonFetch(`/api/ask/conversations/${encodeURIComponent(conversationId)}/model`, {
        method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ model: nextModel }),
      });
      if (!response.ok) throw new Error('Unable to set this model.');
    } catch (err) { setError(err instanceof Error ? err.message : 'Unable to set this model.'); }
  }, [conversationId]);

  const send = useCallback(async () => {
    const text = prompt.trim();
    if (!text || sending) return;
    setSending(true); setError(null); setPrompt('');
    try {
      const id = conversationId ?? await createConversation();
      const optimisticId = `stream-${Date.now()}`;
      setMessages((current) => [...current, { id: `user-${Date.now()}`, role: 'user', body: text, pending: false, createdAt: Date.now() }, { id: optimisticId, role: 'assistant', body: '', pending: true, createdAt: Date.now() }]);
      const controller = new AbortController();
      activeResponseRef.current = controller;
      const response = await daemonFetch(`/api/ask/conversations/${encodeURIComponent(id)}/messages`, {
        method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ prompt: text }), signal: controller.signal,
      });
      if (!response.ok || !response.body) throw new Error('The /ask request could not be started.');
      const reader = response.body.getReader(); const decoder = new TextDecoder(); let buffer = '';
      const applyEvent = (block: string) => {
        const event = block.split('\n').find((line) => line.startsWith('event:'))?.slice(6).trim();
        const raw = block.split('\n').filter((line) => line.startsWith('data:')).map((line) => line.slice(5).trim()).join('\n');
        if (!raw) return;
        try {
          const payload = JSON.parse(raw) as { delta?: string; content?: string; error?: string };
          if (event === 'delta' && payload.delta) setMessages((current) => current.map((message) => message.id === optimisticId ? { ...message, body: message.body + payload.delta } : message));
          if (event === 'done') setMessages((current) => current.map((message) => message.id === optimisticId ? { ...message, body: payload.content ?? message.body, pending: false } : message));
          if (event === 'error') throw new Error(payload.error ?? 'The /ask agent reported an error.');
        } catch (err) { if (err instanceof Error) setError(err.message); }
      };
      while (true) {
        const result = await reader.read();
        buffer += decoder.decode(result.value ?? new Uint8Array(), { stream: !result.done });
        const events = buffer.split(/\r?\n\r?\n/); buffer = events.pop() ?? '';
        events.forEach(applyEvent);
        if (result.done) break;
      }
      if (buffer) applyEvent(buffer);
      await loadConversation(id);
    } catch (err) {
      if ((err as { name?: string }).name !== 'AbortError') setError(err instanceof Error ? err.message : 'Failed to send /ask message.');
    } finally { activeResponseRef.current = null; setSending(false); }
  }, [conversationId, createConversation, loadConversation, prompt, sending]);

  const cancel = useCallback(async () => {
    activeResponseRef.current?.abort();
    if (conversationId) await daemonFetch(`/api/ask/conversations/${encodeURIComponent(conversationId)}/cancel`, { method: 'POST' });
    setSending(false);
    setMessages((current) => current.map((message) => message.pending ? { ...message, pending: false } : message));
  }, [conversationId]);

  const saveQuestionSet = useCallback(async () => {
    const questions = questionSetDraft.split(/\n\s*\n/).map((item) => item.trim()).filter(Boolean);
    if (!questions.length) { setError('Write one or more questions separated by blank lines first.'); return; }
    const name = questionSetName.trim() || 'Untitled question set';
    try {
      const response = await daemonFetch('/api/ask/question-sets', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ name, questions }) });
      if (!response.ok) throw new Error('Could not save question set.');
      const data = (await response.json()) as { questionSet?: QuestionSet };
      if (data.questionSet) { setQuestionSets((current) => [data.questionSet!, ...current]); setQuestionSetId(data.questionSet.id); setQuestionSetName(data.questionSet.name); setQuestionSetDraft(questions.join('\n\n')); setEditingQuestionSet(true); }
    } catch (err) { setError(err instanceof Error ? err.message : 'Could not save question set.'); }
  }, [questionSetDraft, questionSetName]);

  const selectQuestionSet = useCallback((id: string) => {
    const selected = questionSets.find((set) => set.id === id);
    setQuestionSetId(id); setQuestionSetName(selected?.name ?? '');
    setQuestionSetDraft((selected?.questions ?? []).map((question) => question.body).join('\n\n'));
  }, [questionSets]);

  const updateQuestionSet = useCallback(async () => {
    if (!questionSetId) return;
    const name = questionSetName.trim();
    const questions = questionSetDraft.split(/\n\s*\n/).map((item) => item.trim()).filter(Boolean);
    if (!name || !questions.length) { setError('A question set needs a name and at least one question.'); return; }
    try {
      const response = await daemonFetch(`/api/ask/question-sets/${encodeURIComponent(questionSetId)}`, { method: 'PUT', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ name, questions }) });
      if (!response.ok) throw new Error('Could not update question set.');
      const data = (await response.json()) as { questionSet?: QuestionSet };
      if (data.questionSet) setQuestionSets((current) => current.map((set) => set.id === data.questionSet!.id ? data.questionSet! : set));
      setEditingQuestionSet(false);
    } catch (err) { setError(err instanceof Error ? err.message : 'Could not update question set.'); }
  }, [questionSetDraft, questionSetId, questionSetName]);

  const deleteSelectedQuestionSet = useCallback(async () => {
    if (!questionSetId || !window.confirm(`Delete “${questionSetName || 'this question set'}”?`)) return;
    try {
      const response = await daemonFetch(`/api/ask/question-sets/${encodeURIComponent(questionSetId)}`, { method: 'DELETE' });
      if (!response.ok) throw new Error('Could not delete question set.');
      const remaining = questionSets.filter((set) => set.id !== questionSetId);
      setQuestionSets(remaining); selectQuestionSet(remaining[0]?.id ?? ''); setEditingQuestionSet(false);
    } catch (err) { setError(err instanceof Error ? err.message : 'Could not delete question set.'); }
  }, [questionSetId, questionSetName, questionSets, selectQuestionSet]);

  const sendQuestionSet = useCallback(async (mode: 'combined' | 'sequential') => {
    if (!questionSetId) { setError('Choose a question set first.'); return; }
    try {
      const id = conversationId ?? await createConversation();
      setSending(true);
      const response = await daemonFetch(`/api/ask/question-sets/${encodeURIComponent(questionSetId)}/send`, { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ conversationId: id, mode }) });
      if (!response.ok) throw new Error('Could not start question set.');
      await response.text(); // Consume the SSE stream; transcript is persisted server-side.
      await loadConversation(id);
    } catch (err) { setError(err instanceof Error ? err.message : 'Could not send question set.'); }
    finally { setSending(false); }
  }, [conversationId, createConversation, loadConversation, questionSetId]);

  if (collapsed) return <button onClick={onToggleCollapsed} title="Open /ask panel" style={{ ...panelButton, position: 'fixed', right: 86, bottom: 12, zIndex: 60, borderRadius: 20 }}>/ask</button>;

  return <section style={{ position: 'fixed', right: 0, top: 0, bottom: 0, width: 390, zIndex: 56, display: 'flex', flexDirection: 'column', background: 'var(--bg-primary, #161b22)', borderLeft: '1px solid rgba(127,127,127,0.35)' }} aria-label="Copilot ask">
    <div style={{ display: 'flex', gap: 7, alignItems: 'center', padding: '9px 11px', borderBottom: '1px solid rgba(127,127,127,0.3)' }}>
      <strong style={{ fontSize: 13 }}>/ask</strong><span title="Fresh Copilot SDK chat, separate from ACP review feedback" style={{ fontSize: 10, opacity: 0.6 }}>fresh SDK chat</span>
      <select value={model} onChange={(event) => void updateModel(event.target.value)} style={{ marginLeft: 'auto', maxWidth: 210, fontSize: 11, background: 'transparent', color: 'inherit' }}>
        {models.length === 0 && <option value="">{modelStatus === 'loading' ? 'Checking Copilot…' : 'Copilot unavailable'}</option>}
        {models.map((item) => <option key={item.id} value={item.id}>{item.name ?? item.id}</option>)}
      </select>
      {questionSets.length > 0 && <select aria-label="Question set" value={questionSetId} onChange={(event) => selectQuestionSet(event.target.value)} style={{ maxWidth: 120, fontSize: 11, background: 'transparent', color: 'inherit' }}>{questionSets.map((set) => <option key={set.id} value={set.id}>{set.name}</option>)}</select>}
      <button onClick={() => void createConversation()} style={panelButton}>New</button>
      <button onClick={onToggleCollapsed} style={panelButton}>✕</button>
    </div>
    {conversations.length > 1 && <select aria-label="Ask conversation" value={conversationId ?? ''} onChange={(event) => void loadConversation(event.target.value)} style={{ margin: '7px 10px 0', fontSize: 11, background: 'transparent', color: 'inherit' }}>{conversations.map((item) => <option key={item.id} value={item.id}>{item.id.slice(0, 8)} {item.model ? `· ${item.model}` : ''}</option>)}</select>}
    {error && <div role="alert" style={{ padding: '7px 10px', color: '#e5534b', fontSize: 11 }}>{error} <button onClick={() => void refresh()} style={{ ...panelButton, marginLeft: 5 }}>Retry</button></div>}
    {editingQuestionSet && <div style={{ padding: 9, borderBottom: '1px solid rgba(127,127,127,0.3)', display: 'grid', gap: 5 }}>
      <label style={{ fontSize: 10, opacity: 0.7 }}>Named question set — separate questions with a blank line; order is preserved.</label>
      <input aria-label="Question set name" value={questionSetName} onChange={(event) => setQuestionSetName(event.target.value)} style={{ fontSize: 12, padding: 5, background: 'transparent', color: 'inherit', border: '1px solid rgba(127,127,127,0.4)' }} />
      <textarea aria-label="Questions" value={questionSetDraft} onChange={(event) => setQuestionSetDraft(event.target.value)} rows={4} style={{ fontSize: 11, padding: 5, background: 'transparent', color: 'inherit', border: '1px solid rgba(127,127,127,0.4)' }} />
      <div style={{ display: 'flex', justifyContent: 'flex-end', gap: 5 }}><button onClick={() => void (questionSetId ? updateQuestionSet() : saveQuestionSet())} style={panelButton}>Save set</button>{questionSetId && <button onClick={() => void deleteSelectedQuestionSet()} style={panelButton}>Delete</button>}<button onClick={() => setEditingQuestionSet(false)} style={panelButton}>Done</button></div>
    </div>}
    <div style={{ flex: 1, overflowY: 'auto', padding: 11, display: 'flex', flexDirection: 'column', gap: 9 }}>
      {messages.length === 0 && <div style={{ fontSize: 12, opacity: 0.65 }}>Ask about the workspace with a read-only Copilot context. This is separate from /btw.</div>}
      {messages.map((message) => <article key={message.id} style={{ alignSelf: message.role === 'user' ? 'flex-end' : 'stretch', maxWidth: message.role === 'user' ? '88%' : undefined, border: '1px solid rgba(127,127,127,0.28)', borderRadius: 6, padding: '7px 9px', fontSize: 12, background: message.role === 'user' ? 'rgba(127,127,127,0.12)' : 'transparent' }}><div style={{ fontSize: 10, opacity: 0.58, marginBottom: 4 }}>{message.role}{message.pending ? ' · thinking…' : ''}</div><ReactMarkdown remarkPlugins={[remarkGfm]}>{message.body || (message.pending ? '_thinking…_' : '')}</ReactMarkdown></article>)}
    </div>
    <div style={{ padding: 10, borderTop: '1px solid rgba(127,127,127,0.3)' }}>
      <textarea value={prompt} onChange={(event) => setPrompt(event.target.value)} onKeyDown={(event) => { if ((event.metaKey || event.ctrlKey) && event.key === 'Enter') void send(); }} rows={3} placeholder="Ask about this review…" disabled={sending} style={{ width: '100%', resize: 'vertical', fontSize: 12, padding: 6, background: 'transparent', border: '1px solid rgba(127,127,127,0.4)', borderRadius: 4, color: 'inherit' }} />
      <div style={{ display: 'flex', justifyContent: 'flex-end', gap: 6, marginTop: 6, flexWrap: 'wrap' }}>
        <button onClick={() => { setQuestionSetName(''); setQuestionSetDraft(prompt); setEditingQuestionSet(true); }} disabled={!prompt.trim()} style={panelButton}>Save as set</button>
        {questionSetId && <button onClick={() => setEditingQuestionSet((value) => !value)} style={panelButton}>{editingQuestionSet ? 'Hide set' : 'Edit set'}</button>}
        {questionSetId && <><button onClick={() => void sendQuestionSet('combined')} disabled={sending} style={panelButton}>Ask set</button><button onClick={() => void sendQuestionSet('sequential')} disabled={sending} style={panelButton}>Ask one-by-one</button></>}
        {sending && <button onClick={() => void cancel()} style={panelButton}>Cancel</button>}<button onClick={() => void send()} disabled={sending || !prompt.trim()} style={panelButton}>{sending ? 'Asking…' : 'Ask'}</button></div>
    </div>
  </section>;
}
