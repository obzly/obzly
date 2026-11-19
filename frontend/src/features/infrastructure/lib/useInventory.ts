import { useEffect, useState } from 'react';

import { IS_CONTROL_PLANE } from '../../../constants';
import { type SiteChange, type SiteRef, inventoryApi } from '../../../entity/inventory';
import { sitesApi } from '../../../entity/sites';
import { type KindHistory, mergeHistories } from './history';
import type { SiteSummary } from './paging';

export interface SiteResult extends SiteRef {
  /** Why the Site's inventory couldn't be read. */
  error?: string;
  /** Every asset of the Site, from its summary. */
  assets: number;
}

export interface InventoryData {
  /** The Sites that answered, with their counts. */
  summaries: SiteSummary[];
  changes: SiteChange[];
  sites: SiteResult[];
  /** Kind counts over time, from the Sites' inventory history. */
  history: KindHistory;
  loading: boolean;
  /** Set when no Site could be listed at all. */
  error?: string;
}

const CHANGE_HOURS = 24;

/**
 * The Overview's counts and change feed from every Site; asset lists are paged by the views that
 * show them. On a control plane, a Site that fails to answer is reported, not fatal.
 */
export const useInventory = (refresh: number): InventoryData => {
  const [data, setData] = useState<InventoryData>({
    summaries: [],
    changes: [],
    sites: [],
    history: { trend: {} },
    loading: true,
  });

  useEffect(() => {
    const controller = new AbortController();
    const load = async () => {
      let targets: SiteRef[] = [{ siteId: '', siteName: '' }];
      if (IS_CONTROL_PLANE) {
        try {
          const sites = await sitesApi.listSites();
          targets = sites
            .filter((site) => site.enabled)
            .map((site) => ({
              siteId: site.id,
              siteName: site.displayName || site.name || site.id,
            }));
        } catch (reason) {
          if (!controller.signal.aborted) {
            setData((current) => ({
              ...current,
              loading: false,
              error: (reason as Error).message,
            }));
          }
          return;
        }
      }
      const results = await Promise.all(
        targets.map(async (target) => {
          try {
            const [summary, changes] = await Promise.all([
              inventoryApi.summary(target.siteId, controller.signal, true),
              inventoryApi.changes(target.siteId, CHANGE_HOURS, controller.signal),
            ]);
            return {
              site: { ...target, assets: summary.total } as SiteResult,
              summary: { ...target, summary } as SiteSummary,
              changes: changes.map((change) => ({ ...change, ...target })),
            };
          } catch (reason) {
            return {
              site: { ...target, assets: 0, error: (reason as Error).message } as SiteResult,
              summary: undefined,
              changes: [] as SiteChange[],
            };
          }
        }),
      );
      if (controller.signal.aborted) return;
      const summaries = results.flatMap((result) => (result.summary ? [result.summary] : []));
      setData({
        summaries,
        changes: results
          .flatMap((result) => result.changes)
          .sort((a, b) => b.at.localeCompare(a.at)),
        sites: results.map((result) => result.site),
        // Sites that couldn't be read add no assets, so they don't need a history either.
        history: mergeHistories(summaries.map((site) => site.summary.history)),
        loading: false,
      });
    };
    void load();
    return () => controller.abort();
  }, [refresh]);

  return data;
};
