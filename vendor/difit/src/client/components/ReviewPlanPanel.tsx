export type ReviewPlanView = 'canonical' | 'plan';

export interface ReviewPlanHunk {
  id: string;
  path: string;
  header: string;
  oldStart?: number;
  newStart?: number;
  rationale?: string;
}

export interface ReviewPlanQuestion {
  id: string;
  body: string;
  hunkIds?: string[];
}

export interface ReviewPlanRecord {
  state: 'ready' | 'error';
  generatedAt?: number;
  model?: string;
  result?: { entries: Array<{ hunkId: string; rank: number; rationale: string }>; questions?: ReviewPlanQuestion[] };
  error?: string;
}

export interface ReviewPlanPanelProps {
  view: ReviewPlanView;
  onViewChange: (view: ReviewPlanView) => void;
  models: Array<{ id: string; name?: string }>;
  model: string;
  onModelChange: (model: string) => void;
  /** The canonical hunks are supplied from the loaded diff, never from Copilot. */
  hunks: ReviewPlanHunk[];
  record: ReviewPlanRecord | null;
  /** A prior-diff record is informative only and is never applied as an order. */
  stale: boolean;
  loading?: boolean;
  generating?: boolean;
  onGenerate: () => void;
  activeHunkId?: string | null;
  onOpenHunk: (hunkId: string) => void;
  onPreviousHunk: () => void;
  onNextHunk: () => void;
}

const buttonClass = 'rounded border border-github-border px-2 py-1 text-xs text-github-text-secondary hover:bg-github-bg-tertiary hover:text-github-text-primary disabled:cursor-not-allowed disabled:opacity-60';

/**
 * Optional saved Copilot guidance for navigating a diff. The component has no
 * network side effects: opening/reloading a review can only read a record.
 */
