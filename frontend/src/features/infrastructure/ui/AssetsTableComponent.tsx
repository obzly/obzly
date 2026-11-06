import { Input, Segmented, Select, Table, Tag, Tooltip } from 'antd';
import type { ColumnsType, SorterResult } from 'antd/es/table/interface';
import { useMemo, useState } from 'react';

import type { AssetHealth, AssetKind, SiteAsset } from '../../../entity/inventory';
import {
  type AssetNode,
  HEALTH_COLOR,
  HEALTH_LABEL,
  HEALTH_ORDER,
  KIND_FILTERS,
  assetUid,
  buildTree,
  since,
} from '../lib/assets';
import type { SiteResult } from '../lib/useInventory';
import { HealthDot, KindIcon, KindLabel, LifecycleTag, PercentBar } from './parts';

type SortKey = 'health' | 'kind' | 'name' | 'cpu' | 'memory' | 'restarts' | 'age' | 'lastSeen';
type SortOrder = 'ascend' | 'descend';
type HealthFilter = 'all' | AssetHealth;
type Layout = 'flat' | 'tree';

interface Props {
  assets: SiteAsset[];
  sites: SiteResult[];
  /** Several Sites: show and filter by Site. */
  multiSite: boolean;
  kinds: Set<AssetKind>;
  onKindsChange: (kinds: Set<AssetKind>) => void;
  onOpen: (asset: SiteAsset) => void;
}

const SORT_STORAGE_KEY = 'infrastructure_sort';
const LAYOUT_STORAGE_KEY = 'infrastructure_layout';
const SORT_KEYS: SortKey[] = [
  'health',
  'kind',
  'name',
  'cpu',
  'memory',
  'restarts',
  'age',
  'lastSeen',
];
const KIND_ORDER = Object.fromEntries(KIND_FILTERS.map((kind, index) => [kind.value, index]));

const readStored = <T,>(key: string, valid: (value: T) => boolean, fallback: T): T => {
  try {
    const stored = JSON.parse(localStorage.getItem(key) ?? 'null') as T;
    if (stored !== null && valid(stored)) return stored;
  } catch {
    /* fallback */
  }
  return fallback;
};

const store = (key: string, value: unknown) => {
  try {
    localStorage.setItem(key, JSON.stringify(value));
  } catch {
    /* not remembered */
  }
};

const HEALTH_FILTERS: HealthFilter[] = [
  'all',
  'critical',
  'warning',
  'healthy',
  'inactive',
  'unknown',
];

const byName = (a: SiteAsset, b: SiteAsset) => a.name.localeCompare(b.name);
const numeric = (value: (asset: SiteAsset) => number | undefined) => (a: SiteAsset, b: SiteAsset) =>
  (value(a) ?? -1) - (value(b) ?? -1) || byName(a, b);

const SORTERS: Record<SortKey, (a: SiteAsset, b: SiteAsset) => number> = {
  health: (a, b) => HEALTH_ORDER[a.health] - HEALTH_ORDER[b.health] || byName(a, b),
  kind: (a, b) => KIND_ORDER[a.kind] - KIND_ORDER[b.kind] || byName(a, b),
  name: byName,
  cpu: numeric((asset) => asset.metrics.cpuPercent),
  memory: numeric((asset) => asset.metrics.memoryPercent),
  restarts: numeric((asset) => asset.metrics.restarts24h),
  age: (a, b) => b.firstSeen.localeCompare(a.firstSeen) || byName(a, b),
  lastSeen: (a, b) => b.lastSeen.localeCompare(a.lastSeen) || byName(a, b),
};

