import {
  AppstoreOutlined,
  BranchesOutlined,
  FileSearchOutlined,
  LoadingOutlined,
} from '@ant-design/icons';
import { Alert, Drawer, Segmented, Spin } from 'antd';
import type { EChartsOption } from 'echarts';
import { type ReactNode, useCallback, useEffect, useMemo, useState } from 'react';

import {
  type AssetDetail,
  type AssetRef,
  type SiteAsset,
  inventoryApi,
} from '../../../entity/inventory';
import type { PromQLSeries } from '../../../entity/synthetics';
import {
  type ChartColors,
  EChart,
  IconAction,
  Panel,
  baseOption,
  timeAxis,
  valueAxis,
} from '../../synthetics';
import {
  HEALTH_COLOR,
  HEALTH_LABEL,
  KIND_LABEL,
  STATE_LABEL,
  kindLabel,
  logsSearchFor,
  since,
} from '../lib/assets';
import { type ChartQuery, chartQueries } from '../lib/charts';
import { HealthBadge, HealthDot, KindIcon, LifecycleTag } from './parts';

export interface NavigateTarget {
  siteId: string;
  projectId: string;
  tab: 'search' | 'traces' | 'dashboards';
  logsSearch?: string;
  serviceName?: string;
}

interface Props {
  siteId: string;
  siteName: string;
  assetId: string;
  multiSite: boolean;
  onClose: () => void;
  onOpenAsset: (siteId: string, siteName: string, assetId: string) => void;
  onNavigate: (target: NavigateTarget) => void;
}

type RangeKey = '1h' | '6h' | '24h' | '7d';
const RANGE_SECONDS: Record<RangeKey, number> = {
  '1h': 3600,
  '6h': 21600,
  '24h': 86400,
  '7d': 604800,
};

