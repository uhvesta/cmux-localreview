// @vitest-environment happy-dom
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

import { AskPanel } from './AskPanel';

describe('AskPanel workspace defaults', () => {
  let fetchMock: ReturnType<typeof vi.fn>;
  let defaults: { model: string; reasoningEffort: string; contextTier: string };

  beforeEach(() => {
    defaults = { model: 'gpt-5', reasoningEffort: 'medium', contextTier: 'default' };
    fetchMock = vi.fn((url: string, init?: RequestInit) => {
      if (url === '/api/ask/models') return Promise.resolve({ ok: true, json: async () => ({
        state: 'ready', models: [{ id: 'gpt-5', name: 'GPT-5' }, { id: 'claude-sonnet-4.6', name: 'Claude Sonnet' }],
        workspaceDefaults: defaults,
      }) });
      if (url === '/api/ask/conversations?includeArchived=true') return Promise.resolve({ ok: true, json: async () => ({ conversations: [], activeReviewSessionId: 7 }) });
      if (url === '/api/ask/question-sets') return Promise.resolve({ ok: true, json: async () => ({ questionSets: [] }) });
      if (url === '/api/ask/settings' && init?.method === 'PUT') {
        defaults = JSON.parse(String(init.body));
        return Promise.resolve({ ok: true, json: async () => ({ workspaceDefaults: defaults }) });
      }
      return Promise.resolve({ ok: true, json: async () => ({}) });
    });
    vi.stubGlobal('fetch', fetchMock);
  });

  afterEach(() => { cleanup(); vi.unstubAllGlobals(); });

  it('loads and saves a workspace default before any conversation exists', async () => {
    render(<AskPanel collapsed={false} onToggleCollapsed={vi.fn()} />);
    const model = await screen.findByLabelText('Copilot model') as HTMLSelectElement;
    expect(model.value).toBe('gpt-5');
    expect(screen.getByText('fresh SDK chat · workspace default saved')).not.toBeNull();

    fireEvent.change(model, { target: { value: 'claude-sonnet-4.6' } });
    await waitFor(() => {
      const request = fetchMock.mock.calls.find(([url, init]) => url === '/api/ask/settings' && (init as RequestInit | undefined)?.method === 'PUT');
      expect(request).toBeDefined();
      expect(JSON.parse(String((request![1] as RequestInit).body))).toMatchObject({ model: 'claude-sonnet-4.6', contextTier: 'default' });
    });
    // Updating the default can cause one deliberate transcript refresh, but
    // unchanged server defaults must not spin in a continuous fetch loop.
    await new Promise((resolve) => setTimeout(resolve, 20));
    expect(fetchMock.mock.calls.filter(([url]) => url === '/api/ask/models').length).toBeLessThanOrEqual(3);
  });
});
