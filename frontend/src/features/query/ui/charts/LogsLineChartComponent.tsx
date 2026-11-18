import type { ECharts } from 'echarts/core';
import React, { useEffect, useMemo, useRef } from 'react';

import { useTheme } from '../../../../shared/theme';
import { timezoneShiftMs, useDisplayTimezone } from '../../../../shared/time';
import { echarts } from '../../../dashboards';

export interface LineSeries {
  name: string;
  color: string;
  /** [time ms, value]; null is a gap. */
  points: [number, number | null][];
  dashed?: boolean;
  area?: boolean;
}

interface Props {
  series: LineSeries[];
  /** Formats axis and tooltip values. */
  formatValue: (value: number) => string;
  /** Vertical markers, e.g. a rollout. */
  markers?: { time: number; label: string }[];
  /**
   * Relative x axis: values are ms offsets, labelled by this (an overlay of two
   * windows). Absent: wall-clock time in the display timezone.
   */
  formatOffset?: (offsetMs: number) => string;
  height?: number;
  ariaLabel: string;
}

const escapeHtml = (value: string): string =>
  value.replace(/[&<>"']/g, (ch) => `&#${ch.charCodeAt(0)};`);

/** A themed ECharts line chart for Logs › Chart (time series, comparisons, SQL results). */
export const LogsLineChartComponent = ({
  series,
  formatValue,
  markers,
  formatOffset,
  height = 220,
  ariaLabel,
}: Props): React.JSX.Element => {
  const { definition } = useTheme();
  const colors = definition.colors;
  const [timezone] = useDisplayTimezone();
  const containerRef = useRef<HTMLDivElement>(null);
  const chartRef = useRef<ECharts | null>(null);

  // ECharts renders local time or UTC: shift so UTC shows the display timezone.
  const shift = useMemo(
    () => (ms: number) => (formatOffset ? ms : ms + timezoneShiftMs(ms, timezone)),
    [timezone, formatOffset],
  );

  useEffect(() => {
    if (!containerRef.current) return;
    const chart = chartRef.current ?? echarts.init(containerRef.current);
    chartRef.current = chart;
    chart.setOption(
      {
        animation: false,
        useUTC: true,
        grid: { left: 8, right: 16, top: 12, bottom: 20, containLabel: true },
        tooltip: {
          trigger: 'axis',
          backgroundColor: colors.panel,
          borderColor: colors.border,
          textStyle: { color: colors.text },
          valueFormatter: (value: unknown) =>
            typeof value === 'number' ? formatValue(value) : 'n/a',
        },
        xAxis: {
          type: formatOffset ? 'value' : 'time',
          axisLine: { lineStyle: { color: colors.border } },
          axisLabel: {
            color: colors.muted,
            ...(formatOffset ? { formatter: (v: number) => formatOffset(v) } : {}),
          },
          splitLine: { show: false },
          ...(formatOffset ? { min: 'dataMin', max: 'dataMax' } : {}),
        },
        yAxis: {
          type: 'value',
          axisLabel: { color: colors.muted, formatter: (v: number) => formatValue(v) },
          splitLine: { lineStyle: { color: colors.track } },
        },
        series: series.map((s, i) => ({
          type: 'line',
          name: escapeHtml(s.name || '(none)'),
          showSymbol: false,
          connectNulls: false,
          data: s.points.map(([t, v]) => [shift(t), v]),
          lineStyle: { color: s.color, width: 2, type: s.dashed ? 'dashed' : 'solid' },
          itemStyle: { color: s.color },
          ...(s.area ? { areaStyle: { color: s.color, opacity: 0.12 } } : {}),
          ...(i === 0 && markers?.length
            ? {
                markLine: {
                  symbol: 'none',
                  silent: true,
                  lineStyle: { color: colors.muted, type: 'dashed' },
                  label: { color: colors.muted, formatter: (p: { name?: string }) => p.name ?? '' },
                  data: markers.map((m) => ({ name: m.label, xAxis: shift(m.time) })),
                },
              }
            : {}),
        })),
      },
      true,
    );
    const resize = () => chart.resize();
    window.addEventListener('resize', resize);
    return () => window.removeEventListener('resize', resize);
  }, [series, markers, colors, shift, formatValue, formatOffset]);

  useEffect(
    () => () => {
      chartRef.current?.dispose();
      chartRef.current = null;
    },
    [],
  );

  return (
    <div
      ref={containerRef}
      role="img"
      aria-label={ariaLabel}
      style={{ height }}
      className="w-full"
    />
  );
};
