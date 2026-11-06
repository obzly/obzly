import { describe, expect, it } from 'vitest';

import type { SiteAsset } from '../../../entity/inventory';
import { buildTree, iconFor, logsSearchFor, since } from './assets';
import { chartQueries } from './charts';

const asset = (overrides: Partial<SiteAsset>): SiteAsset => ({
  id: overrides.key ?? 'id',
  projectId: 'p1',
  projectName: 'shop',
  kind: 'pod',
  key: 'k',
  name: 'n',
  subtype: '',
  parentKey: '',
  nodeKey: '',
  cluster: 'prd',
  namespace: 'shop',
  node: '',
  attributes: {},
  sources: ['metrics'],
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

describe('buildTree', () => {
  it('nests assets under their parents within one Site and project', () => {
    const tree = buildTree([
      asset({ kind: 'cluster', key: 'cluster:prd' }),
      asset({ kind: 'namespace', key: 'ns:prd/shop', parentKey: 'cluster:prd' }),
      asset({ kind: 'pod', key: 'pod:1', parentKey: 'ns:prd/shop' }),
      // Same keys on another Site stay apart.
      asset({ kind: 'pod', key: 'pod:2', parentKey: 'ns:prd/shop', siteId: 's2' }),
    ]);

    expect(tree.map((node) => node.key)).toEqual(['cluster:prd', 'pod:2']);
    expect(tree[0].children?.[0].key).toBe('ns:prd/shop');
    expect(tree[0].children?.[0].children?.[0].key).toBe('pod:1');
  });
});

describe('iconFor', () => {
  it('uses the cloud icon when the platform says so, else the Kubernetes one', () => {
    expect(iconFor(asset({ kind: 'cluster', attributes: { cloud_platform: 'aws_eks' } }))).toBe(
      '/icons/infra/aws-eks.svg',
    );
    expect(iconFor(asset({ kind: 'cluster' }))).toBe('/icons/infra/k8s-cluster.svg');
    expect(iconFor(asset({ kind: 'host', attributes: { cloud_platform: 'azure_vm' } }))).toBe(
      '/icons/infra/azure-vm.svg',
    );
    expect(iconFor(asset({ kind: 'host' }))).toBeUndefined();
    expect(iconFor(asset({ kind: 'workload', subtype: 'StatefulSet' }))).toBe(
      '/icons/infra/k8s-statefulset.svg',
    );
  });
});

describe('logsSearchFor', () => {
  it('scopes a pod by namespace and name, quoting what needs it', () => {
    expect(logsSearchFor(asset({ kind: 'pod', name: 'web-1' }))).toBe(
      'k8s_namespace_name=shop AND k8s_pod_name=web-1',
    );
    expect(logsSearchFor(asset({ kind: 'service', name: 'check out' }))).toBe(
      'service_name="check out"',
    );
  });
});

describe('chartQueries', () => {
  it("quotes label values so a name can't break the query", () => {
    const [cpu] = chartQueries(asset({ kind: 'pod', name: 'web"1' }));
    expect(cpu.lines[0].query).toContain('k8s_pod_name="web\\"1"');
  });

  it("matches a workload's pods by name prefix, regex-escaped", () => {
    const [cpu] = chartQueries(asset({ kind: 'workload', subtype: 'Deployment', name: 'a.b' }));
    expect(cpu.lines[0].query).toContain('k8s_pod_name=~"a\\\\.b-.+"');
  });
});

describe('since', () => {
  it('reads minutes, hours and days', () => {
    const now = Date.parse('2026-10-25T12:00:00Z');
    expect(since('2026-10-25T11:48:00Z', now)).toBe('12 min');
    expect(since('2026-10-25T09:00:00Z', now)).toBe('3 h');
    expect(since('2026-10-15T12:00:00Z', now)).toBe('10 d');
  });
});
