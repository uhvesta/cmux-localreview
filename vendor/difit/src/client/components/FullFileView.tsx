import { useVirtualizer } from '@tanstack/react-virtual';
import { useEffect, useMemo, useRef, useState } from 'react';

import type { CommentThread, DiffFile, DiffSide, LineNumber } from '../../types/diff';
import { resolveApiUrl } from '../apiBase';
import { CommentBodyRenderer } from './CommentBodyRenderer';

interface FullFileGate {
  afterLine: number;
  hiddenStart: number;
  hiddenEnd: number;
  lines: string[];
}

interface FullFileData {
  side: 'current' | 'base';
  path: string;
  oldPath?: string;
  status: DiffFile['status'];
  lines?: string[];
  gates?: FullFileGate[];
  deleted?: boolean;
  added?: boolean;
}

type Row =
  | { kind: 'line'; lineNumber: number; content: string }
  | { kind: 'gate'; gate: FullFileGate; index: number };

interface FullFileViewProps {
  file: DiffFile;
  threads: CommentThread[];
  onAddComment: (
    file: string,
    line: LineNumber,
    body: string,
    codeContent?: string,
    side?: DiffSide,
  ) => Promise<void>;
}

function threadKey(side: DiffSide, line: number): string {
  return `${side}:${line}`;
}

