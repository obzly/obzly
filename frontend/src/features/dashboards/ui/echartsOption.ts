import type { EChartsOption } from 'echarts';
import type { CallbackDataParams } from 'echarts/types/dist/shared';

import type { AggregationResponse, Panel } from '../../../entity/dashboards';
import { escapeHtml } from '../../../shared/lib';

// Formats a value per the panel's unit. 'percent' expects a 0-1 fraction (the
// OTel convention), not 0-100.
export function formatByUnit(value: number, unit: Panel['unit']): string {
  if (unit === 'bytes') {
    const abs = Math.abs(value);
    if (abs >= 1024 ** 3) return `${(value / 1024 ** 3).toFixed(2)} GB`;
    if (abs >= 1024 ** 2) return `${(value / 1024 ** 2).toFixed(2)} MB`;
    if (abs >= 1024) return `${(value / 1024).toFixed(2)} KB`;
    return `${value.toFixed(0)} B`;
  }
  if (unit === 'percent') {
    return `${(value * 100).toFixed(1)}%`;
  }
  return value.toLocaleString(undefined, { maximumFractionDigits: 2 });
}

// Grouped panels can have dozens of series, so the tooltip shows the top few by
// hovered value and summarizes the rest in one line.
const MAX_TOOLTIP_SERIES = 8;

// Detects a metric series name flattened from a label set ("a=x, b=y") and
// returns its {label: value} pairs in order; null for anything else.
export function parseLabelSet(seriesName: string): Map<string, string> | null {
  const parts = seriesName.split(', ');
  const pairs = new Map<string, string>();
  for (const part of parts) {
    const eq = part.indexOf('=');
    if (eq <= 0) return null;
    pairs.set(part.slice(0, eq), part.slice(eq + 1));
  }
  return pairs.size > 0 ? pairs : null;
}

// "k8s_namespace_name" -> "Namespace": drops the "_name" suffix and title-cases
// the rest. Dotted names from older data read the same way.
function prettifyLabelKey(key: string): string {
  if (key === '__name__') return 'Metric';
  const segments = key.split(/[._]/).filter(Boolean);
  const last = segments[segments.length - 1];
  const base = last === 'name' && segments.length > 1 ? segments[segments.length - 2] : last;
  return base.charAt(0).toUpperCase() + base.slice(1);
}

// Legend text for a metric series: label values only ("aws-eks-nodeagent · kube-system");
// hovering a legend item shows the full label set.
export function shortLegendName(name: string): string {
  const labels = parseLabelSet(name);
  return labels ? [...labels.values()].join(' · ') : name;
}

// With trigger 'axis' (every non-pie chart here) ECharts passes an array, one
// entry per series.
function axisTooltipFormatter(unit: Panel['unit'], borderColor: string) {
  return (params: CallbackDataParams | CallbackDataParams[]): string => {
    const list = Array.isArray(params) ? params : [params];
    if (list.length === 0) return '';

    const point = list[0]?.value;
    const timestamp = Array.isArray(point) ? point[0] : undefined;
    const header =
      typeof timestamp === 'number' || typeof timestamp === 'string'
        ? new Date(timestamp).toLocaleString()
        : '';

    const numericValue = (p: CallbackDataParams): number => {
      const v = Array.isArray(p.value) ? p.value[1] : p.value;
      return typeof v === 'number' ? v : Number.NEGATIVE_INFINITY;
    };
    const sorted = [...list].sort((a, b) => numericValue(b) - numericValue(a));
    const shown = sorted.slice(0, MAX_TOOLTIP_SERIES);
    const hiddenCount = sorted.length - shown.length;

    const formattedValue = (p: CallbackDataParams): string => {
      const v = Array.isArray(p.value) ? p.value[1] : p.value;
      return typeof v === 'number' ? formatByUnit(v, unit) : String(v);
    };

    // A table only works if every shown series parses as a label set; otherwise all
    // rows fall back to the plain line format.
    const labelSets = shown.map((p) => parseLabelSet(p.seriesName ?? ''));
    const canTable = labelSets.every((s): s is Map<string, string> => s !== null);

    let body: string;
    if (canTable) {
      const columns: string[] = [];
      for (const set of labelSets) {
        for (const key of set.keys()) {
          if (!columns.includes(key)) columns.push(key);
        }
      }
      const headerCells = columns
        .map(
          (c) =>
            `<th style="text-align:left;padding:2px 8px 2px 0;">${escapeHtml(prettifyLabelKey(c))}</th>`,
        )
        .join('');
      const rows = shown
        .map((p, i) => {
          const set = labelSets[i];
          const cells = columns
            .map((c) => `<td style="padding:2px 8px 2px 0;">${escapeHtml(set?.get(c) ?? '-')}</td>`)
            .join('');
          return (
            `<tr style="border-top:1px solid ${borderColor};">${cells}` +
            `<td style="padding:2px 0 2px 8px;font-weight:600;">${p.marker ?? ''}${escapeHtml(formattedValue(p))}</td></tr>`
          );
        })
        .join('');
      body =
        `<table style="border-collapse:collapse;">` +
        `<thead><tr>${headerCells}<th style="text-align:left;padding:2px 0 2px 8px;">Value</th></tr></thead>` +
        `<tbody>${rows}</tbody></table>`;
      if (hiddenCount > 0) {
        body += `<div>… and ${hiddenCount} more (use a filter to narrow this down)</div>`;
      }
    } else {
      const lines = shown.map(
        (p) =>
          `${p.marker ?? ''}${escapeHtml(p.seriesName ?? '')}: ${escapeHtml(formattedValue(p))}`,
      );
      if (hiddenCount > 0) {
        lines.push(`… and ${hiddenCount} more (use a filter to narrow this down)`);
      }
      body = lines.join('<br/>');
    }

    return [header, body].filter(Boolean).join('<br/>');
  };
}

