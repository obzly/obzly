import { Spin, Table } from 'antd';
import type { ColumnsType } from 'antd/es/table';
import type { ECharts } from 'echarts/core';
import React, { useEffect, useRef, useState } from 'react';

import { type Panel, dashboardApi, effectivePanelSource } from '../../../entity/dashboards';
import type {
  AggregationBucket,
  PanelTable,
} from '../../../entity/dashboards/model/AggregationResponse';
import { useTheme } from '../../../shared/theme';
import { Sparkline } from '../../../shared/ui/v2';
import type { TimeRange } from '../../query';
import { mergeBuckets } from '../lib/mergeBuckets';
import {
  changeVsPrevious,
  formatWindow,
  isAdditive,
  isTimeBucketedType,
  latestPerSeries,
  stackedSummary,
  statusRows,
  summaryValue,
  totalPerTimestamp,
  valuesByGroup,
  valuesOnGrid,
} from '../lib/panelKinds';
import { sqlPanelData } from '../lib/sqlPanel';
import {
  DonutView,
  GaugeView,
  RankedListView,
  SqlTableView,
  StackedFooter,
  StatTrendView,
  StatusStripView,
} from './PanelKindViews';
import { computeAutoBucketMs } from './bucketInterval';
import { buildEchartsOption, chartPalette, formatByUnit, parseLabelSet } from './echartsOption';
import echarts from './echartsSetup';

interface Props {
  projectId: string;
  panel: Panel;
  timeRange: TimeRange;
  refreshToken?: number;
}

interface TableRow {
  key: string;
  value: number;
  labels?: Map<string, string>;
}

type Bucket = { timestamp?: string; groupKey?: string; value: number };

const ECHARTS_TYPES = new Set<Panel['type']>(['line', 'bar', 'pie', 'stackedBar']);

// A stat with trend's sparkline aims for about this many points.
const TREND_BUCKETS = 30;

// The value of a stat with trend for a window: undefined while loading, null
// when unknown.
type WindowValue = number | null | undefined;

