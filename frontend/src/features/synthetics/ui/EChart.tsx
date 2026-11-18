import type { EChartsOption } from 'echarts';
import type { ECharts } from 'echarts/core';
import { useEffect, useRef } from 'react';

import { type StatusColors, useTheme } from '../../../shared/theme';
import { echarts } from '../../dashboards';

/** Theme colours handed to every chart option builder, so a theme switch redraws with the new ones. */
export interface ChartColors {
  /** Tooltip text and other strong labels. */
  text: string;
  /** Axis labels and legend text. */
  muted: string;
  axis: string;
  split: string;
  /** Tooltip background (the panel colour). */
  tooltip: string;
  /** Series colours in order. */
  series: string[];
  status: StatusColors;
  accent: string;
}

interface Props {
  option: (colors: ChartColors) => EChartsOption;
  height: number;
  onClick?: (params: { dataIndex: number; seriesIndex?: number; value?: unknown }) => void;
}

export const EChart = ({ option, height, onClick }: Props) => {
  const containerRef = useRef<HTMLDivElement>(null);
  const chartRef = useRef<ECharts | null>(null);
  const { definition, chartColors, status } = useTheme();

  useEffect(() => {
    if (!containerRef.current) return;
    const chart = chartRef.current ?? echarts.init(containerRef.current);
    chartRef.current = chart;
    const { colors } = definition;
    chart.setOption(
      option({
        text: colors.text,
        muted: colors.muted,
        axis: colors.border,
        split: colors.track,
        tooltip: colors.panel,
        series: chartColors,
        status,
        accent: colors.accent,
      }),
      true,
    );
    chart.off('click');
    if (onClick) chart.on('click', (params) => onClick(params as never));
  }, [option, definition, chartColors, status, onClick]);

  useEffect(() => {
    const element = containerRef.current;
    const observer = new ResizeObserver(() => chartRef.current?.resize());
    if (element) observer.observe(element);
    return () => {
      observer.disconnect();
      chartRef.current?.dispose();
      chartRef.current = null;
    };
  }, []);

  return <div ref={containerRef} style={{ height }} className="w-full" />;
};
