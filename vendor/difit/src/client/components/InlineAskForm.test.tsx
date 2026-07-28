// @vitest-environment happy-dom
import { render, screen, waitFor } from '@testing-library/react';
import { afterEach, describe, expect, it, vi } from 'vitest';

import { InlineAskForm } from './InlineAskForm';

describe('InlineAskForm Copilot recovery', () => {
  afterEach(() => vi.unstubAllGlobals());

  it('explains an unavailable Copilot runtime before a question can be sent', async () => {
    const fetchMock = vi.fn((input: RequestInfo | URL) => {
      const path = String(input);
      if (path === '/api/ask/models') return Promise.resolve({ ok: true, json: async () => ({ state: 'unavailable', warning: 'Connect Copilot in Queue Home, restart the daemon, then retry.' }) });
      if (path === '/api/ask/inline-conversations') return Promise.resolve({ ok: true, json: async () => ({ conversation: { id: 'inline-1' } }) });
      if (path === '/api/ask/conversations/inline-1') return Promise.resolve({ ok: true, json: async () => ({ messages: [] }) });
      return Promise.resolve({ ok: false, json: async () => ({ error: 'unexpected request' }) });
    });
    vi.stubGlobal('fetch', fetchMock);

    render(<InlineAskForm location={{ repoId: 'repo-1', filePath: 'main.ts', side: 'current', startLine: 7, endLine: 7, selectedCode: 'const answer = 42' }} onConvertToReviewComment={vi.fn()} onClose={vi.fn()} />);

    expect(await screen.findByText('Connect Copilot in Queue Home, restart the daemon, then retry.')).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Try Copilot again' })).toBeInTheDocument();
    expect(screen.getByRole('textbox', { name: 'Reply to Copilot' })).toBeDisabled();
    expect(screen.getByText('No question has been sent.')).toBeInTheDocument();
    await waitFor(() => expect(fetchMock).toHaveBeenCalledWith('/api/ask/models', expect.anything()));
    expect(fetchMock.mock.calls.filter(([path]) => String(path).includes('/messages'))).toHaveLength(0);
  });
});
