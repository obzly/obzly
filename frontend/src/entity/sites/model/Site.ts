/** '' means not set: the picker shows the Site with no environment colour. */
export type SiteEnvironment = '' | 'production' | 'staging' | 'development';

// What any signed-in user sees in the picker: deliberately no apiUrl.
export interface Site {
  id: string;
  name: string;
  enabled: boolean;
  siteRole?: string;
  /** Friendly name for the Site ID; '' means show the ID. */
  displayName: string;
  environment: SiteEnvironment;
}

// Central admins only.
export interface SiteAdmin {
  id: string;
  name: string;
  apiUrl: string;
  enabled: boolean;
  createdAt: string;
  updatedAt: string;
  displayName: string;
  environment: SiteEnvironment;
}

export interface CreateSiteRequest {
  id: string;
  name: string;
  apiUrl: string;
  displayName?: string;
  environment?: SiteEnvironment;
}

/** displayName/environment left out keep the stored values. */
export interface UpdateSiteRequest {
  name: string;
  apiUrl: string;
  enabled: boolean;
  displayName?: string;
  environment?: SiteEnvironment;
}

export interface SiteCheckResult {
  reachable: boolean;
  siteIdMatches: boolean;
  reportedSiteId?: string;
  version?: string;
  error?: string;
}

export type SiteHealth = 'up' | 'degraded' | 'down' | 'disabled';

export interface SiteStatus {
  siteId: string;
  status: SiteHealth;
  message?: string;
  version?: string;
  latencyMs?: number;
  checkedAt: string;
}
