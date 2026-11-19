import { LoadingOutlined } from '@ant-design/icons';
import { Segmented, Spin, Tooltip } from 'antd';
import { useMemo, useState } from 'react';

import type {
  AssetHealth,
  AssetKind,
  AssetQuery,
  SiteAsset,
  SiteChange,
  SiteRef,
} from '../../../entity/inventory';
import { absoluteChange } from '../../../shared/lib/periodChange';
import { Donut, KpiTile } from '../../../shared/ui/v2';
import { Panel } from '../../synthetics';
import {
  HEALTH_COLOR,
  HEALTH_LABEL,
  KIND_LABEL,
  STATE_LABEL,
  assetUid,
  kindLabel,
  since,
} from '../lib/assets';
import type { KindHistory } from '../lib/history';
import { type HealthCounts, type MergedSummary, healthTotal } from '../lib/paging';
import { useSiteAssets } from '../lib/useSiteAssets';
import { HealthDot, KindIcon } from './parts';

/** What opening an asset needs: its Site and id. */
export type AssetTarget = Pick<SiteAsset, 'siteId' | 'siteName' | 'id'>;

interface Props {
  /** Every answering Site's counts, added up. */
  summary: MergedSummary;
  /** The Sites that answered, for the panels' short lists. */
  sites: SiteRef[];
  changes: SiteChange[];
  /** Kind counts over time for the tiles' sparklines and changes vs 24 h ago. */
  history?: KindHistory;
  multiSite: boolean;
  refresh: number;
  onOpen: (asset: AssetTarget) => void;
  onOpenKind: (kind: AssetKind) => void;
}

const CARDS: { kind: AssetKind; label: string }[] = [
  { kind: 'cluster', label: 'Clusters' },
  { kind: 'node', label: 'Nodes' },
  { kind: 'host', label: 'Hosts / VMs' },
  { kind: 'workload', label: 'Workloads' },
  { kind: 'pod', label: 'Pods' },
  { kind: 'service', label: 'Services' },
];

const HEALTHS: AssetHealth[] = ['critical', 'warning', 'healthy', 'unknown', 'inactive'];
const ATTENTION_LIMIT = 12;
const CHANGES_LIMIT = 15;
const TOP_LIMIT = 10;
const LIVE: AssetQuery['state'] = ['new', 'active', 'not_reporting'];

// Leaves first: a pod's reason, not the namespace repeating it.
const ATTENTION_QUERY: AssetQuery = {
  health: ['critical', 'warning'],
  state: LIVE,
  sort: 'attention',
  limit: ATTENTION_LIMIT,
};

const topQuery = (metric: 'cpu' | 'memory'): AssetQuery => ({
  kind: ['pod', 'node', 'host'],
  state: LIVE,
  sort: metric,
  order: 'desc',
  limit: TOP_LIMIT,
});

