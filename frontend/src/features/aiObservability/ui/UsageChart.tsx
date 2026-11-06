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
  const { theme } = useTheme();

  useEffect(() => {
    if (!containerRef.current) return;
    const chart = chartRef.current ?? echarts.init(containerRef.current);
    chartRef.current = chart;

    const isDark = theme === 'dark';
    const text = isDark ? '#e5e7eb' : '#374151';
    const axisLine = isDark ? '#4b5563' : '#d1d5db';
    const splitLine = isDark ? '#374151' : '#e5e7eb';

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
      grid: { left: 60, right: 16, top: 16, bottom: 56 },
      tooltip: {
        trigger: 'axis',
        backgroundColor: isDark ? '#1f2937' : '#ffffff',
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
        data,
      })),
    };
    chart.setOption(option, true);
  }, [points, metric, bucketSeconds, from, to, theme]);

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