export const AssetDetailDrawer = ({
  siteId,
  siteName,
  assetId,
  multiSite,
  onClose,
  onOpenAsset,
  onNavigate,
}: Props) => {
  const [detail, setDetail] = useState<AssetDetail>();
  const [error, setError] = useState<string>();
  const [range, setRange] = useState<RangeKey>('24h');
  const [series, setSeries] = useState<Record<string, PromQLSeries[]>>({});
  const [chartWindow, setChartWindow] = useState<{ from: number; to: number }>({ from: 0, to: 0 });

  useEffect(() => {
    const controller = new AbortController();
    setDetail(undefined);
    setError(undefined);
    inventoryApi
      .asset(siteId, assetId, controller.signal)
      .then(setDetail)
      .catch((reason) => !controller.signal.aborted && setError((reason as Error).message));
    return () => controller.abort();
  }, [siteId, assetId]);

  const asset: SiteAsset | undefined = detail && { ...detail.asset, siteId, siteName };
  const queries = useMemo(() => (asset ? chartQueries(asset) : []), [detail]);

  useEffect(() => {
    if (!asset || queries.length === 0) return;
    const controller = new AbortController();
    const to = Date.now();
    const from = to - RANGE_SECONDS[range] * 1000;
    const step = Math.max(60, Math.round(RANGE_SECONDS[range] / 120));
    setChartWindow({ from, to });
    setSeries({});
    for (const chart of queries) {
      for (const [index, line] of chart.lines.entries()) {
        inventoryApi
          .promql(
            siteId,
            asset.projectId,
            {
              query: line.query,
              start: new Date(from).toISOString(),
              end: new Date(to).toISOString(),
              stepSeconds: step,
            },
            controller.signal,
          )
          .then((response) =>
            setSeries((current) => ({ ...current, [`${chart.key}:${index}`]: response.series })),
          )
          .catch(() => undefined);
      }
    }
    return () => controller.abort();
  }, [queries, range]);

  const logsSearch = asset ? logsSearchFor(asset) : undefined;
  const serviceName =
    asset?.kind === 'service' ? asset.name : asset?.attributes.service_name || undefined;

  return (
    <Drawer
      open
      onClose={onClose}
      size={Math.min(1180, Math.round(window.innerWidth * 0.94))}
      destroyOnHidden
      title={
        asset ? (
          <div className="flex flex-wrap items-center gap-3">
            <HealthDot health={asset.health} size={14} />
            <span className="text-lg font-semibold">{asset.name}</span>
            <span className="inline-flex items-center gap-1.5 text-xs font-normal text-gray-500 dark:text-gray-400">
              <KindIcon asset={asset} />
              {kindLabel(asset)}
            </span>
            <LifecycleTag state={asset.state} />
            <span className="font-mono text-xs font-normal text-gray-500 dark:text-gray-400">
              {[multiSite ? siteName : '', asset.projectName, asset.cluster, asset.namespace]
                .filter(Boolean)
                .join(' / ')}
            </span>
          </div>
        ) : (
          'Asset'
        )
      }
      extra={
        asset && (
          <span className="inline-flex items-center gap-1">
            {logsSearch && (
              <IconAction
                title="Open logs"
                icon={<FileSearchOutlined />}
                onClick={() =>
                  onNavigate({ siteId, projectId: asset.projectId, tab: 'search', logsSearch })
                }
              />
            )}
            {serviceName && (
              <IconAction
                title="Open traces"
                icon={<BranchesOutlined />}
                onClick={() =>
                  onNavigate({ siteId, projectId: asset.projectId, tab: 'traces', serviceName })
                }
              />
            )}
            <IconAction
              title="Open dashboards"
              icon={<AppstoreOutlined />}
              onClick={() => onNavigate({ siteId, projectId: asset.projectId, tab: 'dashboards' })}
            />
          </span>
        )
      }
    >
      {error && <Alert type="error" showIcon title={error} />}
      {!detail && !error && (
        <div className="flex justify-center py-16">
          <Spin indicator={<LoadingOutlined spin />} />
        </div>
      )}
      {detail && asset && (
        <div className="flex flex-col gap-4">
          <Stats asset={asset} />

          <div className="grid grid-cols-1 gap-4 lg:grid-cols-2">
            <Panel title="Health" subtitle="Why it has this colour">
              {asset.reasons.length === 0 ? (
                <div className="text-sm text-gray-500 dark:text-gray-400">Every check passes.</div>
              ) : (
                <ul className="m-0 flex list-none flex-col gap-1.5 p-0">
                  {asset.reasons.map((reason, index) => (
                    <li
                      key={index}
                      className="flex items-baseline gap-2 rounded-md bg-gray-50 px-2.5 py-2 text-sm dark:bg-gray-700/40"
                    >
                      <HealthDot health={reason.health} size={8} />
                      {reason.text}
                    </li>
                  ))}
                </ul>
              )}
            </Panel>
            <Panel title="Relationships" subtitle="Parents and what runs on or under it">
              <div className="mb-2.5 flex flex-wrap items-center gap-1 text-sm text-gray-500 dark:text-gray-400">
                {detail.parents.length === 0 && <span>Top level</span>}
                {detail.parents.map((parent, index) => (
                  <span key={parent.id} className="inline-flex items-center gap-1">
                    {index > 0 && <span>›</span>}
                    <RefLink
                      item={parent}
                      onClick={() => onOpenAsset(siteId, siteName, parent.id)}
                    />
                  </span>
                ))}
              </div>
              {detail.node && (
                <div className="mb-2.5 text-sm text-gray-500 dark:text-gray-400">
                  Runs on{' '}
                  <RefLink
                    item={detail.node}
                    onClick={() => onOpenAsset(siteId, siteName, detail.node!.id)}
                  />
                </div>
              )}
              <div className="flex flex-wrap gap-1.5">
                {detail.children.length === 0 && (
                  <span className="text-xs text-gray-500">Nothing runs under it</span>
                )}
                {detail.children.slice(0, 40).map((child) => (
                  <button
                    key={child.id}
                    type="button"
                    onClick={() => onOpenAsset(siteId, siteName, child.id)}
                    className="inline-flex cursor-pointer items-center gap-1.5 rounded-md border border-gray-200 bg-white px-2 py-0.5 text-xs hover:border-emerald-500 dark:border-gray-600 dark:bg-gray-800"
                  >
                    <HealthDot health={child.health} size={7} />
                    {child.name}
                  </button>
                ))}
                {detail.children.length > 40 && (
                  <span className="text-xs text-gray-500">+{detail.children.length - 40} more</span>
                )}
              </div>
            </Panel>
          </div>

          {queries.length > 0 && (
            <div>
              <div className="mb-2 flex justify-end">
                <Segmented<RangeKey>
                  size="small"
                  value={range}
                  onChange={setRange}
                  options={['1h', '6h', '24h', '7d']}
                />
              </div>
              <div className="grid grid-cols-1 gap-4 lg:grid-cols-3">
                {queries.map((chart) => (
                  <Panel key={chart.key} title={chart.title} subtitle={chart.subtitle}>
                    <LineChart chart={chart} series={series} window={chartWindow} />
                  </Panel>
                ))}
              </div>
            </div>
          )}

          <div className="grid grid-cols-1 gap-4 lg:grid-cols-3">
            <Panel title="Lifecycle" subtitle="From the agents' data and the change feed">
              <ul className="m-0 flex list-none flex-col gap-2.5 p-0 text-sm">
                <TimelineItem at={asset.lastSeen} health={asset.health} text="Last seen" />
                {detail.changes.map((change) => (
                  <TimelineItem
                    key={change.id}
                    at={change.at}
                    health={
                      change.change === 'health'
                        ? ((change.to as keyof typeof HEALTH_LABEL) ?? 'unknown')
                        : 'unknown'
                    }
                    text={
                      change.change === 'new'
                        ? 'Appeared'
                        : change.change === 'state'
                          ? `${STATE_LABEL[change.from as keyof typeof STATE_LABEL] ?? change.from} → ${STATE_LABEL[change.to as keyof typeof STATE_LABEL] ?? change.to}`
                          : `${HEALTH_LABEL[change.to as keyof typeof HEALTH_LABEL] ?? change.to}${change.detail ? `: ${change.detail}` : ''}`
                    }
                  />
                ))}
                <TimelineItem at={asset.firstSeen} health="healthy" text="First seen" />
              </ul>
            </Panel>
            <Panel title="Properties" subtitle="Resource attributes as reported">
              <dl className="m-0 grid grid-cols-[minmax(120px,auto)_minmax(0,1fr)] gap-x-3.5 gap-y-1 text-xs">
                {Object.entries(asset.attributes)
                  .sort(([a], [b]) => a.localeCompare(b))
                  .map(([key, value]) => (
                    <Attribute key={key} name={key} value={value} />
                  ))}
              </dl>
            </Panel>
            <Panel title="Signals" subtitle="Where Obzly sees it">
              <dl className="m-0 grid grid-cols-[minmax(100px,auto)_minmax(0,1fr)] gap-x-3.5 gap-y-1 text-xs">
                {multiSite && <Attribute name="Site" value={siteName} />}
                <Attribute name="Project" value={asset.projectName} />
                <Attribute name="Kind" value={KIND_LABEL[asset.kind]} />
                <Attribute name="Sources" value={asset.sources.join(', ') || '—'} />
                {asset.metrics.phase && <Attribute name="Pod phase" value={asset.metrics.phase} />}
                {asset.metrics.desired !== undefined && (
                  <Attribute
                    name="Replicas"
                    value={`${asset.metrics.available ?? 0} of ${asset.metrics.desired} available`}
                  />
                )}
                {asset.metrics.ready !== undefined && (
                  <Attribute name="Ready" value={asset.metrics.ready ? 'Yes' : 'No'} />
                )}
              </dl>
            </Panel>
          </div>
        </div>
      )}
    </Drawer>
  );
};

