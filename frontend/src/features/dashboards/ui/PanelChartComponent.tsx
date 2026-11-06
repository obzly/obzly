import { Spin, Table } from 'antd';
import type { ColumnsType } from 'antd/es/table';
import type { ECharts } from 'echarts/core';
import React, { useEffect, useRef, useState } from 'react';

import { type Panel, dashboardApi, effectivePanelSource } from '../../../entity/dashboards';
import type { AggregationBucket } from '../../../entity/dashboards/model/AggregationResponse';
import { useTheme } from '../../../shared/theme';
import type { TimeRange } from '../../query';
import { mergeBuckets } from '../lib/mergeBuckets';
import { computeAutoBucketMs } from './bucketInterval';
import { buildEchartsOption, formatByUnit, parseLabelSet } from './echartsOption';
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

// A metrics panel returns every point of every series; stat/table show each series' latest one.
function latestPerSeries(buckets: Bucket[]): Bucket[] {
  const latest = new Map<string, Bucket>();
  for (const bucket of buckets) {
    const key = bucket.groupKey ?? '';
    const current = latest.get(key);
    if (!current || (bucket.timestamp ?? '') >= (current.timestamp ?? '')) latest.set(key, bucket);
  }
  return [...latest.values()];
}

/** One panel's live data, for the grid and for the editor's preview. */
export const PanelChartComponent = ({
  projectId,
  panel,
  timeRange,
  refreshToken,
}: Props): React.JSX.Element => {
  const { theme } = useTheme();
  const containerRef = useRef<HTMLDivElement>(null);
  const chartRef = useRef<ECharts | null>(null);

  const [buckets, setBuckets] = useState<Bucket[]>([]);
  const [isLoading, setIsLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);

  const isTimeSeries = panel.type === 'line' || panel.type === 'bar';
  const effectiveBucketMs =
    panel.bucketMs && panel.bucketMs > 0
      ? panel.bucketMs
      : isTimeSeries
        ? computeAutoBucketMs(timeRange.from.valueOf(), timeRange.to.valueOf())
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

  useEffect(() => {
    if (panel.type === 'stat' || panel.type === 'table') {
      chartRef.current?.dispose();
      chartRef.current = null;
      return;
    }

    if (!containerRef.current) return;

    const chart = chartRef.current ?? echarts.init(containerRef.current);
    chartRef.current = chart;
    chart.setOption(buildEchartsOption(panel, { buckets }, theme === 'dark'), true);

    const handleResize = () => chart.resize();
    window.addEventListener('resize', handleResize);
    return () => window.removeEventListener('resize', handleResize);
  }, [buckets, panel, theme]);

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

  const isMetrics = effectivePanelSource(panel) === 'metrics';
  const summaryBuckets = isMetrics ? latestPerSeries(buckets) : buckets;

  if (panel.type === 'stat') {
    const total = summaryBuckets.reduce((sum, bucket) => sum + bucket.value, 0);
    return (
      <div className="flex h-full items-center justify-center">
        <div className="text-4xl font-semibold text-gray-800 dark:text-gray-100">
          {formatByUnit(total, panel.unit)}
        </div>
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
