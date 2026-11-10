import { LoadingOutlined } from '@ant-design/icons';
import { Button, Empty, Segmented, Spin, Table, Tooltip } from 'antd';
import dayjs from 'dayjs';
import { useCallback, useEffect, useMemo, useState } from 'react';

import {
  type CostAnomaly,
  type CostDimension,
  type ExploreResponse,
  costApi,
} from '../../../entity/cost';
import { type ChartColors, EChart, Panel, baseOption } from '../../synthetics';
import { valueLabel } from '../lib/explorer';
import { formatChange, formatMoney, formatMoneyCompact } from '../lib/format';
import {
  type CostSite,
  type MergedSummary,
  type SiteOutcome,
  type SiteSource,
  mergeExplore,
  mergeSummary,
} from '../lib/merge';
import { addDays, resolveRange } from '../lib/ranges';
import { fanOut } from '../lib/sites';
import { RankList, SiteErrors, SourceBadge, Tile } from './parts';

interface Props {
  sites: CostSite[];
  refresh: number;
  /** Opens the Explorer grouped by a dimension, filtered to a value when one is given. */
  onExplore: (groupBy: CostDimension, filter?: { dimension: CostDimension; value: string }) => void;
  onManageCloud?: () => void;
}

interface Data {
  summary: MergedSummary;
  outcomes: SiteOutcome<unknown>[];
  anomalies: (CostAnomaly & { siteName: string })[];
  services: ExploreResponse;
  accounts: ExploreResponse;
}

const TOP = 6;
const ANOMALY_COLOR = '#dc2626';

