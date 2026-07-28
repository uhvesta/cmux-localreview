import { afterEach, describe, expect, it, vi } from 'vitest';

import { setApiBase } from '../apiBase.js';

import { resolveEventSourceUrl } from './eventSourceUrl.js';

afterEach(() => {
  setApiBase('');
  vi.unstubAllEnvs();
});

describe('resolveEventSourceUrl', () => {
  it('keeps durable Ask streams daemon-global after a repository is selected', () => {
    setApiBase('/api/repos/42');

    expect(resolveEventSourceUrl('/api/ask/conversations/c1/events'))
      .toBe('/api/ask/conversations/c1/events');
  });

  it('keeps repository watch streams scoped to the selected repository', () => {
    setApiBase('/api/repos/42');

    expect(resolveEventSourceUrl('/api/watch')).toBe('/api/repos/42/api/watch');
  });

  it('resolves a global Ask stream against the configured daemon origin', () => {
    setApiBase('/api/repos/42');
    vi.stubEnv('VITE_DIFIT_API_URL', 'http://127.0.0.1:57992');

    expect(resolveEventSourceUrl('/api/ask/conversations/c1/events'))
      .toBe('http://127.0.0.1:57992/api/ask/conversations/c1/events');
  });
});