export function ReviewPlanPanel({
  view, onViewChange, models, model, onModelChange, hunks, record, stale, loading = false, generating = false,
  onGenerate, activeHunkId, onOpenHunk, onPreviousHunk, onNextHunk,
}: ReviewPlanPanelProps) {
  const hunkByID = new Map(hunks.map((hunk) => [hunk.id, hunk]));
  const entries = record?.state === 'ready'
    ? [...(record.result?.entries ?? [])].sort((left, right) => left.rank - right.rank)
    : [];
  const ordered = entries
    .map((entry) => ({ entry, hunk: hunkByID.get(entry.hunkId) }))
    .filter((item): item is { entry: typeof entries[number]; hunk: ReviewPlanHunk } => Boolean(item.hunk));
  const navigable = view === 'plan' && !stale && ordered.length ? ordered.map((item) => item.hunk.id) : hunks.map((hunk) => hunk.id);
  const currentIndex = activeHunkId ? navigable.indexOf(activeHunkId) : -1;
  const actionLabel = record || stale ? 'Recompute plan' : 'Generate plan';

  return (
    <section aria-label="Copilot review plan" className="rounded border border-github-border bg-github-bg-secondary p-3 text-sm">
      <div className="flex flex-wrap items-center gap-2">
        <strong className="text-github-text-primary">Review order</strong>
        <div role="group" aria-label="Review order representation" className="flex overflow-hidden rounded border border-github-border">
          <button type="button" aria-pressed={view === 'canonical'} onClick={() => onViewChange('canonical')} className={`px-2 py-1 text-xs ${view === 'canonical' ? 'bg-github-bg-tertiary text-github-text-primary' : 'text-github-text-secondary hover:bg-github-bg-tertiary'}`}>File order</button>
          <button type="button" aria-pressed={view === 'plan'} onClick={() => onViewChange('plan')} className={`border-l border-github-border px-2 py-1 text-xs ${view === 'plan' ? 'bg-github-bg-tertiary text-github-text-primary' : 'text-github-text-secondary hover:bg-github-bg-tertiary'}`}>Copilot plan</button>
        </div>
        <span className="text-xs text-github-text-muted">{hunks.length} hunk{hunks.length === 1 ? '' : 's'}</span>
        <label className="flex items-center gap-1 text-xs text-github-text-secondary">Model
          <select aria-label="Review plan model" value={model} onChange={(event) => onModelChange(event.target.value)} disabled={generating || !models.length} className="max-w-44 rounded border border-github-border bg-github-bg-primary px-1 py-0.5 text-xs text-github-text-primary disabled:opacity-60">
            {!model && <option value="">Choose in /ask</option>}
            {models.map((candidate) => <option key={candidate.id} value={candidate.id}>{candidate.name || candidate.id}</option>)}
          </select>
        </label>
        <div className="ml-auto flex items-center gap-1">
          <button type="button" className={buttonClass} disabled={!navigable.length} onClick={onPreviousHunk}>Previous hunk</button>
          <button type="button" className={buttonClass} disabled={!navigable.length} onClick={onNextHunk}>Next hunk</button>
          {currentIndex >= 0 && <span aria-live="polite" className="ml-1 text-xs text-github-text-muted">{currentIndex + 1} / {navigable.length}</span>}
        </div>
      </div>

      {loading ? <p className="mt-2 text-xs text-github-text-secondary">Loading saved review plan…</p> : stale ? (
        <div className="mt-2 rounded border border-yellow-800/60 bg-yellow-950/20 p-2 text-xs text-github-text-secondary">
          <p>This saved Copilot plan targets an earlier version of this diff. It is not being used to reorder the current review.</p>
          <button type="button" className={`${buttonClass} mt-2`} disabled={generating} onClick={onGenerate}>{generating ? 'Computing…' : actionLabel}</button>
        </div>
      ) : record?.state === 'error' ? (
        <div className="mt-2 rounded border border-red-900/60 bg-red-950/20 p-2 text-xs text-github-text-secondary">
          <p>{record.error || 'Copilot could not generate this review plan.'}</p>
          <button type="button" className={`${buttonClass} mt-2`} disabled={generating} onClick={onGenerate}>{generating ? 'Computing…' : actionLabel}</button>
        </div>
      ) : ordered.length ? (
        <div className="mt-2 space-y-2">
          {record?.model && <p className="text-xs text-github-text-muted">Saved with: {record.model}</p>}
          {view === 'plan' ? <ol className="space-y-1" aria-label="Copilot planned hunk order">
            {ordered.map(({ entry, hunk }) => <li key={hunk.id} className={`rounded border p-2 ${activeHunkId === hunk.id ? 'border-green-700 bg-green-950/20' : 'border-github-border'}`}>
              <button type="button" className="w-full text-left" onClick={() => onOpenHunk(hunk.id)}>
                <span className="font-medium text-github-text-primary">{entry.rank}. {hunk.path}</span>
                <span className="ml-2 font-mono text-xs text-github-text-muted">{hunk.header}</span>
                <span className="mt-1 block text-xs text-github-text-secondary">{entry.rationale}</span>
              </button>
            </li>)}
          </ol> : <p className="text-xs text-github-text-secondary">The diff is in canonical file order. Switch to Copilot plan to navigate its saved prioritization.</p>}
          {(record?.result?.questions?.length ?? 0) > 0 && <div className="border-t border-github-border pt-2"><strong className="text-xs text-github-text-primary">Suggested questions</strong><ul className="mt-1 list-disc pl-4 text-xs text-github-text-secondary">{record!.result!.questions!.map((question) => <li key={question.id}>{question.body}</li>)}</ul></div>}
          <button type="button" className={`${buttonClass} mt-1`} disabled={generating} onClick={onGenerate}>{generating ? 'Computing…' : actionLabel}</button>
        </div>
      ) : <div className="mt-2 text-xs text-github-text-secondary">
        <p>No saved Copilot review plan exists for this exact diff. File order remains the default.</p>
        <button type="button" className={`${buttonClass} mt-2`} disabled={generating || !hunks.length} onClick={onGenerate}>{generating ? 'Computing…' : actionLabel}</button>
        <p className="mt-1 text-github-text-muted">Generate is explicit: it sends the current diff using the workspace’s saved /ask model settings.</p>
      </div>}
    </section>
  );
}
