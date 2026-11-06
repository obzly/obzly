import { Segmented } from 'antd';
import { useCallback, useMemo, useState } from 'react';

import type { AssetHealth, AssetKind, SiteAsset, SiteChange } from '../../../entity/inventory';
import { type ChartColors, EChart, Panel, baseOption } from '../../synthetics';
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
import { HealthDot, KindIcon } from './parts';

interface Props {
  assets: SiteAsset[];
  changes: SiteChange[];
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

export const OverviewComponent = ({ assets, changes, multiSite, onOpen, onOpenKind }: Props) => {
  const [metric, setMetric] = useState<'cpu' | 'memory'>('cpu');

  const live = useMemo(() => assets.filter((asset) => asset.state !== 'terminated'), [assets]);
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

  const healthDonut = useCallback(
    (colors: ChartColors) => {
      const totals = HEALTHS.filter((health) => health !== 'inactive').map((health) => ({
        name: HEALTH_LABEL[health],
        value: live.filter((asset) => asset.health === health).length,
        itemStyle: { color: HEALTH_COLOR[health] },
      }));
      return {
        ...baseOption(colors),
        tooltip: { trigger: 'item' as const },
        legend: { show: false },
        title: {
          text: String(live.length),
          subtext: 'live assets',
          left: 'center',
          top: '40%',
          textStyle: { color: colors.text, fontSize: 24, fontWeight: 600 },
          subtextStyle: { color: colors.text },
        },
        series: [
          {
            type: 'pie' as const,
            radius: ['58%', '78%'],
            label: { show: false },
            data: totals.filter((item) => item.value > 0),
          },
        ],
      };
    },
    [live],
  );

  const topConsumers = useCallback(
    (colors: ChartColors) => {
      const value = (asset: SiteAsset) =>
        metric === 'cpu' ? asset.metrics.cpuPercent : asset.metrics.memoryPercent;
      const top = live
        .filter(
          (asset) => ['pod', 'node', 'host'].includes(asset.kind) && value(asset) !== undefined,
        )
        .sort((a, b) => (value(b) ?? 0) - (value(a) ?? 0))
        .slice(0, 10)
        .reverse();
      return {
        ...baseOption(colors),
        grid: { left: 8, right: 40, top: 8, bottom: 8, containLabel: true },
        tooltip: { trigger: 'axis' as const, axisPointer: { type: 'shadow' as const } },
        legend: { show: false },
        xAxis: {
          type: 'value' as const,
          max: 100,
          axisLabel: { color: colors.text, formatter: '{value}%' },
          splitLine: { lineStyle: { color: colors.split } },
        },
        yAxis: {
          type: 'category' as const,
          data: top.map((asset) => shortName(asset.name)),
          axisLabel: { color: colors.text, fontSize: 11 },
        },
        series: [
          {
            type: 'bar' as const,
            data: top.map((asset) => ({
              value: value(asset),
              itemStyle: {
                color: (value(asset) ?? 0) >= 90 ? HEALTH_COLOR.warning : '#059669',
              },
            })),
            label: {
              show: true,
              position: 'right' as const,
              color: colors.text,
              formatter: '{c}%',
            },
          },
        ],
      };
    },
    [live, metric],
  );

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

  const groupChart = useCallback(
    (colors: ChartColors) => {
      const names = [...groups.keys()].sort();
      return {
        ...baseOption(colors),
        grid: { left: 8, right: 16, top: 28, bottom: 8, containLabel: true },
        tooltip: { trigger: 'axis' as const, axisPointer: { type: 'shadow' as const } },
        xAxis: {
          type: 'value' as const,
          axisLabel: { color: colors.text },
          splitLine: { lineStyle: { color: colors.split } },
        },
        yAxis: {
          type: 'category' as const,
          data: names,
          axisLabel: { color: colors.text, fontSize: 11 },
        },
        series: HEALTHS.filter((health) => health !== 'inactive').map((health) => ({
          name: HEALTH_LABEL[health],
          type: 'bar' as const,
          stack: 'health',
          itemStyle: { color: HEALTH_COLOR[health] },
          data: names.map((name) => groups.get(name)?.[health] ?? 0),
        })),
      };
    },
    [groups],
  );

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

  return (
    <div className="flex flex-col gap-4">
      <div className="grid grid-cols-2 gap-4 md:grid-cols-3 xl:grid-cols-6">
        {CARDS.map((card) => {
          const row = counts.get(card.kind);
          const total = row ? HEALTHS.reduce((sum, health) => sum + row[health], 0) : 0;
          return (
            <button
              key={card.kind}
              type="button"
              onClick={() => onOpenKind(card.kind)}
              className="flex min-w-0 cursor-pointer flex-col gap-2 rounded-lg border border-gray-200 bg-white p-4 text-left hover:border-emerald-500 dark:border-gray-700 dark:bg-gray-800"
            >
              <span className="flex items-center justify-between text-xs tracking-wider text-gray-500 uppercase dark:text-gray-400">
                {card.label}
                <KindIcon
                  asset={{ kind: card.kind, subtype: 'Deployment', attributes: {} }}
                  size={20}
                />
              </span>
              <span className="text-2xl font-semibold text-gray-900 tabular-nums dark:text-gray-100">
                {total}
              </span>
              <span className="flex h-1.5 overflow-hidden rounded bg-gray-200 dark:bg-gray-700">
                {row &&
                  HEALTHS.map((health) =>
                    row[health] > 0 ? (
                      <i
                        key={health}
                        className="block h-full"
                        style={{
                          width: `${(row[health] / total) * 100}%`,
                          background: HEALTH_COLOR[health],
                        }}
                      />
                    ) : null,
                  )}
              </span>
              <span className="flex flex-wrap gap-2 text-[11px] text-gray-500 dark:text-gray-400">
                {row && row.critical > 0 && <Count health="critical" value={row.critical} />}
                {row && row.warning > 0 && <Count health="warning" value={row.warning} />}
                {row && row.inactive > 0 && <Count health="inactive" value={row.inactive} />}
                {(!row || row.critical + row.warning + row.inactive === 0) && 'All healthy'}
              </span>
            </button>
          );
        })}
      </div>

      <div className="grid grid-cols-1 gap-4 xl:grid-cols-3">
        <Panel title="Health" subtitle="All assets that are not terminated">
          <EChart option={healthDonut} height={240} />
        </Panel>
        <Panel
          className="xl:col-span-2"
          title="Needs attention"
          subtitle="Critical first, with the reason. Click to open."
        >
          {attention.length === 0 ? (
            <div className="py-8 text-center text-sm text-gray-500">Nothing needs attention</div>
          ) : (
            <ul className="m-0 flex list-none flex-col p-0">
              {attention.slice(0, ATTENTION_LIMIT).map((asset) => (
                <li
                  key={assetUid(asset)}
                  onClick={() => onOpen(asset)}
                  className="flex cursor-pointer items-start gap-3 border-b border-dashed border-gray-200 py-2 last:border-b-0 hover:bg-gray-50 dark:border-gray-700 dark:hover:bg-gray-700/40"
                >
                  <span className="pt-1.5">
                    <HealthDot health={asset.health} />
                  </span>
                  <span className="min-w-0 flex-1">
                    <span className="flex flex-wrap items-center gap-2">
                      <b className="truncate font-semibold">{asset.name}</b>
                      <span className="inline-flex items-center gap-1 text-xs text-gray-500 dark:text-gray-400">
                        <KindIcon asset={asset} size={14} />
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
                <li className="pt-2 text-xs text-gray-500">
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
            <div className="py-8 text-center text-sm text-gray-500">No changes</div>
          ) : (
            <ul className="m-0 flex list-none flex-col gap-2.5 p-0">
              {changes.slice(0, CHANGES_LIMIT).map((change) => {
                const asset = byUid.get(
                  assetUid({
                    siteId: change.siteId,
                    projectId: change.projectId,
                    key: change.assetKey,
                  }),
                );
                return (
                  <li
                    key={`${change.siteId}|${change.id}`}
                    className={`grid grid-cols-[64px_12px_minmax(0,1fr)] items-start gap-2.5 text-sm ${asset ? 'cursor-pointer' : ''}`}
                    onClick={() => asset && onOpen(asset)}
                  >
                    <span className="text-right text-xs text-gray-500 tabular-nums dark:text-gray-400">
                      {since(change.at)}
                    </span>
                    <span className="pt-1">
                      <HealthDot health={changeHealth(change)} />
                    </span>
                    <span className="min-w-0">
                      <b className="font-semibold">{change.assetName}</b>{' '}
                      <span className="text-gray-500 dark:text-gray-400">
                        {KIND_LABEL[change.kind].toLowerCase()} · {describeChange(change)}
                      </span>
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
          <EChart option={topConsumers} height={300} />
        </Panel>
      </div>

      <div className="grid grid-cols-1 gap-4 xl:grid-cols-2">
        <Panel title={multiSite ? 'By Site' : 'By project'} subtitle="Live assets, split by health">
          <EChart option={groupChart} height={Math.max(160, groups.size * 34 + 40)} />
        </Panel>
        <Panel
          title="By cloud and region"
          subtitle="From cloud_provider / cloud_region on the agents' data"
        >
          {regions.length === 0 ? (
            <div className="py-8 text-center text-sm text-gray-500">
              No cloud attributes reported
            </div>
          ) : (
            <div className="overflow-x-auto">
              <table className="w-full text-sm">
                <thead>
                  <tr className="text-left text-xs tracking-wide text-gray-500 uppercase dark:text-gray-400">
                    <th className="py-2 font-medium">Cloud · region</th>
                    <th className="py-2 font-medium">{multiSite ? 'Sites' : 'Projects'}</th>
                    <th className="py-2 text-right font-medium">Assets</th>
                    <th className="py-2 text-right font-medium">Warning</th>
                    <th className="py-2 text-right font-medium">Critical</th>
                  </tr>
                </thead>
                <tbody>
                  {regions.map(([key, row]) => (
                    <tr key={key} className="border-t border-gray-200 dark:border-gray-700">
                      <td className="py-2 font-mono text-xs">{key}</td>
                      <td className="py-2 text-gray-500 dark:text-gray-400">
                        {[...row.sites].join(', ')}
                      </td>
                      <td className="py-2 text-right tabular-nums">{row.assets}</td>
                      <td className="py-2 text-right tabular-nums">{row.warning || '—'}</td>
                      <td className="py-2 text-right tabular-nums">{row.critical || '—'}</td>
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

const shortName = (name: string) => (name.length > 28 ? `${name.slice(0, 26)}…` : name);

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