export function FullFileView({ file, threads, onAddComment }: FullFileViewProps) {
  const [side, setSide] = useState<'current' | 'base'>('current');
  const [data, setData] = useState<FullFileData | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [expandedGates, setExpandedGates] = useState<Set<number>>(new Set());
  const [allExpanded, setAllExpanded] = useState(false);
  const [commentingLine, setCommentingLine] = useState<number | null>(null);
  const [commentDraft, setCommentDraft] = useState('');
  const parentRef = useRef<HTMLDivElement | null>(null);

  useEffect(() => {
    let cancelled = false;
    setData(null);
    setError(null);
    setExpandedGates(new Set());
    setAllExpanded(false);
    fetch(resolveApiUrl(`/api/fullfile/${encodeURIComponent(file.path)}?side=${side}`))
      .then((res) => {
        if (!res.ok) throw new Error(`Failed to load full file (${res.status})`);
        return res.json() as Promise<FullFileData>;
      })
      .then((json) => {
        if (!cancelled) setData(json);
      })
      .catch((err: unknown) => {
        if (!cancelled) setError(err instanceof Error ? err.message : 'Failed to load full file');
      });
    return () => {
      cancelled = true;
    };
  }, [file.path, side]);

  const diffSide: DiffSide = side === 'current' ? 'new' : 'old';

  const threadsByLine = useMemo(() => {
    const map = new Map<string, CommentThread[]>();
    for (const thread of threads) {
      if ((thread.side ?? 'new') !== diffSide) continue;
      const line = typeof thread.line === 'number' ? thread.line : thread.line[0];
      const key = threadKey(diffSide, line);
      const existing = map.get(key);
      if (existing) existing.push(thread);
      else map.set(key, [thread]);
    }
    return map;
  }, [threads, diffSide]);

  const rows: Row[] = useMemo(() => {
    if (!data?.lines) return [];
    const gatesByAfterLine = new Map<number, { gate: FullFileGate; index: number }>();
    (data.gates ?? []).forEach((gate, index) => {
      gatesByAfterLine.set(gate.afterLine, { gate, index });
    });

    const result: Row[] = [];
    const leading = gatesByAfterLine.get(0);
    if (leading) result.push({ kind: 'gate', gate: leading.gate, index: leading.index });

    data.lines.forEach((content, i) => {
      const lineNumber = i + 1;
      result.push({ kind: 'line', lineNumber, content });
      const following = gatesByAfterLine.get(lineNumber);
      if (following) result.push({ kind: 'gate', gate: following.gate, index: following.index });
    });

    return result;
  }, [data]);

  const flatRows = useMemo(() => {
    // Expand each gate row into its own line rows when open, matching the
    // same Row union so the virtualizer sees a single flat list.
    const out: Array<Row | { kind: 'gate-line'; gateIndex: number; hiddenLine: number; content: string }> =
      [];
    for (const row of rows) {
      if (row.kind === 'gate' && (allExpanded || expandedGates.has(row.index))) {
        row.gate.lines.forEach((content, offset) => {
          out.push({
            kind: 'gate-line',
            gateIndex: row.index,
            hiddenLine: row.gate.hiddenStart + offset,
            content,
          });
        });
      } else {
        out.push(row);
      }
    }
    return out;
  }, [rows, expandedGates, allExpanded]);

  const virtualizer = useVirtualizer({
    count: flatRows.length,
    getScrollElement: () => parentRef.current,
    estimateSize: () => 20,
    overscan: 20,
  });

  const gateCount = data?.gates?.length ?? 0;
  const gateLabel = side === 'current' ? 'deleted' : 'added';

  const toggleGate = (index: number) => {
    setExpandedGates((prev) => {
      const next = new Set(prev);
      if (next.has(index)) next.delete(index);
      else next.add(index);
      return next;
    });
  };

  const submitComment = async (lineNumber: number) => {
    const body = commentDraft.trim();
    if (!body) return;
    const content = data?.lines?.[lineNumber - 1] ?? '';
    await onAddComment(file.path, lineNumber, body, content, diffSide);
    setCommentingLine(null);
    setCommentDraft('');
  };

  if (file.status === 'deleted' && side === 'current') {
    return (
      <div className="p-4 text-sm text-github-text-secondary">
        <div className="mb-2 rounded bg-red-500/10 border border-red-500/30 px-3 py-2 text-red-400">
          This file was deleted.
        </div>
        <button
          className="text-xs underline"
          onClick={() => setSide('base')}
        >
          View the deleted file's last content (Base)
        </button>
      </div>
    );
  }
  if (file.status === 'added' && side === 'base') {
    return (
      <div className="p-4 text-sm text-github-text-secondary">
        <div className="mb-2 rounded bg-green-500/10 border border-green-500/30 px-3 py-2 text-green-400">
          This file is new — it didn't exist on the base side.
        </div>
        <button className="text-xs underline" onClick={() => setSide('current')}>
          View the current content
        </button>
      </div>
    );
  }

  return (
    <div className="border border-github-border rounded-md overflow-hidden">
      <div className="flex items-center justify-between gap-3 px-3 py-2 bg-github-bg-secondary border-b border-github-border text-xs">
        <div className="flex items-center gap-2">
          <span className="font-mono">
            {file.oldPath && file.oldPath !== file.path ? `${file.oldPath} → ${file.path}` : file.path}
          </span>
          {gateCount > 0 && (
            <span className="text-github-text-muted">
              {gateCount} {gateLabel} region{gateCount === 1 ? '' : 's'}
            </span>
          )}
        </div>
        <div className="flex items-center gap-2">
          <button
            className={`px-2 py-1 rounded border ${side === 'current' ? 'bg-github-accent-emphasis text-white border-github-accent-emphasis' : 'border-github-border'}`}
            onClick={() => setSide('current')}
          >
            Current
          </button>
          <button
            className={`px-2 py-1 rounded border ${side === 'base' ? 'bg-github-accent-emphasis text-white border-github-accent-emphasis' : 'border-github-border'}`}
            onClick={() => setSide('base')}
          >
            Base
          </button>
          {gateCount > 0 && (
            <button
              className="px-2 py-1 rounded border border-github-border"
              onClick={() => {
                setAllExpanded((v) => !v);
                setExpandedGates(new Set());
              }}
            >
              {allExpanded ? `Collapse all ${gateLabel}` : `Expand all ${gateLabel}`}
            </button>
          )}
        </div>
      </div>

      {error && <div className="p-3 text-sm text-red-400">{error}</div>}
      {!data && !error && <div className="p-3 text-sm text-github-text-muted">Loading full file…</div>}

      {data?.lines && (
        <div ref={parentRef} style={{ height: 480, overflow: 'auto' }} className="font-mono text-xs">
          <div style={{ height: virtualizer.getTotalSize(), position: 'relative' }}>
            {virtualizer.getVirtualItems().map((virtualRow) => {
              const row = flatRows[virtualRow.index]!;
              return (
                <div
                  key={virtualRow.key}
                  data-index={virtualRow.index}
                  ref={virtualizer.measureElement}
                  style={{
                    position: 'absolute',
                    top: 0,
                    left: 0,
                    width: '100%',
                    transform: `translateY(${virtualRow.start}px)`,
                  }}
                >
                  {row.kind === 'gate' && (
                    <button
                      onClick={() => toggleGate(row.index)}
                      className={`w-full text-left px-3 py-1 ${side === 'current' ? 'bg-red-500/10 text-red-400' : 'bg-green-500/10 text-green-400'}`}
                    >
                      {expandedGates.has(row.index) || allExpanded ? '▾' : '▸'} {row.gate.lines.length}{' '}
                      {gateLabel} line{row.gate.lines.length === 1 ? '' : 's'} (old{' '}
                      {row.gate.hiddenStart}
                      {row.gate.hiddenEnd !== row.gate.hiddenStart ? `–${row.gate.hiddenEnd}` : ''})
                    </button>
                  )}
                  {row.kind === 'gate-line' && (
                    <div
                      className={`px-3 flex gap-3 ${side === 'current' ? 'bg-red-500/5' : 'bg-green-500/5'}`}
                    >
                      <span
                        className={`w-10 shrink-0 text-right select-none italic ${side === 'current' ? 'text-red-400/70' : 'text-green-400/70'}`}
                        title={`${side === 'current' ? 'old' : 'new'} line ${row.hiddenLine}`}
                      >
                        {row.hiddenLine}
                      </span>
                      <span className="whitespace-pre">{row.content}</span>
                    </div>
                  )}
                  {row.kind === 'line' && (
                    <div>
                      <div className="px-3 flex gap-3 hover:bg-github-bg-tertiary group">
                        <button
                          className="text-github-text-muted w-10 shrink-0 text-right select-none hover:text-github-text-primary"
                          onClick={() =>
                            setCommentingLine((cur) => (cur === row.lineNumber ? null : row.lineNumber))
                          }
                          title="Add a comment on this line"
                        >
                          {row.lineNumber}
                        </button>
                        <span className="whitespace-pre">{row.content}</span>
                        {threadsByLine.has(threadKey(diffSide, row.lineNumber)) && (
                          <span className="text-github-accent-emphasis" title="Has comments">
                            💬
                          </span>
                        )}
                      </div>
                      {threadsByLine.get(threadKey(diffSide, row.lineNumber))?.map((thread) => (
                        <div
                          key={thread.id}
                          className="ml-12 my-1 rounded border border-github-border bg-github-bg-secondary p-2"
                        >
                          {thread.messages.map((message) => (
                            <CommentBodyRenderer key={message.id} body={message.body} />
                          ))}
                        </div>
                      ))}
                      {commentingLine === row.lineNumber && (
                        <div className="ml-12 my-1 flex gap-2">
                          <textarea
                            autoFocus
                            value={commentDraft}
                            onChange={(e) => setCommentDraft(e.target.value)}
                            className="flex-1 text-xs bg-github-bg-secondary border border-github-border rounded p-1"
                            rows={2}
                          />
                          <button
                            className="text-xs px-2 py-1 rounded border border-github-border"
                            onClick={() => void submitComment(row.lineNumber)}
                          >
                            Submit
                          </button>
                        </div>
                      )}
                    </div>
                  )}
                </div>
              );
            })}
          </div>
        </div>
      )}
    </div>
  );
}
