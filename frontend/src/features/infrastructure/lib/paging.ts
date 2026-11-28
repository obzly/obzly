// Server-side paging of inventory assets across Sites. Each Site sorts its own assets; to show one
// list across Sites the UI merges their pages with the same order the Site's SQL uses.
import type {
  AssetHealth,
  AssetKind,
  AssetList,
  AssetSort,
  InventorySummary,
  RegionCount,
  SiteAsset,
  SiteRef,
} from '../../../entity/inventory';
import { HEALTH_ORDER, KIND_FILTERS } from './assets';

export type SortOrder = 'ascend' | 'descend';

/** The most one request returns (the API's cap). */
export const MAX_PAGE = 5000;

export type HealthCounts = Record<AssetHealth, number>;

const KIND_RANK = Object.fromEntries(KIND_FILTERS.map((kind, index) => [kind.value, index]));

const LEAF_RANK: Partial<Record<AssetKind, number>> = {
  pod: 0,
  container: 1,
  node: 2,
  host: 3,
  docker_container: 4,
  workload: 5,
  service: 6,
};

/** Microseconds since the epoch, like Postgres keeps them (Date.parse stops at milliseconds). */
const micros = (iso: string) => {
  const fraction = /\.(\d+)/.exec(iso)?.[1] ?? '';
  return (
    Math.floor(Date.parse(iso) / 1000) * 1_000_000 + Number(fraction.slice(0, 6).padEnd(6, '0'))
  );
};

type Key = (asset: SiteAsset) => number | string;

// Each sort's keys before name and id, ascending; a missing metric is -1. Mirrors sortKeys in
// backend/internal/features/inventory/service.go.
const SORT_KEYS: Record<AssetSort, Key[]> = {
  health: [(a) => HEALTH_ORDER[a.health] ?? 4],
  kind: [(a) => KIND_RANK[a.kind] ?? 99],
  name: [],
  cpu: [(a) => a.metrics.cpuPercent ?? -1],
  memory: [(a) => a.metrics.memoryPercent ?? -1],
  restarts: [(a) => a.metrics.restarts24h ?? -1],
  // Ascending is the youngest and the most recently seen first.
  age: [(a) => -micros(a.firstSeen)],
  lastSeen: [(a) => -micros(a.lastSeen)],
  attention: [(a) => HEALTH_ORDER[a.health] ?? 4, (a) => LEAF_RANK[a.kind] ?? 9],
};

// Bytewise, like the Site's name COLLATE "C".
const compareValues = (a: number | string, b: number | string) => (a < b ? -1 : a > b ? 1 : 0);

/** The Site's order for sort: descend reverses every key, name and id included. */
export const compareAssets = (sort: AssetSort, order: SortOrder = 'ascend') => {
  const keys: Key[] = [...SORT_KEYS[sort], (a) => a.name, (a) => a.id];
  const sign = order === 'descend' ? -1 : 1;
  return (a: SiteAsset, b: SiteAsset) => {
    for (const key of keys) {
      const result = compareValues(key(a), key(b));
      if (result !== 0) return result * sign;
    }
    return 0;
  };
};

const labelled = (asset: AssetList['assets'][number], site: SiteRef): SiteAsset => ({
  ...asset,
  siteId: site.siteId,
  siteName: site.siteName,
});

/** Merges lists that are each sorted by compare into one sorted list. */
export const mergeSorted = <T>(lists: T[][], compare: (a: T, b: T) => number): T[] => {
  const heads = lists.map(() => 0);
  const merged: T[] = [];
  for (;;) {
    let best = -1;
    for (let i = 0; i < lists.length; i++) {
      if (heads[i] >= lists[i].length) continue;
      if (best < 0 || compare(lists[i][heads[i]], lists[best][heads[best]]) < 0) best = i;
    }
    if (best < 0) return merged;
    merged.push(lists[best][heads[best]]);
    heads[best] += 1;
  }
};

/** What has been read of one Site's sorted matches so far. */
export interface SiteCursor {
  assets: SiteAsset[];
  /** How many matched; undefined until the first page arrives. */
  total?: number;
  error?: string;
}

export type FetchPage = (site: SiteRef, offset: number, limit: number) => Promise<AssetList>;

export interface MergedPage {
  rows: SiteAsset[];
  /** Every Site's matches added up. */
  total: number;
  /** Sites that failed, with why. */
  errors: { site: SiteRef; error: string }[];
}

/**
 * One page of the Sites' matches in compare's order. A single Site pages on the server. Several
 * Sites each need their first offset + pageSize matches to place the page exactly, so cursors keeps
 * what was read (per Site ID) and the next page only asks for the rest.
 */
