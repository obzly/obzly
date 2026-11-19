import { LoadingOutlined } from '@ant-design/icons';
import { Alert, Input, Segmented, Select, Table, Tag, Tooltip } from 'antd';
import type { ColumnsType, SorterResult } from 'antd/es/table/interface';
import { useEffect, useMemo, useRef, useState } from 'react';

import {
  type AssetHealth,
  type AssetKind,
  type AssetQuery,
  type SiteAsset,
  type SiteRef,
  inventoryApi,
} from '../../../entity/inventory';
import { Card } from '../../../shared/ui/v2';
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
import { type SiteCursor, type SortOrder, compareAssets, loadMergedPage } from '../lib/paging';
import type { SiteResult } from '../lib/useInventory';
import { HealthDot, KindIcon, KindLabel, LifecycleTag, PercentBar } from './parts';

type SortKey = 'health' | 'kind' | 'name' | 'cpu' | 'memory' | 'restarts' | 'age' | 'lastSeen';
type HealthFilter = 'all' | AssetHealth;
type Layout = 'flat' | 'tree';

interface Props {
  sites: SiteResult[];
  /** Cluster names across the Sites, for the cluster filter. */
  clusters: string[];
  /** Every asset across the Sites (empty inventory vs no match). */
  inventoryTotal: number;
  /** Several Sites: show and filter by Site. */
  multiSite: boolean;
  kinds: Set<AssetKind>;
  onKindsChange: (kinds: Set<AssetKind>) => void;
  onOpen: (asset: SiteAsset) => void;
  /** Bumped to read the assets again. */
  refresh: number;
}

