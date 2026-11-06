// Per-viewer, per-project field usage for the sidebar's "Frequently Used"
// shortcut; a local convenience, so localStorage rather than the backend.

const STORAGE_PREFIX = 'obzly-frequent-fields:';

// Bounded so one-off fields don't accumulate in a long-lived profile.
const MAX_TRACKED_FIELDS = 50;

type FieldUsageCounts = Record<string, number>;

function readCounts(projectId: string): FieldUsageCounts {
  if (typeof window === 'undefined') return {};
  try {
    const raw = localStorage.getItem(STORAGE_PREFIX + projectId);
    if (!raw) return {};
    const parsed: unknown = JSON.parse(raw);
    if (!parsed || typeof parsed !== 'object') return {};
    return parsed as FieldUsageCounts;
  } catch {
    return {};
  }
}

function writeCounts(projectId: string, counts: FieldUsageCounts): void {
  try {
    localStorage.setItem(STORAGE_PREFIX + projectId, JSON.stringify(counts));
  } catch {
    // Best-effort only - private browsing/quota errors just mean this
    // session's usage isn't remembered, nothing else depends on it.
  }
}

// weight lets adding a filter count for more than just expanding a field.
export function recordFieldUsage(projectId: string, fieldName: string, weight = 1): void {
  const counts = readCounts(projectId);
  counts[fieldName] = (counts[fieldName] ?? 0) + weight;

  // Keep only the most-used fields once past the cap.
  const entries = Object.entries(counts);
  if (entries.length > MAX_TRACKED_FIELDS) {
    entries.sort((a, b) => b[1] - a[1]);
    writeCounts(projectId, Object.fromEntries(entries.slice(0, MAX_TRACKED_FIELDS)));
  } else {
    writeCounts(projectId, counts);
  }
}

export function getFrequentFields(projectId: string, limit: number): string[] {
  const counts = readCounts(projectId);
  return Object.entries(counts)
    .sort((a, b) => b[1] - a[1])
    .slice(0, limit)
    .map(([name]) => name);
}