/** One panel's live data, for the grid and for the editor's preview. */
export const PanelChartComponent = ({
  projectId,
  panel,
  timeRange,
  refreshToken,
}: Props): React.JSX.Element => {
  const { definition, chartColors } = useTheme();
  const containerRef = useRef<HTMLDivElement>(null);
  const chartRef = useRef<ECharts | null>(null);

  const [buckets, setBuckets] = useState<Bucket[]>([]);
  const [isLoading, setIsLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  // A logs_sql panel's result, and why it can't be drawn as this kind.
  const [table, setTable] = useState<PanelTable | null>(null);
  const [sqlProblem, setSqlProblem] = useState<string | null>(null);

  const [previousValue, setPreviousValue] = useState<WindowValue>(undefined);
  // Only a span duration needs its own un-bucketed total; anything else is
  // read off the bucketed answer.
  const [currentValue, setCurrentValue] = useState<WindowValue>(undefined);

  const isTimeSeries = isTimeBucketedType(panel.type);
  const isTrend = panel.type === 'statTrend';
  const effectiveBucketMs =
    panel.bucketMs && panel.bucketMs > 0
      ? panel.bucketMs
      : isTimeSeries
        ? computeAutoBucketMs(timeRange.from.valueOf(), timeRange.to.valueOf())
        : isTrend
          ? computeAutoBucketMs(timeRange.from.valueOf(), timeRange.to.valueOf(), TREND_BUCKETS)
          : undefined;

  useEffect(() => {
    let cancelled = false;
    // Aborting stops the query on the Site when the panel or range changes.
    const abort = new AbortController();
    setIsLoading(true);
    setError(null);

    // Streamed: a time-bucketed logs panel fills in as each group of
    // partitions is read, newest first; anything else arrives at once.
    const totals = new Map<string, AggregationBucket>();
    setBuckets([]);
    setTable(null);
    setSqlProblem(null);
    dashboardApi
      .streamPanelQuery(
        projectId,
        {
          panel: { ...panel, bucketMs: effectiveBucketMs },
          timeRange: { from: timeRange.from.toISOString(), to: timeRange.to.toISOString() },
        },
        (received) => {
          if (!cancelled) setBuckets(mergeBuckets(totals, received));
        },
        abort.signal,
        (received) => {
          if (cancelled) return;
          const data = sqlPanelData(panel.type, received);
          setTable(received);
          setSqlProblem(data.problem);
          setBuckets(data.buckets);
        },
      )
      .catch((e: unknown) => {
        if (!cancelled) setError((e as Error).message);
      })
      .finally(() => {
        if (!cancelled) setIsLoading(false);
      });

    return () => {
      cancelled = true;
      abort.abort();
    };
  }, [projectId, panel, timeRange, effectiveBucketMs, refreshToken]);

  // A stat with trend compares against the window just before this one, read
  // as one un-bucketed total (a metrics panel as a range query, latest point).
  useEffect(() => {
    if (panel.type !== 'statTrend') return;
    let cancelled = false;
    const abort = new AbortController();
    const isMetricsPanel = effectivePanelSource(panel) === 'metrics';
    const additive = isAdditive(panel);
    const fromMs = timeRange.from.valueOf();
    const windowMs = timeRange.to.valueOf() - fromMs;
    const totalPanel = { ...panel, bucketMs: undefined };
    const totalOf = (result: { buckets: Bucket[] }): number | null =>
      !additive && !isMetricsPanel && result.buckets.length === 0
        ? null
        : summaryValue(result.buckets, isMetricsPanel);

    setPreviousValue(undefined);
    setCurrentValue(undefined);
    dashboardApi
      .executePanelQuery(
        projectId,
        {
          panel: totalPanel,
          timeRange: {
            from: new Date(fromMs - windowMs).toISOString(),
            to: timeRange.from.toISOString(),
          },
        },
        abort.signal,
      )
      .then((result) => !cancelled && setPreviousValue(totalOf(result)))
      .catch(() => !cancelled && setPreviousValue(null));

    if (!additive && !isMetricsPanel) {
      dashboardApi
        .executePanelQuery(
          projectId,
          {
            panel: totalPanel,
            timeRange: { from: timeRange.from.toISOString(), to: timeRange.to.toISOString() },
          },
          abort.signal,
        )
        .then((result) => !cancelled && setCurrentValue(totalOf(result)))
        .catch(() => !cancelled && setCurrentValue(null));
    }

    return () => {
      cancelled = true;
      abort.abort();
    };
  }, [projectId, panel, timeRange, refreshToken]);

  useEffect(() => {
    if (!ECHARTS_TYPES.has(panel.type)) {
      chartRef.current?.dispose();
      chartRef.current = null;
      return;
    }

    if (!containerRef.current) return;

    // Switching kinds in the editor's preview can remount the container.
    if (chartRef.current && chartRef.current.getDom() !== containerRef.current) {
      chartRef.current.dispose();
      chartRef.current = null;
    }
    const chart = chartRef.current ?? echarts.init(containerRef.current);
    chartRef.current = chart;
    chart.setOption(
      buildEchartsOption(panel, { buckets }, chartPalette(definition.colors, chartColors)),
      true,
    );

    const handleResize = () => chart.resize();
    window.addEventListener('resize', handleResize);
    return () => window.removeEventListener('resize', handleResize);
  }, [buckets, panel, definition, chartColors]);

  useEffect(() => {
    return () => {
      chartRef.current?.dispose();
      chartRef.current = null;
    };
  }, []);

  if (error) {
    return (
      <div className="flex h-full items-center justify-center px-2 text-center text-sm text-red-500">
        {error}
      </div>
    );
  }

  if (isLoading && buckets.length === 0) {
    return (
      <div className="flex h-full items-center justify-center">
        <Spin size="small" />
      </div>
    );
  }

  if (sqlProblem) {
    return (
      <div className="flex h-full items-center justify-center px-2 text-center text-sm text-gray-500 dark:text-gray-400">
        {sqlProblem}
      </div>
    );
  }

  if (table && panel.type === 'table') {
    return <SqlTableView table={table} />;
  }

  const isMetrics = effectivePanelSource(panel) === 'metrics';
  const summaryBuckets = isMetrics ? latestPerSeries(buckets) : buckets;
  const additive = isAdditive(panel);
  const grid = {
    fromMs: timeRange.from.valueOf(),
    toMs: timeRange.to.valueOf(),
    bucketMs:
      effectiveBucketMs ?? computeAutoBucketMs(timeRange.from.valueOf(), timeRange.to.valueOf()),
  };
  const colors = chartColors;

  if (panel.type === 'statTrend') {
    const value = additive || isMetrics ? summaryValue(buckets, isMetrics) : currentValue;
    const trend = additive
      ? (valuesOnGrid(buckets, grid, true) as number[])
      : totalPerTimestamp(buckets);
    return (
      <StatTrendView
        panel={panel}
        value={value ?? null}
        change={
          previousValue === undefined || value === undefined
            ? undefined
            : changeVsPrevious(value, previousValue, panel.unit, panel.higherIs)
        }
        window={formatWindow(grid.toMs - grid.fromMs)}
        trend={trend}
      />
    );
  }

  if (panel.type === 'rankedList') {
    return <RankedListView panel={panel} rows={valuesByGroup(buckets, isMetrics)} />;
  }

  if (panel.type === 'donut') {
    return <DonutView panel={panel} rows={valuesByGroup(buckets, isMetrics)} colors={colors} />;
  }

  if (panel.type === 'gauge') {
    const groups = valuesByGroup(buckets, isMetrics);
    const gauges =
      groups.length > 1
        ? groups
        : [
            {
              key: '',
              value: !additive && groups.length === 0 ? null : summaryValue(buckets, isMetrics),
            },
          ];
    return <GaugeView panel={panel} gauges={gauges} />;
  }

  if (panel.type === 'statusStrip') {
    return (
      <StatusStripView
        panel={panel}
        rows={statusRows(buckets, grid, {
          additive,
          thresholds: panel.thresholds,
          higherIs: panel.higherIs,
          unit: panel.unit,
        })}
      />
    );
  }

  if (panel.type === 'stackedBar') {
    return (
      <div className="flex h-full flex-col gap-2">
        <div ref={containerRef} className="min-h-0 w-full flex-1" />
        <StackedFooter panel={panel} summary={stackedSummary(buckets, additive)} colors={colors} />
      </div>
    );
  }

  if (panel.type === 'stat') {
    const total = summaryBuckets.reduce((sum, bucket) => sum + bucket.value, 0);
    // Only metrics stats carry points over time; logs and traces stats are one total.
    const trend = isMetrics ? totalPerTimestamp(buckets) : [];
    return (
      <div className="flex h-full flex-col justify-center gap-2 overflow-hidden px-2">
        <div className="text-center text-4xl leading-none font-semibold text-gray-900 dark:text-gray-50">
          {formatByUnit(total, panel.unit)}
        </div>
        {trend.length > 1 && (
          <Sparkline values={trend} color="var(--v2-accent)" label={`${panel.title} trend`} />
        )}
      </div>
    );
  }

  if (panel.type === 'table') {
    const rows: TableRow[] = [...summaryBuckets]
      .sort((a, b) => b.value - a.value)
      .map((bucket) => ({
        key: bucket.groupKey || '(none)',
        value: bucket.value,
        labels: isMetrics ? (parseLabelSet(bucket.groupKey ?? '') ?? undefined) : undefined,
      }));

    // Metric series get one column per label, headed by the label key.
    const labelKeys = rows.every((row) => row.labels)
      ? [...new Set(rows.flatMap((row) => [...(row.labels?.keys() ?? [])]))]
      : [];
    const valueColumn: ColumnsType<TableRow>[number] = {
      title: isMetrics ? 'Value' : 'Count',
      dataIndex: 'value',
      width: 110,
      sorter: (a, b) => a.value - b.value,
      render: (value: number) => formatByUnit(value, panel.unit),
    };
    const columns: ColumnsType<TableRow> =
      labelKeys.length > 0
        ? [
            ...labelKeys.map((labelKey) => ({
              title: labelKey,
              key: labelKey,
              ellipsis: true,
              render: (_: unknown, row: TableRow) => row.labels?.get(labelKey) ?? '',
              sorter: (a: TableRow, b: TableRow) =>
                (a.labels?.get(labelKey) ?? '').localeCompare(b.labels?.get(labelKey) ?? ''),
            })),
            valueColumn,
          ]
        : [
            { title: isMetrics ? 'Series' : 'Value', dataIndex: 'key', ellipsis: true },
            valueColumn,
          ];

    return (
      <div className="h-full overflow-auto">
        <Table<TableRow>
          size="small"
          pagination={false}
          dataSource={rows}
          rowKey="key"
          columns={columns}
        />
      </div>
    );
  }

  return <div ref={containerRef} className="h-full w-full" />;
};
