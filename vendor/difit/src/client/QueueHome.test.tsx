// @vitest-environment happy-dom
import { fireEvent, render, screen, waitFor } from '@testing-library/react';
import { afterEach, describe, expect, it, vi } from 'vitest';

import { QueueCard, QueueHome } from './QueueHome';

describe('Queue Home lifecycle recovery', () => {
  afterEach(() => vi.unstubAllGlobals());

  it('does not offer an invalid open action for completed history and exposes explicit requeue', () => {
    const onOpenWorkspace = vi.fn();
    const onRequeue = vi.fn();
    render(<QueueCard item={{
      id: 'completed-1', title: 'Completed review', body: '', workspacePath: '/work/repo', kind: 'local', status: 'completed', position: 1,
      agentProvider: null, acpState: 'unavailable', acpLastError: null,
    }} onOpenWorkspace={onOpenWorkspace} onRequeue={onRequeue} />);

    expect(screen.queryByRole('button', { name: 'Open workspace' })).toBeNull();
    fireEvent.click(screen.getByRole('button', { name: 'Requeue for another pass' }));
    expect(onRequeue).toHaveBeenCalledOnce();
    expect(onOpenWorkspace).not.toHaveBeenCalled();
  });

  it('makes a failed Queue Home load recoverable in place', async () => {
    const fetchMock = vi.fn(() => Promise.resolve({ ok: false, status: 503, json: async () => ({ error: 'offline' }) }));
    vi.stubGlobal('fetch', fetchMock);
    render(<QueueHome />);

    const alert = await screen.findByRole('alert');
    expect(alert.textContent).toContain('The local review queue is unavailable.');
    expect(screen.getByRole('button', { name: 'Retry Queue Home' })).not.toBeNull();
    expect(screen.getByText('localreview daemon status')).not.toBeNull();
    fireEvent.click(screen.getByRole('button', { name: 'Retry Queue Home' }));
    await waitFor(() => expect(fetchMock.mock.calls.length).toBeGreaterThan(4));
  });
});