// ECharts draws on a canvas, which CSS classes and variables can't reach, so the
// caller passes the active theme's colours (useTheme: definition.colors + chartColors).
export interface ChartPalette {
  text: string;
  muted: string;
  /** Axis lines and the tooltip border. */
  border: string;
  /** Grid lines and tooltip row separators. */
  track: string;
  /** Tooltip background and pie slice borders. */
  panel: string;
  series: string[];
}

export function chartPalette(
  colors: { text: string; muted: string; border: string; track: string; panel: string },
  series: string[],
): ChartPalette {
  return {
    text: colors.text,
    muted: colors.muted,
    border: colors.border,
    track: colors.track,
    panel: colors.panel,
    series,
  };
}

// Builds the ECharts option for a line/bar/stackedBar/pie panel (the other
// kinds don't use ECharts). `c` follows the app theme, not the OS.
export function buildEchartsOption(
  panel: Panel,
  data: AggregationResponse,
  c: ChartPalette,
): EChartsOption {
  const tooltip = {
    backgroundColor: c.panel,
    borderColor: c.border,
    textStyle: { color: c.text },
  };

  if (panel.type === 'pie') {
    return {
      color: c.series,
      tooltip: { trigger: 'item', ...tooltip },
      legend: {
        bottom: 0,
        type: 'scroll',
        textStyle: { color: c.muted },
        formatter: shortLegendName,
        tooltip: { show: true },
      },
      series: [
        {
          type: 'pie',
          radius: ['42%', '66%'],
          avoidLabelOverlap: true,
          itemStyle: { borderColor: c.panel, borderWidth: 2 },
          label: { color: c.muted },
          data: data.buckets.map((bucket) => ({
            name: bucket.groupKey || '(none)',
            value: bucket.value,
          })),
        },
      ],
    };
  }

  const isStacked = panel.type === 'stackedBar';
  const seriesType = panel.type === 'bar' || isStacked ? 'bar' : 'line';
  const hasGroups = data.buckets.some((bucket) => bucket.groupKey);

  const axisStyle = {
    axisLine: { lineStyle: { color: c.border } },
    axisLabel: { color: c.muted },
    splitLine: { lineStyle: { color: c.track } },
  };
  const valueAxisStyle = {
    ...axisStyle,
    axisLabel: { ...axisStyle.axisLabel, formatter: (v: number) => formatByUnit(v, panel.unit) },
  };

  if (!hasGroups) {
    return {
      color: c.series,
      tooltip: {
        trigger: 'axis',
        ...tooltip,
        formatter: axisTooltipFormatter(panel.unit, c.track),
      },
      // Small top/bottom margins: ECharts only triggers an 'axis' tooltip inside the
      // grid, so a big margin makes the tooltip vanish near the edges.
      grid: { left: 8, right: 16, top: 8, bottom: 20, containLabel: true },
      xAxis: { type: 'time', ...axisStyle },
      yAxis: { type: 'value', ...valueAxisStyle },
      series: [
        {
          type: seriesType,
          smooth: seriesType === 'line',
          showSymbol: false,
          data: data.buckets.map((bucket) => [bucket.timestamp, bucket.value]),
        },
      ],
    };
  }

  const groupKeys = Array.from(new Set(data.buckets.map((bucket) => bucket.groupKey || '')));

  return {
    color: c.series,
    tooltip: {
      trigger: 'axis',
      ...tooltip,
      formatter: axisTooltipFormatter(panel.unit, c.track),
    },
    // A stacked bar panel's footer is its legend (with each series' share).
    legend: {
      show: !isStacked,
      bottom: 0,
      type: 'scroll',
      textStyle: { color: c.muted },
      formatter: shortLegendName,
      tooltip: { show: true },
    },
    // As above; bottom is larger to fit the legend under the time labels.
    grid: { left: 8, right: 16, top: 8, bottom: isStacked ? 20 : 32, containLabel: true },
    xAxis: { type: 'time', ...axisStyle },
    yAxis: { type: 'value', ...valueAxisStyle },
    series: groupKeys.map((groupKey) => ({
      name: groupKey || '(none)',
      type: seriesType,
      stack: seriesType === 'bar' ? 'total' : undefined,
      smooth: seriesType === 'line',
      showSymbol: false,
      data: data.buckets
        .filter((bucket) => (bucket.groupKey || '') === groupKey)
        .map((bucket) => [bucket.timestamp, bucket.value]),
    })),
  };
}
