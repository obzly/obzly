import {
  type Panel,
  type PanelSource,
  type PanelType,
  type SpanGroupField,
  type SpanMetric,
  effectivePanelSource,
} from '../../../entity/dashboards';

export const PANEL_TYPE_OPTIONS: { value: PanelType; label: string; use: string }[] = [
  { value: 'line', label: 'Line chart', use: 'series over time' },
  { value: 'bar', label: 'Bar chart', use: 'stacked bars per bucket' },
  { value: 'pie', label: 'Pie chart', use: 'parts of a whole' },
  { value: 'stat', label: 'Single number', use: 'one big value' },
  { value: 'table', label: 'Table (top values)', use: 'values with counts' },
  { value: 'statTrend', label: 'Stat with trend', use: 'value, change and sparkline' },
  { value: 'rankedList', label: 'Ranked list', use: 'top N with bars' },
  { value: 'donut', label: 'Donut', use: 'parts with a total' },
  { value: 'gauge', label: 'Gauge', use: 'value against a max' },
  { value: 'stackedBar', label: 'Stacked bars', use: 'series plus summary footer' },
  { value: 'statusStrip', label: 'Status strips', use: 'state per entity over time' },
];

export const SOURCE_OPTIONS: { value: PanelSource; label: string }[] = [
  { value: 'logs', label: 'Logs' },
  { value: 'spans', label: 'Traces' },
  { value: 'metrics', label: 'Metrics' },
];

export const SPAN_METRIC_OPTIONS: { value: SpanMetric; label: string }[] = [
  { value: 'count', label: 'Count' },
  { value: 'error_count', label: 'Error count' },
  { value: 'avg_duration_ms', label: 'Avg duration (ms)' },
  { value: 'p50_duration_ms', label: 'p50 duration (ms)' },
  { value: 'p95_duration_ms', label: 'p95 duration (ms)' },
  { value: 'p99_duration_ms', label: 'p99 duration (ms)' },
];

export const SPAN_GROUP_FIELD_OPTIONS: { value: SpanGroupField; label: string }[] = [
  { value: 'service_name', label: 'Service' },
  { value: 'name', label: 'Operation' },
  { value: 'status_code', label: 'Status' },
  { value: 'kind', label: 'Kind' },
];

const labelOf = <T extends string>(options: { value: T; label: string }[], value: T): string =>
  options.find((o) => o.value === value)?.label ?? value;

/** One line under a panel's title, e.g. "Line chart · Traces · p95 duration (ms) · grouped by Operation". */
export const describePanel = (panel: Panel): string => {
  const source = effectivePanelSource(panel);
  const parts = [labelOf(PANEL_TYPE_OPTIONS, panel.type), labelOf(SOURCE_OPTIONS, source)];

  if (source === 'spans') {
    parts.push(labelOf(SPAN_METRIC_OPTIONS, panel.metric ?? 'count'));
    if (panel.serviceName) parts.push(panel.serviceName);
    if (panel.groupField) {
      parts.push(
        `grouped by ${labelOf(SPAN_GROUP_FIELD_OPTIONS, panel.groupField as SpanGroupField)}`,
      );
    }
  } else if (source === 'metrics') {
    parts.push('PromQL');
  } else if (panel.groupField) {
    parts.push(`grouped by ${panel.groupField}`);
  }

  return parts.join(' · ');
};
