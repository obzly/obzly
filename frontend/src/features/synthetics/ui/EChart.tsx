import type { EChartsOption } from 'echarts';
import type { ECharts } from 'echarts/core';
import { useEffect, useRef } from 'react';

import { useTheme } from '../../../shared/theme';
import { echarts } from '../../dashboards';

export interface ChartColors {
  text: string;
  axis: string;
  split: string;
  tooltip: string;
}

interface Props {
  option: (colors: ChartColors) => EChartsOption;
  height: number;
  onClick?: (params: { dataIndex: number; seriesIndex?: number; value?: unknown }) => void;
}

export const EChart = ({ option, height, onClick }: Props) => {
  const containerRef = useRef<HTMLDivElement>(null);
  const chartRef = useRef<ECharts | null>(null);
  const { theme } = useTheme();

  useEffect(() => {
    if (!containerRef.current) return;
    const chart = chartRef.current ?? echarts.init(containerRef.current);
    chartRef.current = chart;
    const dark = theme === 'dark';
    chart.setOption(
      option({
        text: dark ? '#e5e7eb' : '#374151',
        axis: dark ? '#4b5563' : '#d1d5db',
        split: dark ? '#374151' : '#e5e7eb',
        tooltip: dark ? '#1f2937' : '#ffffff',
      }),
      true,
    );
    chart.off('click');
    if (onClick) chart.on('click', (params) => onClick(params as never));
  }, [option, theme, onClick]);

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
