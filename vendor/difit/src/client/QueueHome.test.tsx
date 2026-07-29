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

    const alert = await screen.findByText('The local review queue is unavailable.');
    expect(alert.parentElement?.textContent).toContain('The local review queue is unavailable.');
    expect(screen.getByRole('button', { name: 'Retry Queue Home' })).not.toBeNull();
    expect(screen.getByText('localreview daemon status')).not.toBeNull();
    fireEvent.click(screen.getByRole('button', { name: 'Retry Queue Home' }));
    await waitFor(() => expect(fetchMock.mock.calls.length).toBeGreaterThan(4));
  });

  it('keeps optional GitHub and federation failures actionable instead of silently hiding them', async () => {
    const fetchMock = vi.fn((input: RequestInfo | URL) => {
      const path = String(input);
      if (path.includes('/api/github/auth/status')) return Promise.resolve({ ok: false, status: 503, json: async () => ({ error: 'secure store is locked' }) });
      if (path.includes('/api/queue')) return Promise.resolve({ ok: true, status: 200, json: async () => ({ items: [] }) });
      if (path.includes('/api/workspaces')) return Promise.resolve({ ok: true, status: 200, json: async () => ({ activeWorkspace: null }) });
      if (path.includes('/api/federation/queue')) return Promise.resolve({ ok: false, status: 503, json: async () => ({ error: 'remote tunnel unavailable' }) });
      if (path.includes('/api/federation/nodes')) return Promise.resolve({ ok: false, status: 503, json: async () => ({ error: 'remote node registry unavailable' }) });
      return Promise.resolve({ ok: true, status: 200, json: async () => ({}) });
    });
    vi.stubGlobal('fetch', fetchMock);
    render(<QueueHome />);

    expect(await screen.findByText(/secure store is locked/)).not.toBeNull();
    expect(screen.getByRole('button', { name: 'Retry GitHub status' })).not.toBeNull();
    expect(screen.getByText(/remote tunnel unavailable/)).not.toBeNull();
    fireEvent.click(screen.getByRole('button', { name: 'Retry remote nodes' }));
    await waitFor(() => expect(fetchMock.mock.calls.length).toBeGreaterThan(5));
  });

  it('preserves the daemon-provided immutable snapshot recovery when opening fails', async () => {
    vi.stubGlobal('fetch', vi.fn((input: RequestInfo | URL) => {
      const path = String(input);
      if (path.includes('/api/github/auth/status')) return Promise.resolve({ ok: true, status: 200, json: async () => ({ provider: 'github-oauth-pkce', capabilities: {
        read: { configured: false, authenticated: false, loginState: 'idle' },
        write: { configured: false, authenticated: false, loginState: 'idle' },
        copilot: { configured: false, authenticated: false, loginState: 'idle' },
      } }) });
      if (path.includes('/api/queue/snapshot-1/open')) return Promise.resolve({ ok: false, status: 400, json: async () => ({
        error: 'The retained snapshot is unavailable.',
        recovery: 'Restore the retained snapshot or requeue it from its source workspace, then try opening it again.',
      }) });
      if (path.includes('/api/queue')) return Promise.resolve({ ok: true, status: 200, json: async () => ({ items: [{
        id: 'snapshot-1', title: 'Retained snapshot', body: '', workspacePath: '/work/repo', kind: 'local', status: 'queued', position: 1,
        agentProvider: null, acpState: 'unavailable', acpLastError: null,
      }] }) });
      if (path.includes('/api/workspaces')) return Promise.resolve({ ok: true, status: 200, json: async () => ({ activeWorkspace: null }) });
      if (path.includes('/api/federation/')) return Promise.resolve({ ok: true, status: 200, json: async () => ({ nodes: [] }) });
      return Promise.resolve({ ok: true, status: 200, json: async () => ({}) });
    }));
    render(<QueueHome />);

    fireEvent.click(await screen.findByRole('button', { name: 'Open workspace' }));
    const alert = await screen.findByRole('alert');
    expect(alert.textContent).toContain('The retained snapshot is unavailable.');
    expect(alert.textContent).toContain('Restore the retained snapshot or requeue it from its source workspace');
    expect(alert.textContent).toContain('The review is still queued');
  });

  it('guides a configured OAuth client through desktop device flow without asking for a secret', async () => {
    const auth = {
      provider: 'github-oauth-pkce',
      capabilities: {
        read: { configured: true, clientId: 'Iv1.readclient', browserOAuthReady: true, authenticated: false, loginState: 'idle' },
        write: { configured: false, authenticated: false, loginState: 'idle' },
        copilot: { configured: false, authenticated: false, loginState: 'idle' },
      },
    };
    vi.stubGlobal('fetch', vi.fn((input: RequestInfo | URL) => {
      const path = String(input);
      if (path.includes('/api/github/auth/status')) return Promise.resolve({ ok: true, status: 200, json: async () => auth });
      if (path.includes('/api/queue')) return Promise.resolve({ ok: true, status: 200, json: async () => ({ items: [] }) });
      if (path.includes('/api/workspaces')) return Promise.resolve({ ok: true, status: 200, json: async () => ({ activeWorkspace: null }) });
      if (path.includes('/api/federation/')) return Promise.resolve({ ok: true, status: 200, json: async () => ({ nodes: [] }) });
      return Promise.resolve({ ok: true, status: 200, json: async () => ({}) });
    }));
    render(<QueueHome />);

    expect(await screen.findByRole('region', { name: 'GitHub OAuth connections' })).not.toBeNull();
    expect(screen.getByText('Iv1.readclient')).not.toBeNull();
    fireEvent.click(screen.getByText('Set up desktop GitHub access safely'));
    expect(screen.getByText(/desktop sign-in does not use it/)).not.toBeNull();
    expect(screen.getByText(/Device Flow/)).not.toBeNull();
    // Desktop device flow is explained, but Queue Home has no secret input or
    // paste flow and never borrows the user's general gh credential.
    expect(screen.queryByRole('textbox', { name: /secret/i })).toBeNull();
    expect(screen.getByText(/gh login/i)).not.toBeNull();
  });

  it('shows a remote tunnel cache state rather than hiding lazy federation reads', async () => {
    vi.stubGlobal('fetch', vi.fn((input: RequestInfo | URL) => {
      const path = String(input);
      if (path.includes('/api/github/auth/status')) return Promise.resolve({ ok: true, status: 200, json: async () => ({ provider: 'github-oauth-pkce', capabilities: {
        read: { configured: false, authenticated: false, loginState: 'idle' },
        write: { configured: false, authenticated: false, loginState: 'idle' },
        copilot: { configured: false, authenticated: false, loginState: 'idle' },
      } }) });
      if (path.includes('/api/queue')) return Promise.resolve({ ok: true, status: 200, json: async () => ({ items: [] }) });
      if (path.includes('/api/workspaces')) return Promise.resolve({ ok: true, status: 200, json: async () => ({ activeWorkspace: null }) });
      if (path.endsWith('/api/federation/nodes')) return Promise.resolve({ ok: true, status: 200, json: async () => ({ nodes: [{ id: 'lab', label: 'Lab', sshTarget: 'reviewer@example.test', remotePort: 57140, enabled: true }] }) });
      if (path.endsWith('/api/federation/queue')) return Promise.resolve({ ok: true, status: 200, json: async () => ({ nodes: [{ node: { id: 'lab', label: 'Lab', sshTarget: 'reviewer@example.test', remotePort: 57140, enabled: true }, items: [], runtime: { state: 'connected', localPort: 49222, cachedResponses: 1, available: true, message: 'SSH loopback federation available' } }] }) });
      if (path.includes('/api/federation/nodes/lab/status')) return Promise.resolve({ ok: true, status: 200, json: async () => ({ runtime: { state: 'connected', localPort: 49222, cachedResponses: 1, available: true, message: 'SSH loopback federation available' } }) });
      return Promise.resolve({ ok: true, status: 200, json: async () => ({}) });
    }));
    render(<QueueHome />);
    expect(await screen.findByText('connected · localhost:49222 · 1 cached response')).not.toBeNull();
  });
});
