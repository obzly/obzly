// Recent search-bar queries per viewer and project; a local convenience, so
// localStorage rather than the backend.

const STORAGE_PREFIX = 'obzly-recent-queries:';
const MAX_RECENT_QUERIES = 10;

function readQueries(projectId: string): string[] {
  if (typeof window === 'undefined') return [];
  try {
    const raw = localStorage.getItem(STORAGE_PREFIX + projectId);
    if (!raw) return [];
    const parsed: unknown = JSON.parse(raw);
    if (!Array.isArray(parsed)) return [];
    return parsed.filter((v): v is string => typeof v === 'string');
  } catch {
    return [];
  }
}

function writeQueries(projectId: string, queries: string[]): void {
  try {
    localStorage.setItem(STORAGE_PREFIX + projectId, JSON.stringify(queries));
  } catch {
    // Best-effort only - private browsing/quota errors just mean this
    // session's history isn't remembered, nothing else depends on it.
  }
}

/** Moves a repeated query to the front rather than storing it twice; caps at MAX_RECENT_QUERIES. */
export function recordRecentQuery(projectId: string, queryText: string): void {
  const trimmed = queryText.trim();
  if (!trimmed) return;

  const existing = readQueries(projectId).filter((q) => q !== trimmed);
  existing.unshift(trimmed);
  writeQueries(projectId, existing.slice(0, MAX_RECENT_QUERIES));
}

export function getRecentQueries(projectId: string): string[] {
  return readQueries(projectId);
}
