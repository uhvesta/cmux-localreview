import { resolveApiUrl } from '../apiBase';

export function resolveEventSourceUrl(path: string): string {
  const basedPath = resolveApiUrl(path);
  const apiUrl = import.meta.env.VITE_DIFIT_API_URL?.trim();
  if (!apiUrl) {
    return basedPath;
  }

  try {
    return new URL(basedPath, apiUrl).toString();
  } catch {
    return basedPath;
  }
}