export const OverviewComponent = ({
  summary,
  sites,
  changes,
  history,
  multiSite,
  refresh,
  onOpen,
  onOpenKind,
}: Props) => {
  const [metric, setMetric] = useState<'cpu' | 'memory'>('cpu');
  const attention = useSiteAssets(sites, ATTENTION_QUERY, refresh);
  const topAssets = useSiteAssets(sites, topQuery(metric), refresh);

  // Hourly counts over 7 days; a sparkline needs two points.
  const trendOf = (kind: AssetKind) => {
    const values = history?.trend[kind];
    return values && values.length > 1 ? values : undefined;
  };
  const counts = summary.kinds;
  const liveTotal = healthTotal(summary.live);

  const healthTotals = HEALTHS.filter((health) => health !== 'inactive').map((health) => ({
    health,
    value: summary.live[health],
  }));

  const top = useMemo(
    () =>
      topAssets.assets.flatMap((asset) => {
        const value = metric === 'cpu' ? asset.metrics.cpuPercent : asset.metrics.memoryPercent;
        return value === undefined ? [] : [{ asset, value }];
      }),
    [topAssets.assets, metric],
  );

  const groups = summary.groups;
  const regions = summary.regions;
  const groupNames = [...groups.keys()].sort();

  return (
    <div className="flex flex-col gap-4">
      <section
        aria-label="Kinds"
        className="grid grid-cols-[repeat(auto-fit,minmax(170px,1fr))] gap-4"
      >
        {CARDS.map((card) => {
          const row = counts.get(card.kind);
          const total = healthTotal(row);
          return (
            <KpiTile
              key={card.kind}
              label={card.label}
              value={total}
              onClick={() => onOpenKind(card.kind)}
              // A count going up or down is neither good nor bad news by itself.
              change={
                history?.previous &&
                absoluteChange(
                  total,
                  history.previous[card.kind] ?? 0,
                  'neither',
                  (difference) => `${difference.toLocaleString()} vs 24 h ago`,
                )
              }
              trend={trendOf(card.kind)}
              icon={
                <span className="inline-flex h-[34px] w-[34px] shrink-0 items-center justify-center rounded-[10px] bg-gray-50 dark:bg-gray-900">
                  <KindIcon
                    asset={{ kind: card.kind, subtype: 'Deployment', attributes: {} }}
                    size={22}
                  />
                </span>
              }
              note={
                <span className="flex flex-col gap-2">
                  <HealthBar row={row} total={total} />
                  <span className="flex flex-wrap gap-2 text-[11px]">
                    {row && row.critical > 0 && <Count health="critical" value={row.critical} />}
                    {row && row.warning > 0 && <Count health="warning" value={row.warning} />}
                    {row && row.inactive > 0 && <Count health="inactive" value={row.inactive} />}
                    {(!row || row.critical + row.warning + row.inactive === 0) && 'All healthy'}
                  </span>
                </span>
              }
            />
          );
        })}
      </section>

      <div className="grid grid-cols-1 gap-4 xl:grid-cols-3">
        <Panel title="Health" subtitle="All assets that are not terminated">
          <div className="flex flex-wrap items-center gap-6">
            <Donut
              label={`Health of ${liveTotal} live assets`}
              size={150}
              center={liveTotal}
              centerSub="live assets"
              parts={healthTotals
                .filter((item) => item.value > 0)
                .map((item) => ({
                  key: item.health,
                  value: item.value,
                  color: HEALTH_COLOR[item.health],
                }))}
            />
            <ul className="m-0 flex min-w-[140px] flex-1 list-none flex-col gap-2 p-0 text-sm">
              {healthTotals.map((item) => (
                <li key={item.health} className="flex items-center gap-2">
                  <span
                    className="inline-block h-2.5 w-2.5 rounded-[3px]"
                    style={{ background: HEALTH_COLOR[item.health] }}
                  />
                  <span className="flex-1 text-gray-600 dark:text-gray-300">
                    {HEALTH_LABEL[item.health]}
                  </span>
                  <strong className="font-semibold tabular-nums">{item.value}</strong>
                </li>
              ))}
            </ul>
          </div>
        </Panel>
        <Panel
          className="xl:col-span-2"
          title="Needs attention"
          subtitle="Critical first, with the reason. Click to open."
        >
          {attention.assets.length === 0 ? (
            <div className="py-8 text-center text-sm text-gray-500 dark:text-gray-400">
              {attention.loading ? (
                <Spin indicator={<LoadingOutlined spin />} />
              ) : (
                'Nothing needs attention'
              )}
            </div>
          ) : (
            <ul className="m-0 flex list-none flex-col p-0">
              {attention.assets.map((asset) => (
                <li
                  key={assetUid(asset)}
                  onClick={() => onOpen(asset)}
                  className="-mx-2 flex cursor-pointer items-start gap-3 rounded-lg border-b border-gray-100 px-2 py-2.5 last:border-b-0 hover:bg-gray-50 dark:border-gray-700 dark:hover:bg-gray-700/40"
                >
                  <span className="pt-1.5">
                    <HealthDot health={asset.health} />
                  </span>
                  <span className="pt-0.5">
                    <KindIcon asset={asset} size={18} />
                  </span>
                  <span className="min-w-0 flex-1">
                    <span className="flex flex-wrap items-center gap-2">
                      <b className="truncate font-mono text-[13px] font-semibold">{asset.name}</b>
                      <span className="text-xs text-gray-500 dark:text-gray-400">
                        {kindLabel(asset)}
                      </span>
                    </span>
                    <span className="block text-sm text-gray-600 dark:text-gray-300">
                      {asset.reasons[0]?.text}
                    </span>
                  </span>
                  <span className="font-mono text-xs text-gray-500 dark:text-gray-400">
                    {multiSite ? asset.siteName : asset.projectName}
                  </span>
                </li>
              ))}
              {attention.total > attention.assets.length && (
                <li className="pt-2 text-xs text-gray-500 dark:text-gray-400">
                  Showing {attention.assets.length} of {attention.total.toLocaleString()}; the rest
                  are in Assets
                </li>
              )}
            </ul>
          )}
        </Panel>
      </div>

      <div className="grid grid-cols-1 gap-4 xl:grid-cols-2">
        <Panel title="Recent changes" subtitle="New, stopped and health changes, last 24 hours">
          {changes.length === 0 ? (
            <div className="py-8 text-center text-sm text-gray-500 dark:text-gray-400">
              No changes
            </div>
          ) : (
            <ul className="m-0 flex list-none flex-col gap-1 p-0">
              {changes.slice(0, CHANGES_LIMIT).map((change) => {
                const asset = change.assetId
                  ? { siteId: change.siteId, siteName: change.siteName, id: change.assetId }
                  : undefined;
                const health = changeHealth(change);
                return (
                  <li
                    key={`${change.siteId}|${change.id}`}
                    className={`-mx-2 grid grid-cols-[72px_minmax(0,1fr)_auto] items-start gap-3 rounded-lg px-2 py-1.5 text-sm ${asset ? 'cursor-pointer hover:bg-gray-50 dark:hover:bg-gray-700/40' : ''}`}
                    onClick={() => asset && onOpen(asset)}
                  >
                    <span
                      className="inline-flex items-center justify-center gap-1 rounded-full px-2 py-0.5 text-[11px] font-medium whitespace-nowrap"
                      style={{
                        color: HEALTH_COLOR[health],
                        background: `${HEALTH_COLOR[health]}1f`,
                      }}
                    >
                      {CHANGE_TAG[change.change]}
                    </span>
                    <span className="min-w-0">
                      <b className="font-mono text-[13px] font-semibold">{change.assetName}</b>{' '}
                      <span className="text-gray-500 dark:text-gray-400">
                        {KIND_LABEL[change.kind].toLowerCase()} · {describeChange(change)}
                      </span>
                    </span>
                    <span className="text-xs text-gray-500 tabular-nums dark:text-gray-400">
                      {since(change.at)}
                    </span>
                  </li>
                );
              })}
            </ul>
          )}
        </Panel>
        <Panel
          title="Top consumers"
          subtitle="Pods, nodes and hosts, now"
          extra={
            <Segmented
              size="small"
              value={metric}
              onChange={(value) => setMetric(value as 'cpu' | 'memory')}
              options={[
                { value: 'cpu', label: 'CPU' },
                { value: 'memory', label: 'Memory' },
              ]}
            />
          }
        >
          {top.length === 0 ? (
            <div className="py-8 text-center text-sm text-gray-500 dark:text-gray-400">
              {topAssets.loading ? (
                <Spin indicator={<LoadingOutlined spin />} />
              ) : (
                `No ${metric === 'cpu' ? 'CPU' : 'memory'} data yet`
              )}
            </div>
          ) : (
            <ul className="m-0 flex list-none flex-col gap-3 p-0">
              {top.map(({ asset, value }) => (
                <li key={assetUid(asset)}>
                  <button
                    type="button"
                    onClick={() => onOpen(asset)}
                    className="flex w-full cursor-pointer flex-col gap-1.5 border-0 bg-transparent p-0 text-left"
                  >
                    <span className="flex w-full items-center gap-2 text-sm">
                      <KindIcon asset={asset} size={16} />
                      <span className="min-w-0 flex-1 truncate font-mono text-[13px] text-gray-800 dark:text-gray-100">
                        {asset.name}
                      </span>
                      <strong className="font-semibold text-gray-900 tabular-nums dark:text-gray-50">
                        {Math.round(value)}%
                      </strong>
                    </span>
                    <span className="block h-1.5 w-full overflow-hidden rounded-full bg-[var(--v2-track)]">
                      <span
                        className="block h-full rounded-full"
                        style={{
                          width: `${Math.min(100, Math.max(2, value))}%`,
                          background: value >= 90 ? HEALTH_COLOR.warning : 'var(--v2-accent)',
                        }}
                      />
                    </span>
                  </button>
                </li>
              ))}
            </ul>
          )}
        </Panel>
      </div>

      <div className="grid grid-cols-1 gap-4 xl:grid-cols-2">
        <Panel title={multiSite ? 'By Site' : 'By project'} subtitle="Live assets, split by health">
          {groupNames.length === 0 ? (
            <div className="py-8 text-center text-sm text-gray-500 dark:text-gray-400">
              No live assets
            </div>
          ) : (
            <div className="flex flex-col gap-3">
              {groupNames.map((name) => {
                const row = groups.get(name)!;
                const total = healthTotal(row);
                return (
                  <div key={name} className="flex flex-col gap-1.5">
                    <span className="flex justify-between gap-3 text-sm">
                      <span className="truncate text-gray-800 dark:text-gray-100">
                        {name || '—'}
                      </span>
                      <span className="text-gray-500 tabular-nums dark:text-gray-400">
                        {total} assets
                      </span>
                    </span>
                    <HealthBar row={row} total={total} height={8} withTitles />
                  </div>
                );
              })}
              <span className="flex flex-wrap gap-3.5 text-xs text-gray-500 dark:text-gray-400">
                {HEALTHS.map((health) => (
                  <span key={health} className="inline-flex items-center gap-1.5">
                    <span
                      className="inline-block h-2.5 w-2.5 rounded-[3px]"
                      style={{ background: HEALTH_COLOR[health] }}
                    />
                    {HEALTH_LABEL[health]}
                  </span>
                ))}
              </span>
            </div>
          )}
        </Panel>
        <Panel
          title="By cloud and region"
          subtitle="From cloud_provider / cloud_region on the agents' data"
        >
          {regions.length === 0 ? (
            <div className="py-8 text-center text-sm text-gray-500 dark:text-gray-400">
              No cloud attributes reported
            </div>
          ) : (
            <div className="overflow-x-auto">
              <table className="w-full text-sm">
                <thead>
                  <tr className="text-left text-xs tracking-wide text-gray-500 dark:text-gray-400">
                    <th className="border-b border-gray-200 py-2 font-medium dark:border-gray-700">
                      Cloud · region
                    </th>
                    <th className="border-b border-gray-200 py-2 font-medium dark:border-gray-700">
                      {multiSite ? 'Sites' : 'Projects'}
                    </th>
                    <th className="border-b border-gray-200 py-2 text-right font-medium dark:border-gray-700">
                      Assets
                    </th>
                    <th className="border-b border-gray-200 py-2 text-right font-medium dark:border-gray-700">
                      Warning
                    </th>
                    <th className="border-b border-gray-200 py-2 text-right font-medium dark:border-gray-700">
                      Critical
                    </th>
                  </tr>
                </thead>
                <tbody>
                  {regions.map((row) => (
                    <tr
                      key={row.key}
                      className="border-b border-gray-100 last:border-b-0 dark:border-gray-700"
                    >
                      <td className="py-2.5 font-mono text-xs">{row.key}</td>
                      <td className="py-2.5 text-gray-500 dark:text-gray-400">
                        {row.where.join(', ')}
                      </td>
                      <td className="py-2.5 text-right tabular-nums">{row.assets}</td>
                      <td
                        className="py-2.5 text-right tabular-nums"
                        style={row.warning ? { color: HEALTH_COLOR.warning } : undefined}
                      >
                        {row.warning || '—'}
                      </td>
                      <td
                        className="py-2.5 text-right tabular-nums"
                        style={row.critical ? { color: HEALTH_COLOR.critical } : undefined}
                      >
                        {row.critical || '—'}
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          )}
        </Panel>
      </div>
    </div>
  );
};

/** One bar split by health, in the problems-first order of HEALTHS. */
const HealthBar = ({
  row,
  total,
  height = 6,
  withTitles = false,
}: {
  row?: HealthCounts;
  total: number;
  height?: number;
  withTitles?: boolean;
}) => (
  <span
    className="flex w-full gap-0.5 overflow-hidden rounded-full bg-[var(--v2-track)]"
    style={{ height }}
  >
    {row &&
      total > 0 &&
      HEALTHS.map((health) =>
        row[health] > 0 ? (
          <Tooltip
            key={health}
            title={withTitles ? `${HEALTH_LABEL[health]}: ${row[health]}` : undefined}
          >
            <i
              className="block h-full"
              style={{
                width: `${(row[health] / total) * 100}%`,
                background: HEALTH_COLOR[health],
              }}
            />
          </Tooltip>
        ) : null,
      )}
  </span>
);

const CHANGE_TAG: Record<SiteChange['change'], string> = {
  new: 'New',
  state: 'State',
  health: 'Health',
};

const Count = ({ health, value }: { health: AssetHealth; value: number }) => (
  <span className="inline-flex items-center gap-1">
    <HealthDot health={health} size={7} />
    {value} {HEALTH_LABEL[health].toLowerCase()}
  </span>
);

const changeHealth = (change: SiteChange): AssetHealth => {
  if (change.change === 'health') return (change.to as AssetHealth) || 'unknown';
  if (change.change === 'new') return 'healthy';
  if (change.to === 'terminated') return 'inactive';
  if (change.to === 'not_reporting') return 'critical';
  return 'unknown';
};

const describeChange = (change: SiteChange) => {
  switch (change.change) {
    case 'new':
      return `new${change.detail ? ` ${change.detail}` : ''}`;
    case 'state':
      return `${STATE_LABEL[change.from as keyof typeof STATE_LABEL] ?? change.from} → ${STATE_LABEL[change.to as keyof typeof STATE_LABEL] ?? change.to}`;
    case 'health':
      return `${HEALTH_LABEL[change.to as AssetHealth] ?? change.to}${change.detail ? `: ${change.detail}` : ''}`;
  }
};
