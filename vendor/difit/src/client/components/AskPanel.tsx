import { useCallback, useEffect, useRef, useState } from 'react';
import ReactMarkdown from 'react-markdown';
import remarkGfm from 'remark-gfm';

import { daemonFetch } from '../services/daemonAuth';
import { resolveEventSourceUrl } from '../utils/eventSourceUrl';

type ReasoningEffort = 'low' | 'medium' | 'high' | 'xhigh';
type ContextTier = 'default' | 'long_context';
interface AskModel {
  id: string;
  name?: string;
  capabilities?: { supports?: { reasoningEffort?: boolean }; limits?: { max_context_window_tokens?: number } };
  supportedReasoningEfforts?: ReasoningEffort[];
  defaultReasoningEffort?: ReasoningEffort;
}
interface AskWorkspaceSettings { model?: string | null; reasoningEffort?: ReasoningEffort | null; contextTier?: ContextTier | null; }
interface AskModelsResponse { models?: AskModel[]; state?: 'ready' | 'unavailable'; warning?: string; workspaceDefaults?: AskWorkspaceSettings; }
interface AskMessage { id: string | number; role: 'user' | 'assistant' | 'system'; body: string; pending: boolean; createdAt: number; location?: { filePath?: string; startLine?: number } | null; }
interface AskConversation { id: string; model?: string | null; reasoningEffort?: ReasoningEffort | null; contextTier?: ContextTier | null; context?: { filePath?: string } | null; reviewSessionId?: number | null; archivedAt?: number | null; updatedAt?: number; }
interface QuestionSet { id: string; name: string; questions: { body: string }[]; }

interface AskPanelProps {
  collapsed: boolean;
  onToggleCollapsed: () => void;
  requestedConversationId?: string;
  onRequestedConversationOpened?: () => void;
}

const ASK_DRAFT_STORAGE_KEY = 'cmux-localreview.ask-draft-v1';
const panelButton = {
  fontSize: 11, padding: '4px 8px', borderRadius: 4, border: '1px solid rgba(127,127,127,0.4)',
  background: 'transparent', color: 'inherit', cursor: 'pointer',
} as const;

function storedDraft(): string {
  try { return window.localStorage.getItem(ASK_DRAFT_STORAGE_KEY) ?? ''; } catch { return ''; }
}

function sameWorkspaceDefaults(left: AskWorkspaceSettings | null, right: AskWorkspaceSettings | null): boolean {
  return left?.model === right?.model
    && left?.reasoningEffort === right?.reasoningEffort
    && left?.contextTier === right?.contextTier;
}

