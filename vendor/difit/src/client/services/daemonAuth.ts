let pendingBootstrapCode: string | undefined;
// This exists only for the deliberate manual-recovery form. Normal browser
// launches never receive the daemon capability at all.
let pendingDaemonCapability: string | undefined;
let browserSessionExchange: Promise<void> | undefined;

/**
 * The global daemon is intentionally token-protected. `localreview-open`
 * hands a one-time, short-lived bootstrap code to the browser in the URL
 * fragment (which is never sent to the server). It stays in memory only long
 * enough to exchange for a scoped, HttpOnly browser-session cookie. It is
 * never written to localStorage, sessionStorage, IndexedDB, or a
 * JavaScript-readable cookie.
 */
export function captureDaemonTokenFromLocation(): void {
  if (typeof window === 'undefined') return;

  try {
    const hash = window.location.hash.startsWith('#') ? window.location.hash.slice(1) : '';
    const params = new URLSearchParams(hash);
    const bootstrapCode = params.get('bootstrapCode');
    // Keep old hand-written links usable only as a recovery path. Current
    // CLIs never put a daemon capability in a URL.
    const legacyCapability = params.get('daemonToken');
    if (!bootstrapCode && !legacyCapability) return;

    pendingBootstrapCode = bootstrapCode ?? undefined;
    pendingDaemonCapability = legacyCapability ?? undefined;
    params.delete('daemonToken');
    params.delete('bootstrapCode');
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
  if ((!pendingBootstrapCode && !pendingDaemonCapability) || typeof window === 'undefined') return;
  let bootstrapCode = pendingBootstrapCode;
  if (!bootstrapCode && pendingDaemonCapability) {
    const capability = pendingDaemonCapability;
    const grant = await fetch('/api/browser/grant', {
      method: 'POST',
      headers: { Authorization: `Bearer ${capability}` },
      credentials: 'same-origin',
    });
    if (!grant.ok) throw new Error('Could not create a local browser bootstrap code. Open Queue Home again with localreview-open.');
    bootstrapCode = (await grant.json() as { bootstrapCode?: string }).bootstrapCode;
    if (!bootstrapCode) throw new Error('The local daemon did not return a browser bootstrap code.');
  }
  const response = await fetch('/api/browser/session', {
    method: 'POST',
    headers: { Authorization: `Bearer ${bootstrapCode}` },
    credentials: 'same-origin',
  });
  if (!response.ok) throw new Error('Could not create a local browser session. Open Queue Home again with localreview-open.');
  // Clear it only after the exchange succeeded. A failed exchange can be
  // retried without putting the capability into persistent web storage.
  pendingBootstrapCode = undefined;
  pendingDaemonCapability = undefined;
}

async function ensureBrowserSession(): Promise<void> {
  // A normal CLI launch supplies a one-time bootstrap code. Manual recovery
  // supplies the owner capability, which is first converted to such a code.
  // Both must trigger the same exchange before protected Queue Home calls.
  if (!pendingBootstrapCode && !pendingDaemonCapability) return;
  if (!browserSessionExchange) browserSessionExchange = exchangeBrowserSession().finally(() => { browserSessionExchange = undefined; });
  await browserSessionExchange;
}

export function daemonFetch(input: RequestInfo | URL, init: RequestInit = {}): Promise<Response> {
  return ensureBrowserSession().then(() => fetch(input, { ...init, credentials: 'same-origin' }));
}
