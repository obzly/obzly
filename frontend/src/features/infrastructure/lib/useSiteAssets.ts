import { useEffect, useState } from 'react';

import {
  type AssetQuery,
  type SiteAsset,
  type SiteRef,
  inventoryApi,
} from '../../../entity/inventory';
import { compareAssets, mergeSorted } from './paging';

export interface SiteAssets {
  /** The first query.limit matches across the Sites, in the query's order. */
  assets: SiteAsset[];
  /** Every Site's matches added up. */
  total: number;
  loading: boolean;
}

/** A short list (query.limit) from every Site, merged: for the Overview's panels. */
export const useSiteAssets = (sites: SiteRef[], query: AssetQuery, refresh: number): SiteAssets => {
  const [data, setData] = useState<SiteAssets>({ assets: [], total: 0, loading: true });
  const key = JSON.stringify([sites.map((site) => site.siteId), query, refresh]);

  useEffect(() => {
    const controller = new AbortController();
    setData((current) => ({ ...current, loading: true }));
    void Promise.all(
      sites.map((site) =>
        inventoryApi
          .assets(site.siteId, query, controller.signal)
          .then((list) => ({
            assets: list.assets.map((asset) => ({
              ...asset,
              siteId: site.siteId,
              siteName: site.siteName,
            })),
            total: list.total,
          }))
          // The Site's own error shows in the header; its panel rows are just missing.
          .catch(() => ({ assets: [] as SiteAsset[], total: 0 })),
      ),
    ).then((lists) => {
      if (controller.signal.aborted) return;
      const compare = compareAssets(
        query.sort ?? 'health',
        query.order === 'desc' ? 'descend' : 'ascend',
      );
      setData({
        assets: mergeSorted(
          lists.map((list) => list.assets),
          compare,
        ).slice(0, query.limit),
        total: lists.reduce((sum, list) => sum + list.total, 0),
        loading: false,
      });
    });
    return () => controller.abort();
    // key covers sites, query and refresh by value.
  }, [key]);

  return data;
};
