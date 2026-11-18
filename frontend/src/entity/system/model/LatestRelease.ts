/** known: version is the newest release. unknown (the check failed) and disabled never mean up to date. */
export type ReleaseCheckStatus = 'known' | 'unknown' | 'disabled';

export interface LatestRelease {
  status: ReleaseCheckStatus;
  /** Without the leading v, e.g. "1.2.0". */
  version?: string;
  publishedAt?: string;
  url?: string;
  checkedAt?: string;
  /** Why the check failed (unknown only). */
  error?: string;
}