export const OverviewTab = ({ sites, refresh, onExplore, onManageCloud }: Props) => {
  const [data, setData] = useState<Data>();
  const [currency, setCurrency] = useState<string>();
  const [days, setDays] = useState<30 | 90>(30);

  useEffect(() => {
    const controller = new AbortController();
    const load = async () => {
      const summaries = await fanOut(sites, (siteId) => costApi.summary(siteId, controller.signal));
      const summary = mergeSummary(summaries);
      const month = resolveRange('mtd', summary.today);
      const top = (groupBy: CostDimension) =>
        fanOut(sites, (siteId) =>
          costApi.explore(
            siteId,
            { ...month, granularity: 'monthly', groupBy, metric: 'effective', filters: {} },
            controller.signal,
          ),
        );
      const [services, accounts, anomalies] = await Promise.all([
        top('service'),
        top('account'),
        fanOut(sites, (siteId) =>
          costApi.anomalies(siteId, addDays(summary.today, -89), summary.today, controller.signal),
        ),
      ]);
      if (controller.signal.aborted) return;
      setData({
        summary,
        outcomes: summaries,
        services: mergeExplore(services),
        accounts: mergeExplore(accounts),
        anomalies: anomalies.flatMap((outcome) =>
          (outcome.value ?? []).map((anomaly) => ({ ...anomaly, siteName: outcome.site.siteName })),
        ),
      });
    };
    void load();
    return () => controller.abort();
  }, [sites, refresh]);

  const current = useMemo(
    () =>
      data?.summary.currencies.find((summary) => summary.currency === currency) ??
      data?.summary.currencies[0],
    [data, currency],
  );
  const money = useCallback(
    (amount: number) => formatMoney(amount, current?.currency ?? 'USD'),
    [current],
  );

  const trend = useCallback(
    (colors: ChartColors) => {
      const points = (current?.daily ?? []).slice(-days);
      const flagged = new Map(
        (data?.anomalies ?? [])
          .filter((anomaly) => anomaly.currency === current?.currency)
          .map((anomaly) => [anomaly.day, anomaly]),
      );
      return {
        ...baseOption(colors),
        grid: { left: 8, right: 16, top: 16, bottom: 8, containLabel: true },
        legend: { show: false },
        tooltip: {
          ...baseOption(colors).tooltip,
          trigger: 'axis' as const,
          formatter: (params: unknown) => {
            const [first] = params as { dataIndex: number }[];
            const point = points[first?.dataIndex ?? -1];
            if (!point) return '';
            const anomaly = flagged.get(point.day);
            return [
              `<b>${point.day}</b>`,
              `${money(point.amount)}${point.estimated ? ' (estimated, not final yet)' : ''}`,
              anomaly
                ? `<span style="color:${ANOMALY_COLOR}">Anomaly: ${anomaly.service || 'whole account'} expected ${money(anomaly.expected)}, was ${money(anomaly.actual)}</span>`
                : '',
            ]
              .filter(Boolean)
              .join('<br/>');
          },
        },
        xAxis: {
          type: 'category' as const,
          data: points.map((point) => point.day.slice(5)),
          axisLabel: { color: colors.text, fontSize: 11, hideOverlap: true },
          axisLine: { lineStyle: { color: colors.axis } },
        },
        yAxis: {
          type: 'value' as const,
          axisLabel: {
            color: colors.text,
            fontSize: 11,
            formatter: (value: number) => formatMoneyCompact(value, current?.currency ?? 'USD'),
          },
          splitLine: { lineStyle: { color: colors.split } },
        },
        series: [
          {
            type: 'bar' as const,
            data: points.map((point) => ({
              value: point.amount,
              itemStyle: { color: '#009966', opacity: point.estimated ? 0.45 : 1 },
            })),
          },
          {
            // Anomalies as dots over their day's bar.
            type: 'line' as const,
            data: points.map((point) => (flagged.has(point.day) ? point.amount : null)),
            lineStyle: { width: 0 },
            symbol: 'circle',
            symbolSize: 9,
            itemStyle: { color: ANOMALY_COLOR },
            tooltip: { show: false },
          },
        ],
      };
    },
    [current, data, days, money],
  );

  if (!data) {
    return (
      <div className="flex justify-center py-16">
        <Spin indicator={<LoadingOutlined spin />} />
      </div>
    );
  }

  const { summary } = data;
  if (summary.sources.length === 0 && summary.currencies.length === 0) {
    return (
      <>
        <SiteErrors outcomes={data.outcomes} />
        <Empty
          className="py-12"
          description="No cost data yet. Turn on Cost for a cloud connection; the probe delivers the first days within minutes (billing API) or once the first export lands."
        >
          {onManageCloud && (
            <Button type="primary" onClick={onManageCloud}>
              Cloud connections
            </Button>
          )}
        </Empty>
      </>
    );
  }

  const ranked = (response: ExploreResponse, dimension: CostDimension) =>
    (response.currencies.find((block) => block.currency === current?.currency)?.rows ?? [])
      .slice(0, TOP)
      .map((row) => ({
        key: row.key,
        label: valueLabel(dimension, row.key, row.label),
        amount: row.total,
      }));
  const estimatedDays = (current?.daily ?? []).filter((point) => point.estimated).length;

  return (
    <div className="flex flex-col gap-4">
      <SiteErrors outcomes={data.outcomes} />
      {summary.currencies.length > 1 && (
        <div className="flex items-center gap-2 text-sm text-gray-600 dark:text-gray-300">
          Currency
          <Segmented
            size="small"
            value={current?.currency}
            onChange={(value) => setCurrency(value as string)}
            options={summary.currencies.map((item) => item.currency)}
          />
          <span className="text-xs text-gray-500 dark:text-gray-400">
            Amounts in different currencies are never added together.
          </span>
        </div>
      )}

      {current && (
        <div className="grid grid-cols-2 gap-4 xl:grid-cols-4">
          <Tile
            label="Month to date"
            value={money(current.monthToDate)}
            hint={
              current.lastMonthSameDays
                ? `${formatChange(current.monthToDate, current.lastMonthSameDays)} vs the same days last month`
                : undefined
            }
            tooltip="Effective (amortized) cost, so a reservation's upfront fee is spread over its term"
          />
          <Tile
            label="Month-end forecast"
            value={current.forecast !== undefined ? money(current.forecast) : 'Not available'}
            hint={
              current.forecast !== undefined
                ? 'Month to date plus the providers’ forecast'
                : 'A provider forecast is missing for some connection'
            }
          />
          <Tile label="Last month" value={money(current.lastMonth)} />
          <Tile
            label="Open anomalies"
            value={summary.openAnomalies}
            hint="Last 90 days, see Budgets & anomalies"
          />
        </div>
      )}

      <Panel
        title="Daily cost"
        subtitle={
          estimatedDays
            ? `Lighter bars are estimated: the provider can still change them (${estimatedDays} day${estimatedDays === 1 ? '' : 's'}). Red dots are anomalies.`
            : 'Red dots are anomalies.'
        }
        extra={
          <Segmented
            size="small"
            value={days}
            onChange={(value) => setDays(value as 30 | 90)}
            options={[
              { value: 30, label: '30 days' },
              { value: 90, label: '90 days' },
            ]}
          />
        }
      >
        <EChart option={trend} height={260} />
      </Panel>

      <div className="grid grid-cols-1 gap-4 xl:grid-cols-2">
        <Panel
          title="Top services"
          subtitle="Month to date"
          extra={
            <Button size="small" type="link" onClick={() => onExplore('service')}>
              Explore
            </Button>
          }
        >
          <RankList
            rows={ranked(data.services, 'service')}
            format={money}
            onClick={(value) => onExplore('resource', { dimension: 'service', value })}
          />
        </Panel>
        <Panel
          title="Top accounts"
          subtitle="Month to date"
          extra={
            <Button size="small" type="link" onClick={() => onExplore('account')}>
              Explore
            </Button>
          }
        >
          <RankList
            rows={ranked(data.accounts, 'account')}
            format={money}
            onClick={(value) => onExplore('service', { dimension: 'account', value })}
          />
        </Panel>
      </div>

      <Panel title="Data status" subtitle="What each connection's cost source last delivered">
        <Table<SiteSource>
          size="small"
          pagination={false}
          rowKey={(source) =>
            `${source.siteId}/${source.projectId}/${source.connectionId}/${source.source}`
          }
          dataSource={summary.sources}
          scroll={{ x: 'max-content' }}
          columns={[
            { title: 'Connection', dataIndex: 'connectionName' },
            ...(sites.length > 1 ? [{ title: 'Site', dataIndex: 'siteName' }] : []),
            {
              title: 'Source',
              dataIndex: 'source',
              render: (source: SiteSource['source']) => <SourceBadge source={source} />,
            },
            {
              title: 'Days covered',
              render: (_, source) =>
                source.coveredFrom ? `${source.coveredFrom} to ${source.coveredTo}` : '-',
            },
            {
              title: 'Final through',
              render: (_, source) => (
                <Tooltip title="Later days are estimated until the provider closes them">
                  {source.finalThrough ?? '-'}
                </Tooltip>
              ),
            },
            {
              title: 'Delivered',
              render: (_, source) => (
                <Tooltip title={dayjs(source.deliveredAt).format('YYYY-MM-DD HH:mm')}>
                  {dayjs(source.deliveredAt).fromNow()}
                </Tooltip>
              ),
            },
          ]}
        />
      </Panel>
    </div>
  );
};
