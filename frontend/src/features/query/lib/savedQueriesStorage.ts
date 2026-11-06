// Per-viewer saved searches in localStorage: one person's shortcut, not a
// team object.
import type { SearchMode } from '../ui/SearchBarComponent';

export interface SavedSearch {
  name: string;
  mode: SearchMode;
  text: string;
}

const STORAGE_PREFIX = 'obzly-saved-searches:';
const MAX_SAVED = 50;

function isSavedSearch(v: unknown): v is SavedSearch {
  if (typeof v !== 'object' || v === null) return false;
  const s = v as Record<string, unknown>;
  return (
    typeof s.name === 'string' &&
    typeof s.text === 'string' &&
    (s.mode === 'simple' || s.mode === 'sql')
  );
}

export function getSavedSearches(projectId: string): SavedSearch[] {
  try {
    const raw = localStorage.getItem(STORAGE_PREFIX + projectId);
    const parsed: unknown = raw ? JSON.parse(raw) : [];
    return Array.isArray(parsed) ? parsed.filter(isSavedSearch) : [];
  } catch {
    return [];
  }
}

function write(projectId: string, searches: SavedSearch[]): void {
  try {
    localStorage.setItem(STORAGE_PREFIX + projectId, JSON.stringify(searches.slice(0, MAX_SAVED)));
  } catch {
    // Best-effort, like recent queries.
  }
}

/** Saves (or overwrites, by name) a search; newest first. */
export function saveSearch(projectId: string, search: SavedSearch): void {
  const name = search.name.trim();
  if (!name || !search.text.trim()) return;
  const rest = getSavedSearches(projectId).filter((s) => s.name !== name);
  write(projectId, [{ ...search, name }, ...rest]);
}

export function deleteSavedSearch(projectId: string, name: string): void {
  write(
    projectId,
    getSavedSearches(projectId).filter((s) => s.name !== name),
  );
}