const Stats = ({ asset }: { asset: SiteAsset }) => {
  const m = asset.metrics;
  const percent = (value?: number, basis?: string) =>
    value === undefined ? '—' : `${Math.round(value)}%${basis === 'limit' ? ' of limit' : ''}`;
  const items: [string, ReactNode][] = [
    ['Health', <HealthBadge key="h" health={asset.health} />],
    ['CPU', percent(m.cpuPercent, m.cpuBasis)],
    ['Memory', percent(m.memoryPercent, m.memoryBasis)],
    ['Disk', percent(m.diskPercent)],
    ['Restarts 24h', m.restarts24h ?? '—'],
    ['Age', since(asset.firstSeen)],
    ['Last seen', since(asset.lastSeen)],
  ];
  return (
    <div className="grid grid-cols-2 rounded-lg border border-gray-200 bg-white sm:grid-cols-4 lg:grid-cols-7 dark:border-gray-700 dark:bg-gray-800">
      {items.map(([label, value]) => (
        <div
          key={label}
          className="border-gray-200 px-3.5 py-2.5 lg:border-r lg:last:border-r-0 dark:border-gray-700"
        >
          <div className="text-[11px] tracking-wider text-gray-500 uppercase dark:text-gray-400">
            {label}
          </div>
          <div className="text-lg font-semibold tabular-nums">{value}</div>
        </div>
      ))}
    </div>
  );
};

