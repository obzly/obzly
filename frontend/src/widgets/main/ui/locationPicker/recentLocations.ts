export interface RecentLocation {
  siteId: string;
  projectId: string;
  projectName: string;
}

const STORAGE_KEY = 'obzly_recent_locations';
const MAX_RECENT = 5;

export function getRecentLocations(): RecentLocation[] {
  try {
    const parsed: unknown = JSON.parse(localStorage.getItem(STORAGE_KEY) ?? '[]');
    return Array.isArray(parsed)
      ? parsed.filter(
          (entry): entry is RecentLocation =>
            typeof entry?.siteId === 'string' &&
            typeof entry?.projectId === 'string' &&
            typeof entry?.projectName === 'string',
        )
      : [];
  } catch {
    return [];
  }
}

/** Moves the location to the front; returns the updated list. */
export function pushRecentLocation(location: RecentLocation): RecentLocation[] {
  const next = [
    location,
    ...getRecentLocations().filter(
      (entry) => !(entry.siteId === location.siteId && entry.projectId === location.projectId),
    ),
  ].slice(0, MAX_RECENT);
  try {
    localStorage.setItem(STORAGE_KEY, JSON.stringify(next));
  } catch {
    /* a lost recent list is not worth surfacing */
  }
  return next;
}
