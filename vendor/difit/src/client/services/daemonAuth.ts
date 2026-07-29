let pendingDaemonCapability: string | undefined;
let browserSessionExchange: Promise<void> | undefined;

/**
 * The global daemon is intentionally token-protected. `localreview-open`
 * hands the token to the browser in the URL fragment (which is never sent to
 * the server). It stays in memory only long enough to exchange for a scoped,
 * HttpOnly browser-session cookie. It is never written to localStorage,
 * sessionStorage, IndexedDB, or a JavaScript-readable cookie.
 */
export function captureDaemonTokenFromLocation(): void {
  if (typeof window === 'undefined') return;

  try {
    const hash = window.location.hash.startsWith('#') ? window.location.hash.slice(1) : '';
    const params = new URLSearchParams(hash);
    const token = params.get('daemonToken');
    if (!token) return;

    pendingDaemonCapability = token;
    params.delete('daemonToken');
    const remainingHash = params.toString();
    window.history.replaceState(
      null,
      '',
      `${window.location.pathname}${window.location.search}${remainingHash ? `#${remainingHash}` : ''}`,
    );
  } catch {
    // A malformed fragment should not stop the read-only reviewer from
    // rendering. Protected daemon controls will show their recovery state.
  }
}

/** Accept a local recovery capability only in memory, pending cookie exchange. */
export function saveDaemonToken(token: string): boolean {
  const normalized = token.trim();
  if (!normalized || typeof window === 'undefined') return false;
  pendingDaemonCapability = normalized;
  browserSessionExchange = undefined;
  return true;
}

async function exchangeBrowserSession(): Promise<void> {
  if (!pendingDaemonCapability || typeof window === 'undefined') return;
  const capability = pendingDaemonCapability;
  const response = await fetch('/api/browser/session', {
    method: 'POST',
    headers: { Authorization: `Bearer ${capability}` },
    credentials: 'same-origin',
  });
  if (!response.ok) throw new Error('Could not create the desktop app’s private session. Quit and reopen CMUX Local Review, then try again.');
  // Clear it only after the exchange succeeded. A failed exchange can be
  // retried without putting the capability into persistent web storage.
  if (pendingDaemonCapability === capability) pendingDaemonCapability = undefined;
}

async function ensureBrowserSession(): Promise<void> {
  if (!pendingDaemonCapability) return;
  if (!browserSessionExchange) browserSessionExchange = exchangeBrowserSession().finally(() => { browserSessionExchange = undefined; });
  await browserSessionExchange;
}

export function daemonFetch(input: RequestInfo | URL, init: RequestInit = {}): Promise<Response> {
  return ensureBrowserSession().then(() => fetch(input, { ...init, credentials: 'same-origin' }));
}
