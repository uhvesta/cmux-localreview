import { fireEvent, render, screen, waitFor } from '@testing-library/react';
import { beforeEach, describe, expect, it, vi } from 'vitest';

import { BtwPanel } from './BtwPanel';

describe('BtwPanel native Copilot delivery', () => {
  beforeEach(() => {
    (global.fetch as ReturnType<typeof vi.fn>).mockImplementation((url: string) => {
      if (url === '/api/btw/threads') {
        return Promise.resolve({ ok: true, json: async () => ({ threads: [] }) });
      }
      if (url === '/api/btw/ask') {
        return Promise.resolve({ ok: true, json: async () => ({ target: 'copilot-sdk' }) });
      }
      return Promise.resolve({ ok: true, json: async () => ({}) });
    });
  });

  it('sends an SDK-native Copilot question rather than a retired ACP or terminal request', async () => {
    render(<BtwPanel selectedRepoId="repo-1" refreshNonce={0} collapsed={false} onToggleCollapsed={vi.fn()} />);
    const textbox = screen.getByRole('textbox');
    fireEvent.change(textbox, { target: { value: 'Why is this safe?' } });
    fireEvent.click(screen.getByRole('button', { name: 'Ask' }));

    await waitFor(() => {
      const call = (global.fetch as ReturnType<typeof vi.fn>).mock.calls.find(([url]) => url === '/api/btw/ask');
      expect(call).toBeDefined();
      expect(JSON.parse((call![1] as RequestInit).body as string)).toEqual({
        transport: 'copilot', repoId: 'repo-1', question: 'Why is this safe?',
      });
    });
    expect(screen.getByText('Copilot SDK · private side conversation')).toBeInTheDocument();
    expect(screen.queryByText('ACP agent')).not.toBeInTheDocument();
  });
});