/** A list the tree shows only the first of, said as "1,000 of 1,412 under checkout-api". */
interface Capped {
  key: string;
  text: string;
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
const PAGE_SIZES = [50, 100, 200, 500];
// The tree reads at most this many matches, roots or children per Site and level.
const TREE_LIMIT = 1000;
const SEARCH_DELAY_MS = 300;

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

// Kind filters look like the v2 chips; antd keeps the checked colours.
const CHIP = 'm-0 rounded-lg border border-gray-200 px-2.5 py-0.5 text-[13px] dark:border-gray-700';

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

const toNode = (asset: SiteAsset): AssetNode => ({ ...asset, uid: assetUid(asset) });

// Server-side paging, sorting and filtering in the flat layout; the tree reads its roots and opens
// branches on demand, or reads the matches with their parents when filtered.
export const AssetsTableComponent = ({
  sites,
  clusters,
  inventoryTotal,
  multiSite,
  kinds,
  onKindsChange,
  onOpen,
  refresh,
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
  const [searchInput, setSearchInput] = useState('');
  const [search, setSearch] = useState('');
  const [site, setSite] = useState<string>('');
  const [cluster, setCluster] = useState<string>('');
  const [pageSize, setPageSize] = useState(100);
  // The page belongs to its filters and sort: new ones start at page 1.
  const [paging, setPaging] = useState({ key: '', page: 1 });
  const [flat, setFlat] = useState<{ rows: AssetNode[]; total: number }>({ rows: [], total: 0 });
  const [treeRoots, setTreeRoots] = useState<AssetNode[]>([]);
  // Children read per branch, with the refresh they were read at.
  const [children, setChildren] = useState<Map<string, { nodes: AssetNode[]; at: number }>>(
    new Map(),
  );
  const [expanded, setExpanded] = useState<Map<string, AssetNode>>(new Map());
  const [pending, setPending] = useState<Set<string>>(new Set());
  const [capped, setCapped] = useState<Capped[]>([]);
  const [errors, setErrors] = useState<string[]>([]);
  const [loading, setLoading] = useState(true);
  // What the flat layout has read of each Site, for the filters and sort it was read with.
  const cursors = useRef({ key: '', map: new Map<string, SiteCursor>() });
  const treeQuery = useRef('');
  // The last view read: the 60 s refresh reads it again without the loading overlay.
  const shown = useRef('');

  const targets = useMemo<SiteRef[]>(
    () =>
      sites
        .filter((s) => !s.error && (!site || s.siteId === site))
        .map((s) => ({ siteId: s.siteId, siteName: s.siteName })),
    [sites, site],
  );
  const filters = useMemo<AssetQuery>(
    () => ({
      kind: [...kinds],
      health: health === 'all' ? undefined : [health],
      cluster: cluster || undefined,
      q: search || undefined,
    }),
    [kinds, health, cluster, search],
  );
  const filtered = kinds.size > 0 || health !== 'all' || search !== '';
  const queryKey = JSON.stringify([filters, targets.map((t) => t.siteId)]);
  const pageKey = JSON.stringify([queryKey, sort]);
  const page = paging.key === pageKey ? paging.page : 1;

  useEffect(() => {
    const timer = setTimeout(() => setSearch(searchInput.trim()), SEARCH_DELAY_MS);
    return () => clearTimeout(timer);
  }, [searchInput]);

  // Flat: one page across the Sites.
  useEffect(() => {
    if (layout !== 'flat') return;
    const controller = new AbortController();
    const cursorKey = `${pageKey}|${refresh}`;
    if (cursors.current.key !== cursorKey) cursors.current = { key: cursorKey, map: new Map() };
    const map = cursors.current.map;
    let done = false;
    const view = JSON.stringify([layout, pageKey, page, pageSize]);
    if (shown.current !== view) setLoading(true);
    shown.current = view;
    const query: AssetQuery = {
      ...filters,
      sort: sort.key,
      order: sort.order === 'descend' ? 'desc' : 'asc',
    };
    void loadMergedPage(
      targets,
      map,
      (target, offset, limit) =>
        inventoryApi.assets(target.siteId, { ...query, offset, limit }, controller.signal),
      compareAssets(sort.key, sort.order),
      (page - 1) * pageSize,
      pageSize,
    ).then((result) => {
      done = true;
      if (controller.signal.aborted) return;
      setFlat({ rows: result.rows.map(toNode), total: result.total });
      setErrors(result.errors.map((e) => `${e.site.siteName || 'This Site'}: ${e.error}`));
      setCapped([]);
      setLoading(false);
    });
    return () => {
      controller.abort();
      // A cancelled read may have left a Site's cursor failed: start over.
      if (!done && cursors.current.map === map) cursors.current = { key: '', map: new Map() };
    };
  }, [layout, pageKey, page, pageSize, refresh]);

  // Tree: the roots, or the matches with their parents.
  useEffect(() => {
    if (layout !== 'tree') return;
    const controller = new AbortController();
    const view = JSON.stringify([layout, queryKey]);
    if (shown.current !== view) {
      setLoading(true);
      setChildren(new Map());
    }
    shown.current = view;
    // New filters close the branches; a refresh keeps them open and reads them again.
    if (!filtered && treeQuery.current !== queryKey) setExpanded(new Map());
    treeQuery.current = queryKey;
    const query: AssetQuery = filtered
      ? { ...filters, ancestors: true, limit: TREE_LIMIT }
      : { cluster: cluster || undefined, roots: true, childCounts: true, limit: TREE_LIMIT };
    void Promise.all(
      targets.map((target) =>
        inventoryApi
          .assets(target.siteId, query, controller.signal)
          .then((list) => ({ target, list, error: undefined }))
          .catch((reason) => ({ target, list: undefined, error: (reason as Error).message })),
      ),
    ).then((results) => {
      if (controller.signal.aborted) return;
      const nodes: SiteAsset[] = [];
      const cappedNow: Capped[] = [];
      for (const { target, list } of results) {
        if (!list) continue;
        for (const asset of [...list.assets, ...(list.ancestors ?? [])]) {
          nodes.push({ ...asset, ...target });
        }
        if (list.total > list.assets.length) {
          cappedNow.push({
            key: target.siteId,
            text: `${list.assets.length.toLocaleString()} of ${list.total.toLocaleString()} ${filtered ? 'matches' : 'top-level assets'}${multiSite ? ` on ${target.siteName}` : ''}`,
          });
        }
      }
      const roots = filtered ? buildTree(nodes) : nodes.map(toNode);
      setTreeRoots(roots);
      if (filtered) {
        // Matches sit inside their branches: open every branch so they show.
        const open = new Map<string, AssetNode>();
        const walk = (list: AssetNode[]) =>
          list.forEach((node) => {
            if (node.children) {
              open.set(node.uid, node);
              walk(node.children);
            }
          });
        walk(roots);
        setExpanded(open);
      }
      setCapped(cappedNow);
      setErrors(
        results.flatMap((r) =>
          r.error ? [`${r.target.siteName || 'This Site'}: ${r.error}`] : [],
        ),
      );
      setLoading(false);
    });
    return () => controller.abort();
  }, [layout, queryKey, refresh]);

  // Unfiltered tree: read the children of open branches not read since the last refresh.
  useEffect(() => {
    if (layout !== 'tree' || filtered) return;
    const missing = [...expanded.values()].filter(
      (node) => children.get(node.uid)?.at !== refresh && !pending.has(node.uid),
    );
    if (missing.length === 0) return;
    setPending((current) => new Set([...current, ...missing.map((node) => node.uid)]));
    for (const node of missing) {
      const target = { siteId: node.siteId, siteName: node.siteName };
      inventoryApi
        .assets(node.siteId, {
          projectId: node.projectId,
          parent: node.key,
          cluster: cluster || undefined,
          childCounts: true,
          limit: TREE_LIMIT,
        })
        .then((list) => {
          setChildren((current) =>
            new Map(current).set(node.uid, {
              nodes: list.assets.map((asset) => toNode({ ...asset, ...target })),
              at: refresh,
            }),
          );
          if (list.total > list.assets.length) {
            setCapped((current) => [
              ...current.filter((c) => c.key !== node.uid),
              {
                key: node.uid,
                text: `${list.assets.length.toLocaleString()} of ${list.total.toLocaleString()} under ${node.name}`,
              },
            ]);
          }
        })
        .catch((reason) => {
          setErrors((current) => [...current, `${node.name}: ${(reason as Error).message}`]);
          // Not tried again before the next refresh.
          setChildren((current) => new Map(current).set(node.uid, { nodes: [], at: refresh }));
        })
        .finally(() =>
          setPending((current) => {
            const next = new Set(current);
            next.delete(node.uid);
            return next;
          }),
        );
    }
  }, [layout, filtered, expanded, children, refresh, pending]);

  const rows = useMemo<AssetNode[]>(() => {
    if (layout === 'flat') return flat.rows;
    if (filtered) return treeRoots;
    // A branch with children not read yet gets an empty list, so it shows as expandable.
    const grow = (node: AssetNode): AssetNode => {
      const loaded = children.get(node.uid)?.nodes;
      return {
        ...node,
        children: loaded
          ? loaded.length > 0
            ? loaded.map(grow)
            : undefined
          : node.childCount
            ? []
            : undefined,
      };
    };
    return treeRoots.map(grow);
  }, [layout, filtered, flat.rows, treeRoots, children]);

  // The flat layout sorts on the Sites; the tree sorts what it has read.
  const sortable = (key: SortKey) => ({
    sorter: layout === 'flat' ? true : SORTERS[key],
    sortOrder: sort.key === key ? sort.order : null,
    sortDirections: ['ascend', 'descend', 'ascend'] as SortOrder[],
  });
  const usageTitle = (basis?: string) =>
    basis === 'limit' ? 'Of its limit' : basis === 'node' ? 'Of its node' : undefined;

  const columns: ColumnsType<AssetNode> = [
    {
      title: 'Health',
      key: 'health',
      width: 110,
      ...sortable('health'),
      render: (_, asset) => (
        <span className="inline-flex items-center gap-2 text-xs whitespace-nowrap text-gray-600 dark:text-gray-300">
          <HealthDot health={asset.health} />
          {HEALTH_LABEL[asset.health]}
        </span>
      ),
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
          {layout === 'tree' &&
            (pending.has(asset.uid) ? (
              <LoadingOutlined spin className="text-gray-400" />
            ) : (
              <KindIcon asset={asset} size={16} />
            ))}
          <Tooltip title={asset.name} placement="top" mouseEnterDelay={0.4}>
            <span className="truncate font-mono text-[13px] font-medium">{asset.name}</span>
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
    <Card>
      <div className="mb-3 flex flex-wrap items-center gap-3">
        <Input.Search
          allowClear
          placeholder="Search name, label or IP"
          className="max-w-xs"
          value={searchInput}
          onChange={(event) => setSearchInput(event.target.value)}
          onSearch={(value) => setSearch(value.trim())}
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
            setExpanded(new Map());
            store(LAYOUT_STORAGE_KEY, value);
          }}
          options={[
            { value: 'flat', label: 'Flat' },
            { value: 'tree', label: 'Tree' },
          ]}
        />
      </div>
      <div className="mb-3 flex flex-wrap items-center gap-2">
        <Tag.CheckableTag
          className={CHIP}
          checked={kinds.size === 0}
          onChange={() => onKindsChange(new Set())}
        >
          All kinds
        </Tag.CheckableTag>
        {KIND_FILTERS.map((kind) => (
          <Tag.CheckableTag
            key={kind.value}
            className={CHIP}
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
      {errors.length > 0 && (
        <Alert
          className="mb-3"
          type="error"
          showIcon
          title={`Couldn't read ${errors.join('; ')}`}
        />
      )}
      {layout === 'tree' && capped.length > 0 && (
        <Alert
          className="mb-3"
          type="info"
          showIcon
          title={`Showing ${capped.map((c) => c.text).join(', ')}. Narrow the search or filters, or use the flat layout to page through all of them.`}
        />
      )}
      <Table<AssetNode>
        rowKey="uid"
        size="middle"
        dataSource={rows}
        columns={columns}
        loading={loading && { indicator: <LoadingOutlined spin /> }}
        pagination={
          layout === 'flat'
            ? {
                current: page,
                pageSize,
                total: flat.total,
                showSizeChanger: true,
                pageSizeOptions: PAGE_SIZES,
                showTotal: (total, [from, to]) =>
                  `Showing ${from.toLocaleString()}–${to.toLocaleString()} of ${total.toLocaleString()}`,
              }
            : false
        }
        scroll={{ x: 1640 }}
        showSorterTooltip={false}
        expandable={
          layout === 'tree'
            ? {
                indentSize: 18,
                expandIconColumnIndex: 2,
                expandedRowKeys: [...expanded.keys()],
                onExpand: (open, node) =>
                  setExpanded((current) => {
                    const next = new Map(current);
                    if (open) next.set(node.uid, node);
                    else next.delete(node.uid);
                    return next;
                  }),
              }
            : { childrenColumnName: '_none' }
        }
        onRow={(asset) => ({ onClick: () => onOpen(asset), className: 'cursor-pointer' })}
        onChange={(pagination, __, sorter, extra) => {
          if (extra.action === 'paginate') {
            if (pagination.pageSize && pagination.pageSize !== pageSize) {
              setPageSize(pagination.pageSize);
              setPaging({ key: pageKey, page: 1 });
            } else {
              setPaging({ key: pageKey, page: pagination.current ?? 1 });
            }
            return;
          }
          const current = (Array.isArray(sorter) ? sorter[0] : sorter) as SorterResult<AssetNode>;
          const next = {
            key: (current.columnKey as SortKey) ?? 'health',
            order: (current.order ?? 'ascend') as SortOrder,
          };
          setSort(next);
          store(SORT_STORAGE_KEY, next);
        }}
        locale={{
          emptyText: loading
            ? ' '
            : inventoryTotal === 0
              ? 'Nothing in the inventory yet'
              : 'No assets match',
        }}
      />
    </Card>
  );
};
