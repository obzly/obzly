import { describe, expect, it } from 'vitest';

import type { AssetList, InventorySummary, SiteAsset, SiteRef } from '../../../entity/inventory';
import { assetSearchParams } from '../../../entity/inventory/api/assetSearchParams';
import {
  type SiteCursor,
  compareAssets,
  loadMergedPage,
  mergeSorted,
  mergeSummaries,
} from './paging';

let ids = 0;
const asset = (overrides: Partial<SiteAsset>): SiteAsset => ({
  id: `00000000-0000-0000-0000-${String(++ids).padStart(12, '0')}`,
  projectId: 'p1',
  projectName: 'shop',
  kind: 'pod',
  key: overrides.name ?? 'k',
  name: 'n',
  subtype: '',
  parentKey: '',
  nodeKey: '',
  cluster: 'prd',
  namespace: 'shop',
  node: '',
  attributes: {},
  sources: [],
  firstSeen: '2026-10-25T10:00:00Z',
  lastSeen: '2026-10-25T12:00:00Z',
  state: 'active',
  health: 'healthy',
  reasons: [],
  metrics: {},
  updatedAt: '2026-10-25T12:00:00Z',
  siteId: 's1',
  siteName: 'Site one',
  ...overrides,
});

const names = (assets: SiteAsset[]) => assets.map((a) => a.name);

describe('compareAssets', () => {
  // The same assets and orders as the Site's paging tests (assets_paging_test.go).
  const fixture = [
    asset({ name: 'prd', kind: 'cluster', health: 'healthy', firstSeen: '2026-10-22T12:00:00Z' }),
    asset({ name: 'shop', kind: 'namespace', health: 'warning' }),
    asset({ name: 'checkout-api', kind: 'workload', health: 'critical' }),
    asset({
      name: 'checkout-api-1',
      health: 'critical',
      metrics: { cpuPercent: 90 },
      firstSeen: '2026-10-25T11:00:00.000001Z',
    }),
    asset({
      name: 'checkout-api-2',
      health: 'healthy',
      metrics: { cpuPercent: 10 },
      firstSeen: '2026-10-25T11:00:00Z',
    }),
    asset({ name: 'app', kind: 'container', health: 'warning', metrics: { cpuPercent: 50 } }),
    asset({ name: 'ip-10-0-1-5', kind: 'node', health: 'healthy', metrics: { cpuPercent: 30 } }),
    asset({ name: 'old-job', health: 'inactive', firstSeen: '2026-10-21T08:00:00Z' }),
    asset({ name: 'build-01', kind: 'host', health: 'healthy' }),
  ];
  const sorted = (...args: Parameters<typeof compareAssets>) =>
    names([...fixture].sort(compareAssets(...args)));

  it('sorts by health, then name bytewise, and reverses every key descending', () => {
    const byHealth = [
      'checkout-api',
      'checkout-api-1',
      'app',
      'shop',
      'build-01',
      'checkout-api-2',
      'ip-10-0-1-5',
      'prd',
      'old-job',
    ];
    expect(sorted('health')).toEqual(byHealth);
    expect(sorted('health', 'descend')).toEqual([...byHealth].reverse());
  });

  it('sorts kinds in the hierarchy order and missing metrics below any value', () => {
    expect(sorted('kind')).toEqual([
      'prd',
      'ip-10-0-1-5',
      'build-01',
      'shop',
      'checkout-api',
      'checkout-api-1',
      'checkout-api-2',
      'old-job',
      'app',
    ]);
    expect(sorted('cpu', 'descend')).toEqual([
      'checkout-api-1',
      'app',
      'ip-10-0-1-5',
      'checkout-api-2',
      'shop',
      'prd',
      'old-job',
      'checkout-api',
      'build-01',
    ]);
  });

  it('sorts age youngest first, to the microsecond', () => {
    const pods = fixture.filter((a) => a.kind === 'pod').sort(compareAssets('age'));
    expect(names(pods)).toEqual(['checkout-api-1', 'checkout-api-2', 'old-job']);
  });

  it('puts leaves before their parents for attention', () => {
    const problems = fixture.filter((a) => a.health === 'critical' || a.health === 'warning');
    expect(names(problems.sort(compareAssets('attention')))).toEqual([
      'checkout-api-1',
      'checkout-api',
      'app',
      'shop',
    ]);
  });
});

describe('mergeSorted', () => {
  it('merges sorted lists into one sorted list', () => {
    const compare = (a: number, b: number) => a - b;
    expect(mergeSorted([[1, 4, 9], [], [2, 3, 10], [5]], compare)).toEqual([1, 2, 3, 4, 5, 9, 10]);
  });
});

