// @vitest-environment happy-dom
import { fireEvent, render, screen, waitFor } from '@testing-library/react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

import { BtwPanel } from './BtwPanel';

describe('BtwPanel native Copilot delivery', () => {
  let fetchMock: ReturnType<typeof vi.fn>;

  beforeEach(() => {
    // Keep this test runnable from both the vendored Vitest config and a
    // repository-root `bunx vitest run <file>` invocation. The latter does
    // not load vendor/vitest.setup.ts, so native fetch is not a vi.fn there.
    fetchMock = vi.fn((url: string) => {
      if (url === '/api/btw/threads') {
        return Promise.resolve({ ok: true, json: async () => ({ threads: [] }) });
      }
      if (url === '/api/btw/ask') {
        return Promise.resolve({ ok: true, json: async () => ({ target: 'copilot-sdk' }) });
      }
      return Promise.resolve({ ok: true, json: async () => ({}) });
    });
    vi.stubGlobal('fetch', fetchMock);
  });

  afterEach(() => vi.unstubAllGlobals());

  it('sends an SDK-native Copilot question rather than a retired ACP or terminal request', async () => {
    render(<BtwPanel selectedRepoId="repo-1" refreshNonce={0} collapsed={false} onToggleCollapsed={vi.fn()} />);
    const textbox = screen.getByRole('textbox');
    fireEvent.change(textbox, { target: { value: 'Why is this safe?' } });
    fireEvent.click(screen.getByRole('button', { name: 'Ask' }));

    await waitFor(() => {
      const call = fetchMock.mock.calls.find(([url]) => url === '/api/btw/ask');
      expect(call).toBeDefined();
      expect(JSON.parse((call![1] as RequestInit).body as string)).toEqual({
        transport: 'copilot', repoId: 'repo-1', question: 'Why is this safe?',
      });
    });
    // Do not rely on vendor/vitest.setup.ts here: this focused test is also
    // intentionally runnable through `bunx vitest run <path>` at repo root.
    expect(screen.getByText('Copilot SDK · private side conversation')).not.toBeNull();
    expect(screen.queryByText('ACP agent')).toBeNull();
  });
});