export const loadMergedPage = async (
  sites: SiteRef[],
  cursors: Map<string, SiteCursor>,
  fetchPage: FetchPage,
  compare: (a: SiteAsset, b: SiteAsset) => number,
  offset: number,
  pageSize: number,
): Promise<MergedPage> => {
  if (sites.length === 1) {
    const [site] = sites;
    try {
      const list = await fetchPage(site, offset, pageSize);
      return {
        rows: list.assets.map((asset) => labelled(asset, site)),
        total: list.total,
        errors: [],
      };
    } catch (reason) {
      return { rows: [], total: 0, errors: [{ site, error: (reason as Error).message }] };
    }
  }
  const need = offset + pageSize;
  await Promise.all(
    sites.map(async (site) => {
      let cursor = cursors.get(site.siteId);
      if (!cursor) {
        cursor = { assets: [] };
        cursors.set(site.siteId, cursor);
      }
      while (!cursor.error && cursor.assets.length < Math.min(need, cursor.total ?? Infinity)) {
        const from = cursor.assets.length;
        try {
          const list = await fetchPage(site, from, Math.min(MAX_PAGE, need - from));
          cursor.total = list.total;
          cursor.assets.push(...list.assets.map((asset) => labelled(asset, site)));
          // A Site that returns less than it counted (rebuilt meanwhile) has nothing more to give.
          if (list.assets.length === 0) cursor.total = cursor.assets.length;
        } catch (reason) {
          cursor.error = (reason as Error).message;
        }
      }
    }),
  );
  const states = sites.map((site) => ({ site, cursor: cursors.get(site.siteId)! }));
  return {
    rows: mergeSorted(
      states.map((state) => state.cursor.assets),
      compare,
    ).slice(offset, need),
    total: states.reduce(
      (sum, state) =>
        sum + (state.cursor.error ? state.cursor.assets.length : (state.cursor.total ?? 0)),
      0,
    ),
    errors: states
      .filter((state) => state.cursor.error)
      .map((state) => ({ site: state.site, error: state.cursor.error! })),
  };
};

/** Where the Overview's numbers come from: each Site's summary, labelled. */
export interface SiteSummary extends SiteRef {
  summary: InventorySummary;
}

export const emptyHealth = (): HealthCounts => ({
  critical: 0,
  warning: 0,
  healthy: 0,
  unknown: 0,
  inactive: 0,
});

const addHealth = (into: HealthCounts, from?: Partial<Record<AssetHealth, number>>) => {
  for (const [health, count] of Object.entries(from ?? {}) as [AssetHealth, number][]) {
    into[health] = (into[health] ?? 0) + count;
  }
  return into;
};

export const healthTotal = (row?: HealthCounts) =>
  row ? Object.values(row).reduce((sum, count) => sum + count, 0) : 0;

export interface RegionRow {
  /** "aws · us-east-1". */
  key: string;
  assets: number;
  warning: number;
  critical: number;
  /** Sites (several) or projects (one Site) with assets there. */
  where: string[];
}

export interface MergedSummary {
  total: number;
  /** Assets terminated (kept a while before they're dropped). */
  terminated: number;
  kinds: Map<AssetKind, HealthCounts>;
  /** Assets not terminated, by health. */
  live: HealthCounts;
  /** Live assets by Site (several Sites) or project, by health; empty groups left out. */
  groups: Map<string, HealthCounts>;
  regions: RegionRow[];
  clusters: string[];
}

/** Adds the Sites' summaries up for the Overview tiles and panels. */
export const mergeSummaries = (sites: SiteSummary[], multiSite: boolean): MergedSummary => {
  const kinds = new Map<AssetKind, HealthCounts>();
  const live = emptyHealth();
  const groups = new Map<string, HealthCounts>();
  const regions = new Map<string, RegionRow>();
  const clusters = new Set<string>();
  let total = 0;
  let terminated = 0;
  for (const { siteName, summary } of sites) {
    total += summary.total;
    terminated += summary.states?.terminated ?? 0;
    for (const [kind, healths] of Object.entries(summary.kinds) as [
      AssetKind,
      Partial<Record<AssetHealth, number>>,
    ][]) {
      kinds.set(kind, addHealth(kinds.get(kind) ?? emptyHealth(), healths));
    }
    addHealth(live, summary.live);
    const groupRows = multiSite
      ? [{ name: siteName, live: summary.live }]
      : summary.projects.map((project) => ({ name: project.name, live: project.live }));
    for (const row of groupRows) {
      const counts = addHealth(groups.get(row.name) ?? emptyHealth(), row.live);
      if (healthTotal(counts) > 0) groups.set(row.name, counts);
    }
    for (const region of summary.regions ?? ([] as RegionCount[])) {
      const key = `${region.provider || 'cloud'} · ${region.region}`;
      const row = regions.get(key) ?? { key, assets: 0, warning: 0, critical: 0, where: [] };
      row.assets += region.assets;
      row.warning += region.warning;
      row.critical += region.critical;
      for (const name of multiSite ? [siteName] : region.projects) {
        if (!row.where.includes(name)) row.where.push(name);
      }
      regions.set(key, row);
    }
    for (const cluster of summary.clusters ?? []) clusters.add(cluster);
  }
  return {
    total,
    terminated,
    kinds,
    live,
    groups,
    regions: [...regions.values()].sort(
      (a, b) => b.assets - a.assets || a.key.localeCompare(b.key),
    ),
    clusters: [...clusters].sort(),
  };
};
