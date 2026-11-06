import { createContext } from 'react';

import type { Site } from '../../entity/sites';

export interface SiteContextValue {
  /** Always empty in single-site mode. */
  sites: Site[];
  selectedSite?: Site;
  selectSite: (site: Site) => void;
  isLoading: boolean;
  /** Re-fetches the Site list - e.g. after the admin registers/removes one. */
  reloadSites: () => Promise<void>;
}

export const SiteContext = createContext<SiteContextValue | null>(null);
