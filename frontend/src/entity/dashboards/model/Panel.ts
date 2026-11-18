import type { QueryNode } from '../../query';
import type { GridPos } from './GridPos';
import type { PanelSource, SpanMetric } from './PanelSource';
import type { PanelType } from './PanelType';

// What a logs panel computes per bucket and group.
export type LogMeasure =
  'count' | 'distinct' | 'sum' | 'avg' | 'min' | 'max' | 'p50' | 'p95' | 'p99';

// Whether a rising value is good or bad. Absent: change pills stay neutral and
// thresholds breach upwards.
export type PanelHigherIs = 'good' | 'bad';

// In the panel's raw values (a 'percent' unit is a 0-1 fraction).
export interface PanelThresholds {
  warn?: number;
  critical?: number;
}

// A saved aggregation request plus display metadata. bucketMs undefined/0
// means an auto interval from the dashboard's time range.
export interface Panel {
  id: string;
  title: string;
  type: PanelType;
  // Absent means 'logs' (older panels); see effectivePanelSource.
  source?: PanelSource;

  // logs-source only. measure (absent: count lines) reads measureField;
  // topGroups keeps a time series' N largest groups; groupBands groups a
  // numeric groupField into 1-2-5 log bands (previews only, the backend
  // refuses it on save).
  query?: QueryNode | null;
  measure?: LogMeasure;
  measureField?: string;
  topGroups?: number;
  groupBands?: 'log';

  // spans-source only.
  serviceName?: string;
  operationName?: string;
  rootSpansOnly?: boolean;
  metric?: SpanMetric;

  // metrics-source only: PromQL, run as a range query over the panel's window.
  promQuery?: string;

  // Shared by both sources.
  groupField?: string;
  bucketMs?: number;
  limit?: number;
  // Display only: 'bytes' formats a byte count, 'percent' a 0-1 fraction.
  unit?: 'bytes' | 'percent';
  gridPos: GridPos;

  // Display settings of the v2 kinds (the backend rejects them on other types):
  // min/max bound a gauge; thresholds colour a gauge or status strip; higherIs
  // colours a stat-with-trend change pill and picks the threshold side.
  min?: number;
  max?: number;
  thresholds?: PanelThresholds;
  higherIs?: PanelHigherIs;
}