describe('loadMergedPage', () => {
  const siteA: SiteRef = { siteId: 'a', siteName: 'Site A' };
  const siteB: SiteRef = { siteId: 'b', siteName: 'Site B' };
  const compare = compareAssets('name');
  const data: Record<string, SiteAsset[]> = {
    a: ['a1', 'a3', 'a5', 'a7', 'a9', 'b0'].map((name) => asset({ name, siteId: 'a' })),
    b: ['a2', 'a4', 'a6', 'a8'].map((name) => asset({ name, siteId: 'b' })),
  };
  const fakeSites = () => {
    const calls: string[] = [];
    const fetchPage = async (site: SiteRef, offset: number, limit: number): Promise<AssetList> => {
      calls.push(`${site.siteId}:${offset}+${limit}`);
      const all = [...data[site.siteId]].sort(compare);
      return { assets: all.slice(offset, offset + limit), total: all.length };
    };
    return { calls, fetchPage };
  };

  it('pages one Site on the server', async () => {
    const { calls, fetchPage } = fakeSites();
    const page = await loadMergedPage([siteA], new Map(), fetchPage, compare, 2, 2);
    expect(names(page.rows)).toEqual(['a5', 'a7']);
    expect(page.rows[0].siteName).toBe('Site A');
    expect(page.total).toBe(6);
    expect(calls).toEqual(['a:2+2']);
  });

  it('gives consecutive pages of the Sites merged, reading only what it has not read yet', async () => {
    const { calls, fetchPage } = fakeSites();
    const cursors = new Map<string, SiteCursor>();
    const seen: string[] = [];
    for (let offset = 0; offset < 10; offset += 3) {
      const page = await loadMergedPage([siteA, siteB], cursors, fetchPage, compare, offset, 3);
      expect(page.total).toBe(10);
      seen.push(...names(page.rows));
    }
    expect(seen).toEqual(['a1', 'a2', 'a3', 'a4', 'a5', 'a6', 'a7', 'a8', 'a9', 'b0']);
    expect(calls).toEqual(['a:0+3', 'b:0+3', 'a:3+3', 'b:3+3']);
  });

  it('reports a Site that fails and still shows the others', async () => {
    const { fetchPage } = fakeSites();
    const failing = async (site: SiteRef, offset: number, limit: number) => {
      if (site.siteId === 'b') throw new Error('Site unavailable');
      return fetchPage(site, offset, limit);
    };
    const page = await loadMergedPage([siteA, siteB], new Map(), failing, compare, 0, 4);
    expect(names(page.rows)).toEqual(['a1', 'a3', 'a5', 'a7']);
    expect(page.total).toBe(6);
    expect(page.errors).toEqual([{ site: siteB, error: 'Site unavailable' }]);
  });
});

describe('mergeSummaries', () => {
  const summary = (overrides: Partial<InventorySummary>): InventorySummary => ({
    total: 0,
    kinds: {},
    health: {},
    states: {},
    projects: [],
    ...overrides,
  });

  it('adds kinds, live health, groups, regions and clusters up across Sites', () => {
    const merged = mergeSummaries(
      [
        {
          siteId: 'a',
          siteName: 'Site A',
          summary: summary({
            total: 5,
            kinds: { pod: { healthy: 3, critical: 1 }, cluster: { healthy: 1 } },
            live: { healthy: 4, critical: 1 },
            projects: [{ id: 'p', name: 'shop', assets: 5, live: { healthy: 4, critical: 1 } }],
            regions: [
              {
                provider: 'aws',
                region: 'us-east-1',
                assets: 2,
                warning: 0,
                critical: 1,
                projects: ['shop'],
              },
            ],
            clusters: ['prd'],
          }),
        },
        {
          siteId: 'b',
          siteName: 'Site B',
          summary: summary({
            total: 2,
            kinds: { pod: { warning: 2 } },
            live: { warning: 1 },
            projects: [{ id: 'q', name: 'shop', assets: 2, live: { warning: 1 } }],
            regions: [
              {
                provider: 'aws',
                region: 'us-east-1',
                assets: 3,
                warning: 1,
                critical: 0,
                projects: ['shop'],
              },
              {
                provider: '',
                region: 'eu-west-1',
                assets: 4,
                warning: 0,
                critical: 0,
                projects: ['ops'],
              },
            ],
            clusters: ['edge', 'prd'],
          }),
        },
      ],
      true,
    );
    expect(merged.total).toBe(7);
    expect(merged.kinds.get('pod')).toMatchObject({ healthy: 3, critical: 1, warning: 2 });
    expect(merged.live).toMatchObject({ healthy: 4, critical: 1, warning: 1 });
    expect([...merged.groups.keys()]).toEqual(['Site A', 'Site B']);
    expect(merged.regions).toEqual([
      { key: 'aws · us-east-1', assets: 5, warning: 1, critical: 1, where: ['Site A', 'Site B'] },
      { key: 'cloud · eu-west-1', assets: 4, warning: 0, critical: 0, where: ['Site B'] },
    ]);
    expect(merged.clusters).toEqual(['edge', 'prd']);
  });

  it('groups by project on one Site and leaves out projects with nothing live', () => {
    const merged = mergeSummaries(
      [
        {
          siteId: '',
          siteName: '',
          summary: summary({
            projects: [
              { id: 'p', name: 'shop', assets: 3, live: { healthy: 3 } },
              { id: 'q', name: 'old', assets: 1, live: {} },
            ],
          }),
        },
      ],
      false,
    );
    expect([...merged.groups.keys()]).toEqual(['shop']);
  });
});

describe('assetSearchParams', () => {
  it('joins lists with commas and leaves empty values out', () => {
    expect(
      assetSearchParams({
        kind: ['pod', 'node'],
        health: [],
        q: '',
        cluster: undefined,
        roots: false,
        childCounts: true,
        sort: 'cpu',
        order: 'desc',
        offset: 0,
        limit: 100,
      }).toString(),
    ).toBe('kind=pod%2Cnode&childCounts=true&sort=cpu&order=desc&offset=0&limit=100');
  });
});