/** A review-scoped, persisted Copilot conversation. It never shares /btw messages. */
export function AskPanel({ collapsed, onToggleCollapsed, requestedConversationId, onRequestedConversationOpened }: AskPanelProps) {
  const [models, setModels] = useState<AskModel[]>([]);
  const [conversations, setConversations] = useState<AskConversation[]>([]);
  const [activeReviewSessionId, setActiveReviewSessionId] = useState<number | null>(null);
  const [showHistory, setShowHistory] = useState(false);
  const [conversationId, setConversationId] = useState<string | null>(null);
  const [messages, setMessages] = useState<AskMessage[]>([]);
  const [questionSets, setQuestionSets] = useState<QuestionSet[]>([]);
  const [questionSetId, setQuestionSetId] = useState('');
  const [questionSetName, setQuestionSetName] = useState('');
  const [questionSetDraft, setQuestionSetDraft] = useState('');
  const [model, setModel] = useState('');
  const [reasoningEffort, setReasoningEffort] = useState<ReasoningEffort | ''>('');
  const [contextTier, setContextTier] = useState<ContextTier>('default');
  const [workspaceDefaults, setWorkspaceDefaults] = useState<AskWorkspaceSettings | null>(null);
  const [prompt, setPrompt] = useState(storedDraft);
  const [retryPrompt, setRetryPrompt] = useState<string | null>(null);
  const [sending, setSending] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [modelStatus, setModelStatus] = useState<'loading' | 'available' | 'unavailable'>('loading');
  const [editingQuestionSet, setEditingQuestionSet] = useState(false);
  const activeResponseRef = useRef<AbortController | null>(null);
  const sendInFlightRef = useRef(false);
  const activeEventSourceRef = useRef<EventSource | null>(null);
  const abortEventStreamRef = useRef<(() => void) | null>(null);
  const workspaceDefaultsRef = useRef<AskWorkspaceSettings | null>(null);
  // Several entry points can restore a transcript at once (panel refresh,
  // finished inline turn, and “Open full /ask chat”). Only the newest result
  // may update the panel; otherwise an earlier response can hide a just-made
  // inline question until the reviewer reloads the page.
  const transcriptLoadSequence = useRef(0);

  const loadConversation = useCallback(async (id: string) => {
    const sequence = ++transcriptLoadSequence.current;
    const response = await daemonFetch(`/api/ask/conversations/${encodeURIComponent(id)}`);
    if (!response.ok) throw new Error('Failed to load this /ask conversation.');
    const data = (await response.json()) as { conversation?: AskConversation; messages?: AskMessage[] };
    if (sequence !== transcriptLoadSequence.current) return;
    setConversationId(id);
    setMessages(Array.isArray(data.messages) ? data.messages : []);
    // A conversation with no override inherits the durable workspace picker.
    // Do not leave controls displaying values from whichever conversation was
    // viewed immediately before it.
    const defaults = workspaceDefaultsRef.current;
    setModel(data.conversation?.model ?? defaults?.model ?? '');
    setReasoningEffort(data.conversation?.reasoningEffort ?? defaults?.reasoningEffort ?? '');
    setContextTier(data.conversation?.contextTier ?? defaults?.contextTier ?? 'default');
  }, []);

  const refresh = useCallback(async () => {
    setError(null);
    try {
      const [modelsResult, conversationsResult] = await Promise.allSettled([
        daemonFetch('/api/ask/models'),
        daemonFetch('/api/ask/conversations?includeArchived=true'),
      ]);
      const modelsResponse = modelsResult.status === 'fulfilled' ? modelsResult.value : null;
      const conversationsResponse = conversationsResult.status === 'fulfilled' ? conversationsResult.value : null;
      if (modelsResponse?.ok) {
        const modelData = (await modelsResponse.json()) as AskModelsResponse;
        const nextModels = Array.isArray(modelData.models) ? modelData.models : [];
        const defaults = modelData.workspaceDefaults ?? null;
        // `/ask/models` is refreshed whenever the panel opens. Preserve
        // object identity for an unchanged default; otherwise `refresh` would
        // be recreated through `loadConversation` and continuously refetch.
        setWorkspaceDefaults((current) => {
          if (sameWorkspaceDefaults(current, defaults)) return current;
          workspaceDefaultsRef.current = defaults;
          return defaults;
        });
        if (modelData.state === 'unavailable') {
          // A 200 fallback is intentionally not an authenticated model list.
          // Do not let the reviewer unknowingly start a question with a fake
          // picker choice; preserve the saved draft and show the daemon's
          // token-free recovery guidance instead.
          setModels([]); setModelStatus('unavailable');
          setError(modelData.warning || 'Copilot SDK is unavailable. Restart the localreview daemon, then refresh /ask; saved transcripts remain available.');
        } else {
          setModels(nextModels); setModelStatus('available');
          // A workspace picker default is durable independently of a
          // conversation. Only adopt it before a transcript is selected: an
          // explicit conversation override must always remain authoritative.
          if (!conversationId) {
            setModel(defaults?.model ?? nextModels[0]?.id ?? '');
            setReasoningEffort(defaults?.reasoningEffort ?? '');
            setContextTier(defaults?.contextTier ?? 'default');
          }
        }
      } else {
        setModels([]); setModelStatus('unavailable');
        if (modelsResponse?.status === 401 || modelsResponse?.status === 503) {
          setError('Copilot SDK is unavailable or unauthenticated. Authenticate Copilot, then retry; saved transcripts remain available.');
        }
      }
      if (!conversationsResponse?.ok) throw new Error('Open this review with localreview-open to use saved /ask conversations.');
      const conversationData = (await conversationsResponse.json()) as { conversations?: AskConversation[]; activeReviewSessionId?: number };
      const nextConversations = Array.isArray(conversationData.conversations) ? conversationData.conversations : [];
      setConversations(nextConversations);
      const nextReviewSessionId = typeof conversationData.activeReviewSessionId === 'number' ? conversationData.activeReviewSessionId : null;
      setActiveReviewSessionId(nextReviewSessionId);
      const active = nextConversations.filter((item) => !item.archivedAt && item.reviewSessionId === nextReviewSessionId);
      const selected = conversationId && (showHistory || active.some((item) => item.id === conversationId)) && nextConversations.some((item) => item.id === conversationId)
        ? conversationId : active.find((item) => !item.context?.filePath)?.id ?? active[0]?.id;
      if (selected) await loadConversation(selected);
      else { setConversationId(null); setMessages([]); }
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Unable to load /ask.');
    }
  }, [conversationId, loadConversation, showHistory]);

  useEffect(() => {
    if (!requestedConversationId || collapsed) return;
    void loadConversation(requestedConversationId)
      .catch((err: unknown) => setError(err instanceof Error ? err.message : 'Could not open the inline /ask conversation.'))
      .finally(() => onRequestedConversationOpened?.());
  }, [collapsed, loadConversation, onRequestedConversationOpened, requestedConversationId]);

  useEffect(() => {
    const refreshTranscript = (event: Event) => {
      const id = (event as CustomEvent<{ conversationId?: string | null }>).detail?.conversationId;
      if (id && id === conversationId) void loadConversation(id).catch(() => undefined);
    };
    window.addEventListener('cmux-localreview:ask-updated', refreshTranscript);
    return () => window.removeEventListener('cmux-localreview:ask-updated', refreshTranscript);
  }, [conversationId, loadConversation]);

  useEffect(() => {
    const refreshForNewReview = () => { setShowHistory(false); void refresh(); };
    window.addEventListener('cmux-localreview:review-round-started', refreshForNewReview);
    return () => window.removeEventListener('cmux-localreview:review-round-started', refreshForNewReview);
  }, [refresh]);

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
  useEffect(() => () => { activeResponseRef.current?.abort(); abortEventStreamRef.current?.(); }, []);

  const createConversation = useCallback(async (): Promise<string> => {
    const response = await daemonFetch('/api/ask/conversations/fresh', {
      method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ model: model || undefined, reasoningEffort: reasoningEffort || undefined, contextTier }),
    });
    if (!response.ok) throw new Error('Could not start a /ask conversation.');
    const data = (await response.json()) as { conversation?: AskConversation };
    if (!data.conversation?.id) throw new Error('The /ask server did not return a conversation id.');
    setConversations((current) => [
      data.conversation!,
      ...current
        .map((item) => item.reviewSessionId === activeReviewSessionId && !item.archivedAt ? { ...item, archivedAt: Date.now() } : item)
        .filter((item) => item.id !== data.conversation!.id),
    ]);
    setConversationId(data.conversation.id);
    setMessages([]);
    return data.conversation.id;
  }, [activeReviewSessionId, contextTier, model, reasoningEffort]);

  const resumeConversation = useCallback(async () => {
    if (!conversationId) return;
    try {
      const response = await daemonFetch(`/api/ask/conversations/${encodeURIComponent(conversationId)}/resume`, { method: 'POST' });
      if (!response.ok) throw new Error('This historical Ask session cannot be resumed for the current review.');
      await refresh();
    } catch (err) { setError(err instanceof Error ? err.message : 'Unable to resume this Ask session.'); }
  }, [conversationId, refresh]);

  const updateSettings = useCallback(async (next: { model?: string; reasoningEffort?: ReasoningEffort | ''; contextTier?: ContextTier }) => {
    const nextModel = next.model ?? model;
    const nextEffort = next.reasoningEffort ?? reasoningEffort;
    const nextTier = next.contextTier ?? contextTier;
    try {
      // Keep the workspace picker durable even before the first question.
      // This is deliberately separate from a selected conversation: it
      // governs future fresh chats, while the conversation retains an
      // explicit model/config override after it has started.
      const defaultsResponse = await daemonFetch('/api/ask/settings', {
        method: 'PUT', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ model: nextModel, reasoningEffort: nextEffort || undefined, contextTier: nextTier }),
      });
      if (!defaultsResponse.ok) throw new Error('Unable to save this workspace’s Copilot defaults.');
      const defaultsData = (await defaultsResponse.json()) as { workspaceDefaults?: AskWorkspaceSettings };
      const savedDefaults = defaultsData.workspaceDefaults ?? { model: nextModel || null, reasoningEffort: nextEffort || null, contextTier: nextTier };
      setWorkspaceDefaults((current) => {
        if (sameWorkspaceDefaults(current, savedDefaults)) return current;
        workspaceDefaultsRef.current = savedDefaults;
        return savedDefaults;
      });
      if (conversationId) {
        const response = await daemonFetch(`/api/ask/conversations/${encodeURIComponent(conversationId)}/settings`, {
          method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ model: nextModel, reasoningEffort: nextEffort || undefined, contextTier: nextTier }),
        });
        if (!response.ok) throw new Error('Workspace defaults saved, but this conversation could not switch. Cancel or wait for the current response, then try again.');
        const data = (await response.json()) as { conversation?: AskConversation };
        if (data.conversation) setConversations((current) => current.map((item) => item.id === data.conversation!.id ? data.conversation! : item));
      }
      setModel(nextModel); setReasoningEffort(nextEffort); setContextTier(nextTier);
    } catch (err) { setError(err instanceof Error ? err.message : 'Unable to update Copilot settings.'); }
  }, [contextTier, conversationId, model, reasoningEffort]);

  const sendPrompt = useCallback(async (text: string) => {
    const id = conversationId ?? await createConversation();
    const optimisticId = `stream-${Date.now()}-${Math.random().toString(36).slice(2)}`;
    setMessages((current) => [...current, { id: `user-${Date.now()}`, role: 'user', body: text, pending: false, createdAt: Date.now() }, { id: optimisticId, role: 'assistant', body: '', pending: true, createdAt: Date.now() }]);
    const controller = new AbortController();
    activeResponseRef.current = controller;

    // The native daemon deliberately returns 202 from POST /messages and
    // streams the actual response from this durable per-conversation SSE
    // endpoint. Subscribe before posting so the first Copilot delta cannot be
    // lost between accepting the turn and opening the live view.
    const source = new EventSource(resolveEventSourceUrl(`/api/ask/conversations/${encodeURIComponent(id)}/events`));
    activeEventSourceRef.current = source;
    let settled = false;
    let readySettled = false;
    let stopWaitingForReady: ((reason: Error) => void) | null = null;
    let closeStream: (() => void) | null = null;
    const ready = new Promise<void>((resolve, reject) => {
      const finish = (reason?: Error) => {
        if (readySettled) return;
        readySettled = true;
        window.clearTimeout(timeout);
        if (reason) reject(reason); else resolve();
      };
      const timeout = window.setTimeout(() => finish(new Error('Timed out while connecting to the Copilot stream.')), 10_000);
      stopWaitingForReady = (reason) => finish(reason);
      source.addEventListener('status', () => finish(), { once: true });
      source.onerror = () => finish(new Error('Copilot stream disconnected before the question started.'));
    });
    const streamed = new Promise<void>((resolve, reject) => {
      const close = (error?: Error) => {
        if (settled) return;
        settled = true;
        source.close();
        if (activeEventSourceRef.current === source) activeEventSourceRef.current = null;
        error ? reject(error) : resolve();
      };
      closeStream = () => close();
      // Cancelling before the EventSource has emitted its initial status used
      // to leave `ready` pending for ten seconds. Resolve the stream and
      // reject that connection wait together, so the UI always re-enables.
      abortEventStreamRef.current = () => {
        stopWaitingForReady?.(new DOMException('The /ask response was cancelled.', 'AbortError'));
        closeStream?.();
      };
      source.addEventListener('delta', (event) => {
        try {
          const payload = JSON.parse((event as MessageEvent).data) as { text?: string };
          if (payload.text) setMessages((current) => current.map((message) => message.id === optimisticId ? { ...message, body: message.body + payload.text } : message));
        } catch { /* a malformed transport event is ignored; durable history remains authoritative */ }
      });
      source.addEventListener('done', () => {
        setMessages((current) => current.map((message) => message.id === optimisticId ? { ...message, pending: false } : message));
        close();
      });
      source.addEventListener('error', (event) => {
        const data = (event as MessageEvent).data;
        if (typeof data === 'string' && data) {
          try { close(new Error((JSON.parse(data) as { error?: string }).error ?? 'Copilot could not complete this response.')); return; } catch { /* fall through */ }
        }
        // Browser EventSource uses `error` for transient connection errors as
        // well. Keep the durable turn pending until the daemon settles it.
      });
    });
    try {
      await ready;
      const response = await daemonFetch(`/api/ask/conversations/${encodeURIComponent(id)}/messages`, {
        method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ body: text }), signal: controller.signal,
      });
      if (!response.ok) {
        const detail = await response.json().catch(() => ({})) as { error?: string };
        throw new Error(detail.error ?? 'The /ask request could not be started.');
      }
      await streamed;
      await loadConversation(id);
    } catch (err) {
      source.close();
      if (activeEventSourceRef.current === source) activeEventSourceRef.current = null;
      if (abortEventStreamRef.current) abortEventStreamRef.current = null;
      const cancelled = (err as { name?: string }).name === 'AbortError';
      setMessages((current) => current.map((message) => message.id === optimisticId ? { ...message, pending: false, body: message.body || (cancelled ? 'Response cancelled before it completed.' : 'Copilot did not complete this response.') } : message));
      throw err;
    } finally { activeResponseRef.current = null; }
  }, [conversationId, createConversation, loadConversation]);

  const send = useCallback(async (promptOverride?: string) => {
    const text = (promptOverride ?? prompt).trim();
    // State updates are asynchronous. The ref closes the double-click/keyboard
    // race before `sending` has rendered disabled controls.
    if (!text || sending || sendInFlightRef.current) return;
	if (modelStatus === 'unavailable') {
	  setError('Copilot is unavailable. Follow the recovery steps above, then refresh /ask; this draft has not been sent.');
	  return;
	}
    sendInFlightRef.current = true;
    setSending(true); setError(null); setPrompt('');
    try {
      await sendPrompt(text);
      setRetryPrompt(null);
    } catch (err) {
      if ((err as { name?: string }).name !== 'AbortError') {
        setError(err instanceof Error ? err.message : 'Failed to send /ask message.');
        setRetryPrompt(text);
        // The regular draft store persists this text across a reload; a failed
        // transient stream must never strand the reviewer without their words.
        setPrompt((current) => current || text);
      }
    } finally {
      sendInFlightRef.current = false;
      setSending(false);
    }
  }, [modelStatus, prompt, sendPrompt, sending]);

  const cancel = useCallback(async () => {
    activeResponseRef.current?.abort();
    abortEventStreamRef.current?.(); abortEventStreamRef.current = null;
    try {
      if (!conversationId) return;
      const response = await daemonFetch(`/api/ask/conversations/${encodeURIComponent(conversationId)}/cancel`, { method: 'POST' });
      if (!response.ok) throw new Error('Copilot could not be cancelled.');
      // The server owns the canonical terminal marker (including a partial
      // response). Reload it rather than leaving an optimistic truncated row.
      await loadConversation(conversationId);
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Copilot could not be cancelled.');
    } finally {
      setSending(false);
    }
  }, [conversationId, loadConversation]);

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

  const startNewQuestionSet = useCallback((draft = '') => {
    // Clear the selected ID as well as the fields. Otherwise “Save as set”
    // would silently overwrite the currently selected named set.
    setQuestionSetId(''); setQuestionSetName(''); setQuestionSetDraft(draft);
    setEditingQuestionSet(true);
  }, []);

  const moveQuestion = useCallback((index: number, direction: -1 | 1) => {
    const questions = questionSetDraft.split(/\n\s*\n/).map((item) => item.trim()).filter(Boolean);
    const target = index + direction;
    if (target < 0 || target >= questions.length) return;
    const moved = questions[index]; const displaced = questions[target];
    if (moved === undefined || displaced === undefined) return;
    questions[index] = displaced; questions[target] = moved;
    setQuestionSetDraft(questions.join('\n\n'));
  }, [questionSetDraft]);

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
      const selected = questionSets.find((set) => set.id === questionSetId);
      const questions = selected?.questions.map((question) => question.body.trim()).filter(Boolean) ?? [];
      if (!questions.length) throw new Error('This question set has no questions.');
      setSending(true);
      setError(null);
      if (mode === 'combined') {
        await sendPrompt(`Please answer these review questions in order. Keep each answer clearly numbered.\n\n${questions.map((question, index) => `${index + 1}. ${question}`).join('\n')}`);
      } else {
        for (const question of questions) await sendPrompt(question);
      }
    } catch (err) { setError(err instanceof Error ? err.message : 'Could not send question set.'); }
    finally { setSending(false); }
  }, [questionSetId, questionSets, sendPrompt]);

  const selectedModel = models.find((candidate) => candidate.id === model);
  const supportedEfforts = selectedModel?.supportedReasoningEfforts ?? [];
  const maxContext = selectedModel?.capabilities?.limits?.max_context_window_tokens;
  const chooseModel = (nextModel: string) => {
    const selected = models.find((candidate) => candidate.id === nextModel);
    const nextEffort = selected?.supportedReasoningEfforts?.includes(reasoningEffort as ReasoningEffort)
      ? reasoningEffort
      : selected?.defaultReasoningEffort ?? '';
    void updateSettings({ model: nextModel, reasoningEffort: nextEffort });
  };
  const activeConversations = conversations.filter((item) => !item.archivedAt && item.reviewSessionId === activeReviewSessionId);
  const historicalConversations = conversations.filter((item) => item.archivedAt || item.reviewSessionId !== activeReviewSessionId);
  const visibleConversations = showHistory ? conversations : activeConversations;
  const selectedConversation = conversations.find((item) => item.id === conversationId);
  const historicalConversation = Boolean(selectedConversation && (selectedConversation.archivedAt || selectedConversation.reviewSessionId !== activeReviewSessionId));
	const copilotUnavailable = modelStatus === 'unavailable';

  if (collapsed) return <button onClick={onToggleCollapsed} title="Open /ask panel" style={{ ...panelButton, position: 'fixed', right: 86, bottom: 12, zIndex: 60, borderRadius: 20 }}>/ask</button>;

  return <section style={{ position: 'fixed', right: 0, top: 0, bottom: 0, width: 390, zIndex: 56, display: 'flex', flexDirection: 'column', background: 'var(--bg-primary, #161b22)', borderLeft: '1px solid rgba(127,127,127,0.35)' }} aria-label="Copilot ask">
    <div style={{ display: 'flex', gap: 7, alignItems: 'center', padding: '9px 11px', borderBottom: '1px solid rgba(127,127,127,0.3)' }}>
      <strong style={{ fontSize: 13 }}>/ask</strong><span title="Fresh Copilot SDK chat, separate from ACP review feedback" style={{ fontSize: 10, opacity: 0.6 }}>fresh SDK chat{workspaceDefaults?.model ? ' · workspace default saved' : ''}</span>
      <select aria-label="Copilot model" disabled={historicalConversation || sending} title={sending ? 'Cancel or wait for Copilot before changing this conversation’s model.' : 'The next question starts a fresh SDK session with this model.'} value={model} onChange={(event) => chooseModel(event.target.value)} style={{ marginLeft: 'auto', maxWidth: 210, fontSize: 11, background: 'transparent', color: 'inherit' }}>
        {models.length === 0 && <option value="">{modelStatus === 'loading' ? 'Checking Copilot…' : 'Copilot unavailable'}</option>}
        {models.map((item) => <option key={item.id} value={item.id}>{item.name ?? item.id}</option>)}
      </select>
      {questionSets.length > 0 && <select aria-label="Question set" value={questionSetId} onChange={(event) => selectQuestionSet(event.target.value)} style={{ maxWidth: 120, fontSize: 11, background: 'transparent', color: 'inherit' }}>{questionSets.map((set) => <option key={set.id} value={set.id}>{set.name}</option>)}</select>}
      <button onClick={() => startNewQuestionSet()} disabled={historicalConversation} style={panelButton}>New set</button>
      <button onClick={() => void createConversation()} style={panelButton}>Start fresh</button>
      <button onClick={onToggleCollapsed} style={panelButton}>✕</button>
    </div>
    <div aria-label="Copilot model settings" style={{ padding: '6px 10px', display: 'flex', gap: 6, flexWrap: 'wrap', alignItems: 'center', borderBottom: '1px solid rgba(127,127,127,0.22)', fontSize: 10 }}>
      {supportedEfforts.length > 0 ? <label>Thinking <select aria-label="Thinking level" disabled={historicalConversation || sending} value={reasoningEffort || selectedModel?.defaultReasoningEffort || supportedEfforts[0]} onChange={(event) => void updateSettings({ reasoningEffort: event.target.value as ReasoningEffort })} style={{ marginLeft: 3, fontSize: 10, background: 'transparent', color: 'inherit' }}>{supportedEfforts.map((effort) => <option key={effort} value={effort}>{effort}</option>)}</select></label> : <span title="This model does not expose a reasoning-effort control">Thinking: model default</span>}
      <label>Context <select aria-label="Context window tier" disabled={historicalConversation || sending} value={contextTier} onChange={(event) => void updateSettings({ contextTier: event.target.value as ContextTier })} style={{ marginLeft: 3, fontSize: 10, background: 'transparent', color: 'inherit' }}><option value="default">Default</option><option value="long_context">Long (if supported)</option></select></label>
      <span title="The maximum context window is reported by the installed Copilot runtime">{maxContext && maxContext > 0 ? `${Math.round(maxContext / 1000)}k max context` : 'Context limit reported by runtime'}</span>
    </div>
    {historicalConversations.length > 0 && <label style={{ margin: '7px 10px 0', fontSize: 11, opacity: 0.78 }}><input type="checkbox" checked={showHistory} onChange={(event) => setShowHistory(event.target.checked)} /> Show previous Ask sessions ({historicalConversations.length})</label>}
    {visibleConversations.length > 0 && <select aria-label="Ask conversation" value={conversationId ?? ''} onChange={(event) => void loadConversation(event.target.value)} style={{ margin: '7px 10px 0', fontSize: 11, background: 'transparent', color: 'inherit' }}>{visibleConversations.map((item) => <option key={item.id} value={item.id}>{item.archivedAt || item.reviewSessionId !== activeReviewSessionId ? 'Previous · ' : 'Current · '}{item.id.slice(0, 8)} {item.model ? `· ${item.model}` : ''}</option>)}</select>}
    {error && <div role="alert" style={{ padding: '7px 10px', color: '#e5534b', fontSize: 11 }}>{error} {retryPrompt && <button onClick={() => void send(retryPrompt)} disabled={sending} style={{ ...panelButton, marginLeft: 5 }}>Retry last question</button>} <button onClick={() => void refresh()} style={{ ...panelButton, marginLeft: 5 }}>Refresh chat</button></div>}
    {historicalConversation && <div role="status" style={{ padding: '7px 10px', color: '#b6c2cf', fontSize: 11, borderBottom: '1px solid rgba(127,127,127,0.25)' }}>Previous Ask session — read-only so it does not clutter or alter this review round.{selectedConversation?.reviewSessionId === activeReviewSessionId && <button onClick={() => void resumeConversation()} style={{ ...panelButton, marginLeft: 6 }}>Resume this session</button>}</div>}
    {editingQuestionSet && <div style={{ padding: 9, borderBottom: '1px solid rgba(127,127,127,0.3)', display: 'grid', gap: 5 }}>
      <label style={{ fontSize: 10, opacity: 0.7 }}>Named question set — separate questions with a blank line; order is preserved.</label>
      <input aria-label="Question set name" value={questionSetName} onChange={(event) => setQuestionSetName(event.target.value)} style={{ fontSize: 12, padding: 5, background: 'transparent', color: 'inherit', border: '1px solid rgba(127,127,127,0.4)' }} />
      <textarea aria-label="Questions" value={questionSetDraft} onChange={(event) => setQuestionSetDraft(event.target.value)} rows={4} style={{ fontSize: 11, padding: 5, background: 'transparent', color: 'inherit', border: '1px solid rgba(127,127,127,0.4)' }} />
      {questionSetDraft.split(/\n\s*\n/).map((item) => item.trim()).filter(Boolean).map((question, index, all) => <div key={`${index}-${question}`} aria-label={`Question ${index + 1}`} style={{ display: 'flex', gap: 4, alignItems: 'center', fontSize: 10 }}><span style={{ flex: 1, overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>{index + 1}. {question}</span><button aria-label={`Move question ${index + 1} up`} disabled={index === 0} onClick={() => moveQuestion(index, -1)} style={panelButton}>↑</button><button aria-label={`Move question ${index + 1} down`} disabled={index === all.length - 1} onClick={() => moveQuestion(index, 1)} style={panelButton}>↓</button></div>)}
      <div style={{ display: 'flex', justifyContent: 'flex-end', gap: 5 }}><button onClick={() => void (questionSetId ? updateQuestionSet() : saveQuestionSet())} style={panelButton}>Save set</button>{questionSetId && <button onClick={() => void deleteSelectedQuestionSet()} style={panelButton}>Delete</button>}<button onClick={() => setEditingQuestionSet(false)} style={panelButton}>Done</button></div>
    </div>}
    <div style={{ flex: 1, overflowY: 'auto', padding: 11, display: 'flex', flexDirection: 'column', gap: 9 }}>
      {messages.length === 0 && <div style={{ fontSize: 12, opacity: 0.65 }}>Ask about the workspace with a read-only Copilot context. Inline `/ask` questions and answers also appear here; this is separate from /btw.</div>}
      {messages.map((message) => <article key={message.id} style={{ alignSelf: message.role === 'user' ? 'flex-end' : 'stretch', maxWidth: message.role === 'user' ? '88%' : undefined, border: '1px solid rgba(127,127,127,0.28)', borderRadius: 6, padding: '7px 9px', fontSize: 12, background: message.role === 'user' ? 'rgba(127,127,127,0.12)' : 'transparent' }}><div style={{ fontSize: 10, opacity: 0.58, marginBottom: 4 }}>{message.role === 'assistant' ? 'Copilot · shared SDK chat' : message.role}{message.location?.filePath ? ` · ${message.location.filePath}:${message.location.startLine ?? '?'}` : ''}{message.pending ? ' · thinking…' : ''}</div><ReactMarkdown remarkPlugins={[remarkGfm]}>{message.body || (message.pending ? '_thinking…_' : '')}</ReactMarkdown></article>)}
    </div>
    <div style={{ padding: 10, borderTop: '1px solid rgba(127,127,127,0.3)' }}>
      <textarea value={prompt} onChange={(event) => setPrompt(event.target.value)} onKeyDown={(event) => { if ((event.metaKey || event.ctrlKey) && event.key === 'Enter') void send(); }} rows={3} placeholder={historicalConversation ? 'Resume this previous Ask session or start fresh to continue…' : 'Ask about this review…'} disabled={sending || historicalConversation} style={{ width: '100%', resize: 'vertical', fontSize: 12, padding: 6, background: 'transparent', border: '1px solid rgba(127,127,127,0.4)', borderRadius: 4, color: 'inherit' }} />
      <div style={{ display: 'flex', justifyContent: 'flex-end', gap: 6, marginTop: 6, flexWrap: 'wrap' }}>
        <button onClick={() => startNewQuestionSet(prompt)} disabled={!prompt.trim() || historicalConversation} style={panelButton}>Save as set</button>
        {questionSetId && <button onClick={() => setEditingQuestionSet((value) => !value)} style={panelButton}>{editingQuestionSet ? 'Hide set' : 'Edit set'}</button>}
        {questionSetId && <><button onClick={() => void sendQuestionSet('combined')} disabled={sending || historicalConversation || copilotUnavailable} style={panelButton}>Ask set</button><button onClick={() => void sendQuestionSet('sequential')} disabled={sending || historicalConversation || copilotUnavailable} style={panelButton}>Ask one-by-one</button></>}
        {sending && <button onClick={() => void cancel()} style={panelButton}>Cancel</button>}<button onClick={() => void send()} disabled={sending || historicalConversation || copilotUnavailable || !prompt.trim()} style={panelButton}>{sending ? 'Asking…' : 'Ask'}</button></div>
    </div>
  </section>;
}
