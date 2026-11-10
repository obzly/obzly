import { IS_CONTROL_PLANE } from '../../../constants';
import { sitesApi } from '../../../entity/sites';
import type { CostSite, SiteOutcome } from './merge';

/** Every Site a control plane can read; a Site is just itself (''). */
export const listCostSites = async (): Promise<CostSite[]> => {
  if (!IS_CONTROL_PLANE) return [{ siteId: '', siteName: '' }];
  const sites = await sitesApi.listSites();
  return sites
    .filter((site) => site.enabled)
    .map((site) => ({ siteId: site.id, siteName: site.displayName || site.name || site.id }));
};

export const fanOut = <T>(
  sites: CostSite[],
  call: (siteId: string) => Promise<T>,
): Promise<SiteOutcome<T>[]> =>
  Promise.all(
    sites.map(async (site) => {
      try {
        return { site, value: await call(site.siteId) };
      } catch (reason) {
        return { site, error: (reason as Error).message };
      }
    }),
  );
