// The selected Site, and per-Site scoping of other keys so state never leaks
// between Sites. Unused in single-site mode, where keys stay unscoped.

const SELECTED_SITE_KEY = 'obzly_selected_site_id';

export function getStoredSiteId(): string | null {
  try {
    return localStorage.getItem(SELECTED_SITE_KEY);
  } catch {
    return null;
  }
}

export function setStoredSiteId(siteId: string): void {
  try {
    localStorage.setItem(SELECTED_SITE_KEY, siteId);
  } catch {
    /* ignore - a lost preference is not worth surfacing to the user */
  }
}

export function scopedStorageKey(baseKey: string, siteId: string): string {
  return `${baseKey}:site:${siteId}`;
}