const RefLink = ({ item, onClick }: { item: AssetRef; onClick: () => void }) => (
  <button
    type="button"
    onClick={onClick}
    className="inline-flex cursor-pointer items-center gap-1 border-b border-dashed border-gray-300 text-gray-900 hover:border-emerald-500 hover:text-emerald-600 dark:border-gray-600 dark:text-gray-100"
  >
    <KindIcon asset={{ kind: item.kind, subtype: item.subtype, attributes: {} }} size={14} />
    {item.name}
  </button>
);

const TimelineItem = ({
  at,
  health,
  text,
}: {
  at: string;
  health: keyof typeof HEALTH_COLOR;
  text: string;
}) => (
  <li className="grid grid-cols-[80px_12px_minmax(0,1fr)] items-start gap-2.5">
    <span
      className="text-right text-xs text-gray-500 tabular-nums dark:text-gray-400"
      title={new Date(at).toLocaleString()}
    >
      {since(at)} ago
    </span>
    <span className="pt-1">
      <HealthDot health={health} size={8} />
    </span>
    <span>{text}</span>
  </li>
);

const Attribute = ({ name, value }: { name: string; value: string }) => (
  <>
    <dt className="font-mono text-gray-500 dark:text-gray-400">{name}</dt>
    <dd className="m-0 font-mono break-all">{value}</dd>
  </>
);

const LINE_COLORS = ['#059669', '#3b82f6', '#f59e0b', '#a855f7'];

const LineChart = ({
  chart,
  series,
  window,
}: {
  chart: ChartQuery;
  series: Record<string, PromQLSeries[]>;
  window: { from: number; to: number };
}) => {
  const option = useCallback(
    (colors: ChartColors): EChartsOption => ({
      ...baseOption(colors),
      xAxis: timeAxis(colors, window.from, window.to),
      yAxis: valueAxis(colors, chart.format),
      series: chart.lines.flatMap((line, index) =>
        (series[`${chart.key}:${index}`] ?? []).map((s, n) => ({
          name: line.legend(s.labels),
          type: 'line' as const,
          showSymbol: false,
          smooth: true,
          lineStyle: { width: 1.6 },
          color: LINE_COLORS[(index + n) % LINE_COLORS.length],
          data: s.points.map((point) => [new Date(point.timestamp).getTime(), point.value]),
        })),
      ),
    }),
    [chart, series, window],
  );
  return <EChart option={option} height={220} />;
};
