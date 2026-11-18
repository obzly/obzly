import { useEffect, useState } from 'react';

import { IS_CONTROL_PLANE } from '../../../constants';
import {
  type SiteAsset,
  type SiteChange,
  type SiteRef,
  inventoryApi,
} from '../../../entity/inventory';
import { sitesApi } from '../../../entity/sites';
import { type KindHistory, mergeHistories } from './history';

export interface SiteResult extends SiteRef {
  /** Why the Site's inventory couldn't be read. */
  error?: string;
  assets: number;
  /** More matched than were returned (the API caps at 5000). */
  truncated: boolean;
}

export interface InventoryData {
  assets: SiteAsset[];
  changes: SiteChange[];
  sites: SiteResult[];
  /** Kind counts over time, from the Sites' inventory history. */
  history: KindHistory;
  loading: boolean;
  /** Set when no Site could be listed at all. */
  error?: string;
}

const CHANGE_HOURS = 24;

/** On a control plane, a Site that fails to answer is reported, not fatal. */
export const useInventory = (refresh: number): InventoryData => {
  const [data, setData] = useState<InventoryData>({
    assets: [],
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
            const [list, changes, summary] = await Promise.all([
              inventoryApi.assets(target.siteId, controller.signal),
              inventoryApi.changes(target.siteId, CHANGE_HOURS, controller.signal),
              // Only for pills and sparklines: a Site without it still shows its assets.
              inventoryApi.summary(target.siteId, controller.signal, true).catch(() => undefined),
            ]);
            return {
              site: {
                ...target,
                assets: list.total,
                truncated: list.total > list.assets.length,
              } as SiteResult,
              assets: list.assets.map((asset) => ({ ...asset, ...target })),
              changes: changes.map((change) => ({ ...change, ...target })),
              history: summary?.history,
              failed: false,
            };
          } catch (reason) {
            return {
              site: { ...target, assets: 0, truncated: false, error: (reason as Error).message },
              assets: [] as SiteAsset[],
              changes: [] as SiteChange[],
              history: undefined,
              failed: true,
            };
          }
        }),
      );
      if (controller.signal.aborted) return;
      setData({
        assets: results.flatMap((result) => result.assets),
        changes: results
          .flatMap((result) => result.changes)
          .sort((a, b) => b.at.localeCompare(a.at)),
        sites: results.map((result) => result.site),
        // Sites that couldn't be read add no assets, so they don't need a history either.
        history: mergeHistories(
          results.filter((result) => !result.failed).map((result) => result.history),
        ),
        loading: false,
      });
    };
    void load();
    return () => controller.abort();
  }, [refresh]);

  return data;
};
