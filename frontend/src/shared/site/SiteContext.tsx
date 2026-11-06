import type { JSX, ReactNode } from 'react';
import { useCallback, useEffect, useRef, useState } from 'react';

import { IS_CONTROL_PLANE } from '../../constants';
import { type Site, sitesApi } from '../../entity/sites';
import { publishSelectedSiteId } from './currentSiteRef';
import { SiteContext } from './siteContextValue';
import { getStoredSiteId, setStoredSiteId } from './siteStorage';

/** The selected Site; in single-site mode it never fetches and reports no Sites. */
export function SiteProvider({ children }: { children: ReactNode }): JSX.Element {
  const [sites, setSites] = useState<Site[]>([]);
  const [selectedSiteId, setSelectedSiteId] = useState<string | undefined>(undefined);
  const [isLoading, setIsLoading] = useState(IS_CONTROL_PLANE);

  // Lets reloadSites compute and publish the next selection synchronously.
  const selectedSiteIdRef = useRef<string | undefined>(undefined);

  // Publish currentSiteId synchronously, never from an effect: child effects
  // run first, so Site-scoped calls would otherwise hit the control plane.
  const applySelectedSiteId = (id: string | undefined) => {
    selectedSiteIdRef.current = id;
    if (id) {
      publishSelectedSiteId(id);
      setStoredSiteId(id);
    }
    setSelectedSiteId(id);
  };

  const reloadSites = useCallback(async () => {
    if (!IS_CONTROL_PLANE) {
      setIsLoading(false);
      return;
    }

    setIsLoading(true);
    try {
      const fetched = await sitesApi.listSites();
      setSites(fetched);

      const current = selectedSiteIdRef.current;
      if (current && fetched.some((site) => site.id === current)) {
        applySelectedSiteId(current);
      } else {
        const stored = getStoredSiteId();
        const preferred =
          (stored && fetched.find((site) => site.id === stored)) || fetched[0] || undefined;
        applySelectedSiteId(preferred?.id);
      }
    } finally {
      setIsLoading(false);
    }
  }, []);

  useEffect(() => {
    reloadSites();
  }, [reloadSites]);

  const selectedSite = sites.find((site) => site.id === selectedSiteId);

  const selectSite = (site: Site) => {
    applySelectedSiteId(site.id);
  };

  return (
    <SiteContext.Provider value={{ sites, selectedSite, selectSite, isLoading, reloadSites }}>
      {children}
    </SiteContext.Provider>
  );
}
