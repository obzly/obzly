import type { EChartsOption } from 'echarts';
import type { ECharts } from 'echarts/core';
import React, { useEffect, useRef } from 'react';

import type { AiUsageTimeseriesPoint } from '../../../entity/aiUsage';
import { useTheme } from '../../../shared/theme';
import { echarts } from '../../dashboards';
import { formatCount, formatUsd } from '../lib/format';

export type UsageMetric = 'costUsd' | 'tokens' | 'requests';

interface Props {
  points: AiUsageTimeseriesPoint[];
  metric: UsageMetric;
  bucketSeconds: number;
  from: number;
  to: number;
  height: number;
}

const formatValue = (metric: UsageMetric, value: number): string =>
  metric === 'costUsd' ? formatUsd(value) : formatCount(value);

export const UsageChart = ({
  points,
  metric,
  bucketSeconds,
  from,
  to,
  height,
}: Props): React.JSX.Element => {
  const containerRef = useRef<HTMLDivElement>(null);
  const chartRef = useRef<ECharts | null>(null);
  // ECharts can't use CSS variables, so the theme's colours come from the registry.
  const { definition, chartColors } = useTheme();

  useEffect(() => {
    if (!containerRef.current) return;
    const chart = chartRef.current ?? echarts.init(containerRef.current);
    chartRef.current = chart;

    const text = definition.colors.muted;
    const axisLine = definition.colors.border;
    const splitLine = definition.colors.track;

    // ECharts stacks by data index, not by x value, so every series needs a
    // value (zero if absent) for every bucket or bars float mid-air.
    const buckets = [...new Set(points.map((point) => point.bucket))].sort((x, y) => x - y);
    const valuesByKey = new Map<string, Map<number, number>>();
    for (const point of points) {
      const key = point.key || '(unknown)';
      const values = valuesByKey.get(key) ?? new Map<number, number>();
      values.set(point.bucket, point[metric]);
      valuesByKey.set(key, values);
    }
    const byKey = new Map<string, [number, number][]>(
      [...valuesByKey.entries()].map(([key, values]) => [
        key,
        buckets.map((bucket): [number, number] => [bucket, values.get(bucket) ?? 0]),
      ]),
    );

    const option: EChartsOption = {
      animation: false,
      color: chartColors,
      grid: { left: 60, right: 16, top: 16, bottom: 56 },
      tooltip: {
        trigger: 'axis',
        backgroundColor: definition.colors.panel,
        borderColor: axisLine,
        textStyle: { color: text },
        valueFormatter: (value) => formatValue(metric, Number(value)),
      },
      legend: { bottom: 0, type: 'scroll', textStyle: { color: text } },
      xAxis: {
        type: 'time',
        min: from,
        max: to,
        axisLine: { lineStyle: { color: axisLine } },
        axisLabel: { color: text, hideOverlap: true },
      },
      yAxis: {
        type: 'value',
        axisLabel: { color: text, formatter: (value: number) => formatValue(metric, value) },
        splitLine: { lineStyle: { color: splitLine } },
      },
      series: [...byKey.entries()].map(([name, data]) => ({
        name,
        type: 'bar',
        stack: 'total',
        barMaxWidth: 24,
        barMinWidth: 2,
        itemStyle: { borderRadius: 2 },
        data,
      })),
    };
    chart.setOption(option, true);
  }, [points, metric, bucketSeconds, from, to, definition, chartColors]);

  useEffect(() => {
    const handleResize = () => chartRef.current?.resize();
    window.addEventListener('resize', handleResize);
    return () => {
      window.removeEventListener('resize', handleResize);
      chartRef.current?.dispose();
      chartRef.current = null;
    };
  }, []);

  return <div ref={containerRef} style={{ height }} className="w-full" />;
};