export const AssetsTableComponent = ({
  assets,
  sites,
  multiSite,
  kinds,
  onKindsChange,
  onOpen,
}: Props) => {
  const [sort, setSort] = useState(() =>
    readStored<{ key: SortKey; order: SortOrder }>(
      SORT_STORAGE_KEY,
      (value) => SORT_KEYS.includes(value.key) && ['ascend', 'descend'].includes(value.order),
      { key: 'health', order: 'ascend' },
    ),
  );
  const [layout, setLayout] = useState<Layout>(() =>
    readStored<Layout>(LAYOUT_STORAGE_KEY, (value) => value === 'flat' || value === 'tree', 'flat'),
  );
  const [health, setHealth] = useState<HealthFilter>('all');
  const [search, setSearch] = useState('');
  const [site, setSite] = useState<string>('');
  const [cluster, setCluster] = useState<string>('');

  const clusters = useMemo(
    () => [...new Set(assets.filter((a) => a.kind === 'cluster').map((a) => a.cluster))].sort(),
    [assets],
  );

  const matches = useMemo(() => {
    const text = search.trim().toLowerCase();
    return (asset: SiteAsset) =>
      (health === 'all' || asset.health === health) &&
      (kinds.size === 0 || kinds.has(asset.kind)) &&
      (!site || asset.siteId === site) &&
      (!cluster || asset.cluster === cluster) &&
      (!text ||
        asset.name.toLowerCase().includes(text) ||
        asset.node.toLowerCase().includes(text) ||
        asset.projectName.toLowerCase().includes(text) ||
        Object.values(asset.attributes).some((value) => value.toLowerCase().includes(text)));
  }, [health, kinds, site, cluster, search]);

  const rows = useMemo<AssetNode[]>(() => {
    if (layout === 'flat') {
      return assets.filter(matches).map((asset) => ({ ...asset, uid: assetUid(asset) }));
    }
    // A tree keeps a branch when it or anything under it matches.
    const prune = (nodes: AssetNode[]): AssetNode[] =>
      nodes.flatMap((node) => {
        const children = node.children ? prune(node.children) : [];
        if (!matches(node) && children.length === 0) return [];
        return [{ ...node, children: children.length > 0 ? children : undefined }];
      });
    return prune(buildTree(assets));
  }, [assets, layout, matches]);

  const sortable = (key: SortKey) => ({
    sorter: SORTERS[key],
    sortOrder: sort.key === key ? sort.order : null,
    sortDirections: ['ascend', 'descend', 'ascend'] as SortOrder[],
  });
  const usageTitle = (basis?: string) =>
    basis === 'limit' ? 'Of its limit' : basis === 'node' ? 'Of its node' : undefined;

  const columns: ColumnsType<AssetNode> = [
    {
      title: 'Health',
      key: 'health',
      width: 80,
      ...sortable('health'),
      render: (_, asset) => <HealthDot health={asset.health} />,
    },
    {
      title: 'Kind',
      key: 'kind',
      width: 160,
      ...sortable('kind'),
      render: (_, asset) => <KindLabel asset={asset} />,
    },
    {
      title: 'Name',
      key: 'name',
      width: 340,
      className: 'whitespace-nowrap',
      ...sortable('name'),
      render: (_, asset) => (
        <span className="inline-flex max-w-[270px] items-center gap-1.5 align-middle">
          {layout === 'tree' && <KindIcon asset={asset} size={16} />}
          <Tooltip title={asset.name} placement="top" mouseEnterDelay={0.4}>
            <span className="truncate font-medium">{asset.name}</span>
          </Tooltip>
        </span>
      ),
    },
    {
      title: multiSite ? 'Site / project' : 'Project',
      key: 'project',
      width: 190,
      render: (_, asset) => (
        <span className="text-gray-600 dark:text-gray-300">
          {multiSite ? `${asset.siteName} / ${asset.projectName}` : asset.projectName}
        </span>
      ),
    },
    {
      title: 'Cluster / namespace',
      key: 'where',
      width: 200,
      render: (_, asset) =>
        asset.cluster || asset.namespace ? (
          <span className="font-mono text-xs text-gray-600 dark:text-gray-300">
            {[asset.cluster, asset.namespace].filter(Boolean).join(' / ')}
          </span>
        ) : (
          <span className="text-gray-400">—</span>
        ),
    },
    {
      title: 'Node / host',
      key: 'node',
      width: 170,
      render: (_, asset) =>
        asset.node && asset.kind !== 'node' && asset.kind !== 'host' ? (
          <Tooltip title={asset.node} placement="top">
            <span className="block max-w-[160px] truncate font-mono text-xs">
              {asset.node.split('.')[0]}
            </span>
          </Tooltip>
        ) : (
          <span className="text-gray-400">—</span>
        ),
    },
    {
      title: 'CPU',
      key: 'cpu',
      width: 120,
      ...sortable('cpu'),
      render: (_, asset) => (
        <PercentBar
          value={asset.metrics.cpuPercent}
          criticalAt={101}
          title={usageTitle(asset.metrics.cpuBasis)}
        />
      ),
    },
    {
      title: 'Memory',
      key: 'memory',
      width: 120,
      ...sortable('memory'),
      render: (_, asset) => (
        <PercentBar
          value={asset.metrics.memoryPercent}
          title={usageTitle(asset.metrics.memoryBasis)}
        />
      ),
    },
    {
      title: 'Restarts 24h',
      key: 'restarts',
      width: 120,
      align: 'right',
      ...sortable('restarts'),
      render: (_, asset) =>
        asset.metrics.restarts24h === undefined ? (
          <span className="text-gray-400">—</span>
        ) : (
          <span className="tabular-nums">{asset.metrics.restarts24h}</span>
        ),
    },
    {
      title: 'Age',
      key: 'age',
      width: 80,
      ...sortable('age'),
      render: (_, asset) => <span className="tabular-nums">{since(asset.firstSeen)}</span>,
    },
    {
      title: 'Last seen',
      key: 'lastSeen',
      width: 100,
      ...sortable('lastSeen'),
      render: (_, asset) => (
        <span className="text-gray-500 tabular-nums dark:text-gray-400">
          {since(asset.lastSeen)}
        </span>
      ),
    },
    {
      title: 'Lifecycle',
      key: 'state',
      width: 120,
      render: (_, asset) => <LifecycleTag state={asset.state} />,
    },
  ];

  const toggleKind = (kind: AssetKind) => {
    const next = new Set(kinds);
    if (next.has(kind)) next.delete(kind);
    else next.add(kind);
    onKindsChange(next);
  };

  return (
    <div>
      <div className="mb-3 flex flex-wrap items-center gap-3">
        <Input.Search
          allowClear
          placeholder="Search name, label or IP"
          className="max-w-xs"
          onChange={(event) => setSearch(event.target.value)}
        />
        <Segmented<HealthFilter>
          value={health}
          onChange={setHealth}
          options={HEALTH_FILTERS.map((value) => ({
            value,
            label: (
              <span className="inline-flex items-center gap-1.5">
                {value !== 'all' && (
                  <span
                    className="inline-block h-2 w-2 rounded-full"
                    style={{ background: HEALTH_COLOR[value] }}
                  />
                )}
                {value === 'all' ? 'All' : HEALTH_LABEL[value]}
              </span>
            ),
          }))}
        />
        <div className="flex-1" />
        <Segmented<Layout>
          value={layout}
          onChange={(value) => {
            setLayout(value);
            store(LAYOUT_STORAGE_KEY, value);
          }}
          options={[
            { value: 'flat', label: 'Flat' },
            { value: 'tree', label: 'Tree' },
          ]}
        />
      </div>
      <div className="mb-3 flex flex-wrap items-center gap-2">
        <Tag.CheckableTag checked={kinds.size === 0} onChange={() => onKindsChange(new Set())}>
          All kinds
        </Tag.CheckableTag>
        {KIND_FILTERS.map((kind) => (
          <Tag.CheckableTag
            key={kind.value}
            checked={kinds.has(kind.value)}
            onChange={() => toggleKind(kind.value)}
          >
            {kind.label}
          </Tag.CheckableTag>
        ))}
        <div className="flex-1" />
        {multiSite && (
          <Select
            value={site}
            onChange={setSite}
            className="min-w-40"
            options={[
              { value: '', label: 'All Sites' },
              ...sites.map((s) => ({ value: s.siteId, label: s.siteName, disabled: !!s.error })),
            ]}
          />
        )}
        {clusters.length > 0 && (
          <Select
            value={cluster}
            onChange={setCluster}
            className="min-w-44"
            options={[
              { value: '', label: 'All clusters' },
              ...clusters.map((name) => ({ value: name, label: name || '(unnamed cluster)' })),
            ]}
          />
        )}
      </div>
      <Table<AssetNode>
        rowKey="uid"
        size="middle"
        dataSource={rows}
        columns={columns}
        pagination={layout === 'flat' ? { defaultPageSize: 100, showSizeChanger: true } : false}
        scroll={{ x: 1640 }}
        showSorterTooltip={false}
        expandable={
          layout === 'tree'
            ? { indentSize: 18, expandIconColumnIndex: 2 }
            : { childrenColumnName: '_none' }
        }
        onRow={(asset) => ({ onClick: () => onOpen(asset), className: 'cursor-pointer' })}
        onChange={(_, __, sorter) => {
          const current = (Array.isArray(sorter) ? sorter[0] : sorter) as SorterResult<AssetNode>;
          const next = {
            key: (current.columnKey as SortKey) ?? 'health',
            order: (current.order ?? 'ascend') as SortOrder,
          };
          setSort(next);
          store(SORT_STORAGE_KEY, next);
        }}
        locale={{
          emptyText: assets.length === 0 ? 'Nothing in the inventory yet' : 'No assets match',
        }}
      />
    </div>
  );
};
