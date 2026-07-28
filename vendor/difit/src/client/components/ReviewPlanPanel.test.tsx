import { fireEvent, render, screen } from '@testing-library/react';
import { describe, expect, it, vi } from 'vitest';

import { ReviewPlanPanel } from './ReviewPlanPanel';

const hunks = [
  { id: 'one', path: 'packages/a.ts', header: '@@ -1 +1 @@' },
  { id: 'two', path: 'packages/b.ts', header: '@@ -4 +5 @@' },
];

describe('ReviewPlanPanel', () => {
  it('does not imply a Copilot request until the reviewer explicitly generates', () => {
    const generate = vi.fn();
    render(<ReviewPlanPanel view="canonical" onViewChange={vi.fn()} models={[]} model="" onModelChange={vi.fn()} hunks={hunks} record={null} stale={false} onGenerate={generate} onOpenHunk={vi.fn()} onPreviousHunk={vi.fn()} onNextHunk={vi.fn()} onAskQuestion={vi.fn()} />);
    expect(screen.getByText('No saved Copilot review plan exists for this exact diff. File order remains the default.')).toBeInTheDocument();
    expect(generate).not.toHaveBeenCalled();
    fireEvent.click(screen.getByRole('button', { name: 'Generate plan' }));
    expect(generate).toHaveBeenCalledOnce();
  });

  it('never applies a stale plan to the current diff', () => {
    render(<ReviewPlanPanel view="plan" onViewChange={vi.fn()} models={[]} model="" onModelChange={vi.fn()} hunks={hunks} stale record={{ state: 'ready', result: { entries: [{ hunkId: 'two', rank: 1, rationale: 'Check this first.' }] } }} onGenerate={vi.fn()} onOpenHunk={vi.fn()} onPreviousHunk={vi.fn()} onNextHunk={vi.fn()} onAskQuestion={vi.fn()} />);
    expect(screen.getByText(/targets an earlier version/)).toBeInTheDocument();
    expect(screen.queryByText('Check this first.')).not.toBeInTheDocument();
  });

  it('renders saved plan entries and sends navigation to the selected hunk', () => {
    const open = vi.fn();
    const changeModel = vi.fn();
    const ask = vi.fn();
    render(<ReviewPlanPanel view="plan" onViewChange={vi.fn()} models={[{ id: 'gpt-5', name: 'GPT-5' }, { id: 'claude', name: 'Claude' }]} model="gpt-5" onModelChange={changeModel} hunks={hunks} stale={false} record={{ id: 'plan-1', state: 'ready', model: 'gpt-5', result: { entries: [{ hunkId: 'two', rank: 1, rationale: 'Check this first.' }], questions: [{ id: 'q-1', body: 'Is this safe?' }] } }} onGenerate={vi.fn()} onOpenHunk={open} onPreviousHunk={vi.fn()} onNextHunk={vi.fn()} onAskQuestion={ask} />);
    fireEvent.click(screen.getByRole('button', { name: /1\. packages\/b\.ts/ }));
    expect(open).toHaveBeenCalledWith('two');
    expect(screen.getByText('Saved with: gpt-5')).toBeInTheDocument();
    fireEvent.change(screen.getByLabelText('Review plan model'), { target: { value: 'claude' } });
    expect(changeModel).toHaveBeenCalledWith('claude');
    fireEvent.click(screen.getAllByRole('button', { name: 'Ask in /ask' })[0]!);
    expect(ask).toHaveBeenCalledWith('Is this safe?');
  });
});
