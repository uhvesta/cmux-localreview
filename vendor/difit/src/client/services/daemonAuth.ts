const DAEMON_TOKEN_STORAGE_KEY = 'cmux-localreview.daemon-token';

/**
 * The global daemon is intentionally token-protected. `localreview-open`
 * hands the token to the browser in the URL fragment (which is never sent to
 * the server); this helper immediately moves it into sessionStorage so normal
 * same-origin API calls can authenticate for the lifetime of this tab.
 */
export function captureDaemonTokenFromLocation(): void {
  if (typeof window === 'undefined') return;

  try {
    const hash = window.location.hash.startsWith('#') ? window.location.hash.slice(1) : '';
    const params = new URLSearchParams(hash);
    const token = params.get('daemonToken');
    if (!token) return;

    window.sessionStorage.setItem(DAEMON_TOKEN_STORAGE_KEY, token);
    params.delete('daemonToken');
    const remainingHash = params.toString();
    window.history.replaceState(
      null,
      '',
      `${window.location.pathname}${window.location.search}${remainingHash ? `#${remainingHash}` : ''}`,
    );
  } catch {
    // Storage can be disabled in hardened browser profiles. The app remains
    // usable for unprotected standalone-server routes in that case.
  }
}

export function daemonAuthHeaders(headers?: HeadersInit): Headers {
  const merged = new Headers(headers);
  if (typeof window === 'undefined') return merged;

  try {
    const token = window.sessionStorage.getItem(DAEMON_TOKEN_STORAGE_KEY);
    if (token) merged.set('Authorization', `Bearer ${token}`);
  } catch {
    // See captureDaemonTokenFromLocation.
  }
  return merged;
}

export function daemonFetch(input: RequestInfo | URL, init: RequestInit = {}): Promise<Response> {
  return fetch(input, { ...init, headers: daemonAuthHeaders(init.headers) });
}
