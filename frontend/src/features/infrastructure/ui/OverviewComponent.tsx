import { Segmented, Tooltip } from 'antd';
import { useMemo, useState } from 'react';

import type { AssetHealth, AssetKind, SiteAsset, SiteChange } from '../../../entity/inventory';
import { absoluteChange } from '../../../shared/lib/periodChange';
import { Donut, KpiTile } from '../../../shared/ui/v2';
import { Panel } from '../../synthetics';
import {
  HEALTH_COLOR,
  HEALTH_LABEL,
  HEALTH_ORDER,
  KIND_LABEL,
  STATE_LABEL,
  assetUid,
  kindLabel,
  since,
} from '../lib/assets';
import type { KindHistory } from '../lib/history';
import { HealthDot, KindIcon } from './parts';

interface Props {
  assets: SiteAsset[];
  changes: SiteChange[];
  /** Kind counts over time for the tiles' sparklines and changes vs 24 h ago. */
  history?: KindHistory;
  multiSite: boolean;
  onOpen: (asset: SiteAsset) => void;
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

export const OverviewComponent = ({
  assets,
  changes,
  history,
  multiSite,
  onOpen,
  onOpenKind,
}: Props) => {
  const [metric, setMetric] = useState<'cpu' | 'memory'>('cpu');

  const live = useMemo(() => assets.filter((asset) => asset.state !== 'terminated'), [assets]);
  // Hourly counts over 7 days; a sparkline needs two points.
  const trendOf = (kind: AssetKind) => {
    const values = history?.trend[kind];
    return values && values.length > 1 ? values : undefined;
  };
  const byUid = useMemo(() => new Map(assets.map((asset) => [assetUid(asset), asset])), [assets]);

  const counts = useMemo(() => {
    const result = new Map<AssetKind, Record<AssetHealth, number>>();
    for (const asset of assets) {
      const row =
        result.get(asset.kind) ??
        ({ critical: 0, warning: 0, healthy: 0, unknown: 0, inactive: 0 } as Record<
          AssetHealth,
          number
        >);
      row[asset.health] += 1;
      result.set(asset.kind, row);
    }
    return result;
  }, [assets]);

  const attention = useMemo(
    () =>
      live
        .filter((asset) => asset.health === 'critical' || asset.health === 'warning')
        .sort(
          (a, b) =>
            HEALTH_ORDER[a.health] - HEALTH_ORDER[b.health] ||
            // Leaves first: a pod's reason, not the namespace repeating it.
            leafRank(a.kind) - leafRank(b.kind) ||
            a.name.localeCompare(b.name),
        ),
    [live],
  );

  const healthTotals = useMemo(
    () =>
      HEALTHS.filter((health) => health !== 'inactive').map((health) => ({
        health,
        value: live.filter((asset) => asset.health === health).length,
      })),
    [live],
  );

  const top = useMemo(() => {
    const value = (asset: SiteAsset) =>
      metric === 'cpu' ? asset.metrics.cpuPercent : asset.metrics.memoryPercent;
    return live
      .filter((asset) => ['pod', 'node', 'host'].includes(asset.kind) && value(asset) !== undefined)
      .map((asset) => ({ asset, value: value(asset) ?? 0 }))
      .sort((a, b) => b.value - a.value)
      .slice(0, 10);
  }, [live, metric]);

  const groups = useMemo(() => {
    const result = new Map<string, Record<AssetHealth, number>>();
    for (const asset of live) {
      const key = multiSite ? asset.siteName : asset.projectName;
      const row = result.get(key) ?? {
        critical: 0,
        warning: 0,
        healthy: 0,
        unknown: 0,
        inactive: 0,
      };
      row[asset.health] += 1;
      result.set(key, row);
    }
    return result;
  }, [live, multiSite]);

  const regions = useMemo(() => {
    const result = new Map<
      string,
      { assets: number; warning: number; critical: number; sites: Set<string> }
    >();
    for (const asset of live) {
      const region = asset.attributes.cloud_region;
      if (!region || asset.kind === 'container' || asset.kind === 'pod') continue;
      const key = `${asset.attributes.cloud_provider || 'cloud'} · ${region}`;
      const row = result.get(key) ?? {
        assets: 0,
        warning: 0,
        critical: 0,
        sites: new Set<string>(),
      };
      row.assets += 1;
      if (asset.health === 'warning') row.warning += 1;
      if (asset.health === 'critical') row.critical += 1;
      row.sites.add(multiSite ? asset.siteName : asset.projectName);
      result.set(key, row);
    }
    return [...result.entries()].sort((a, b) => b[1].assets - a[1].assets);
  }, [live, multiSite]);

  const groupNames = [...groups.keys()].sort();

  return (
    <div className="flex flex-col gap-4">
      <section
        aria-label="Kinds"
        className="grid grid-cols-[repeat(auto-fit,minmax(170px,1fr))] gap-4"
      >
        {CARDS.map((card) => {
          const row = counts.get(card.kind);
          const total = row ? HEALTHS.reduce((sum, health) => sum + row[health], 0) : 0;
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
              label={`Health of ${live.length} live assets`}
              size={150}
              center={live.length}
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
          {attention.length === 0 ? (
            <div className="py-8 text-center text-sm text-gray-500 dark:text-gray-400">
              Nothing needs attention
            </div>
          ) : (
            <ul className="m-0 flex list-none flex-col p-0">
              {attention.slice(0, ATTENTION_LIMIT).map((asset) => (
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
              {attention.length > ATTENTION_LIMIT && (
                <li className="pt-2 text-xs text-gray-500 dark:text-gray-400">
                  and {attention.length - ATTENTION_LIMIT} more in Assets
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
                const asset = byUid.get(
                  assetUid({
                    siteId: change.siteId,
                    projectId: change.projectId,
                    key: change.assetKey,
                  }),
                );
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
              No {metric === 'cpu' ? 'CPU' : 'memory'} data yet
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
                const total = HEALTHS.reduce((sum, health) => sum + row[health], 0);
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
                  {regions.map(([key, row]) => (
                    <tr
                      key={key}
                      className="border-b border-gray-100 last:border-b-0 dark:border-gray-700"
                    >
                      <td className="py-2.5 font-mono text-xs">{key}</td>
                      <td className="py-2.5 text-gray-500 dark:text-gray-400">
                        {[...row.sites].join(', ')}
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
  row?: Record<AssetHealth, number>;
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

const leafRank = (kind: AssetKind) =>
  ({ pod: 0, container: 1, node: 2, host: 3, docker_container: 4, workload: 5, service: 6 })[
    kind as string
  ] ?? 9;

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
