import type { Panel } from './Panel';

// logs_sql: the panel's `sql` over SQL mode's "logs" table; its data is columns and rows.
export type PanelSource = 'logs' | 'spans' | 'metrics' | 'logs_sql';

// Older panels have no `source`; they mean 'logs'.
export function effectivePanelSource(panel: Pick<Panel, 'source'>): PanelSource {
  return panel.source ?? 'logs';
}

export type SpanMetric =
  | 'count'
  | 'error_count'
  | 'avg_duration_ms'
  | 'p50_duration_ms'
  | 'p95_duration_ms'
  | 'p99_duration_ms';

export type SpanGroupField = 'service_name' | 'name' | 'status_code' | 'kind';
