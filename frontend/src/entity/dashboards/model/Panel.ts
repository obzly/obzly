import type { QueryNode } from '../../query';
import type { GridPos } from './GridPos';
import type { PanelSource, SpanMetric } from './PanelSource';
import type { PanelType } from './PanelType';

// A saved aggregation request plus display metadata. bucketMs undefined/0
// means an auto interval from the dashboard's time range.
export interface Panel {
  id: string;
  title: string;
  type: PanelType;
  // Absent means 'logs' (older panels); see effectivePanelSource.
  source?: PanelSource;

  // logs-source only.
  query?: QueryNode | null;

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
}
