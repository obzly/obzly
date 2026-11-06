import type { EChartsOption } from 'echarts';

import type { ChartColors } from '../ui/EChart';

/** Axis, tooltip and grid styling shared by every Synthetics chart. */
export const baseOption = (colors: ChartColors): EChartsOption => ({
  animation: false,
  grid: { left: 52, right: 16, top: 28, bottom: 28 },
  tooltip: {
    trigger: 'axis',
    backgroundColor: colors.tooltip,
    borderColor: colors.axis,
    textStyle: { color: colors.text, fontSize: 12 },
  },
  legend: { top: 0, right: 0, type: 'scroll', textStyle: { color: colors.text, fontSize: 11 } },
});

export const timeAxis = (colors: ChartColors, from: number, to: number) => ({
  type: 'time' as const,
  min: from,
  max: to,
  axisLine: { lineStyle: { color: colors.axis } },
  axisLabel: { color: colors.text, hideOverlap: true, fontSize: 11 },
  splitLine: { show: false },
});

export const valueAxis = (colors: ChartColors, formatter?: (value: number) => string) => ({
  type: 'value' as const,
  axisLabel: {
    color: colors.text,
    fontSize: 11,
    hideOverlap: true,
    ...(formatter ? { formatter } : {}),
  },
  splitLine: { lineStyle: { color: colors.split } },
});
