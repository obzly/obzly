import type { QueryNode } from './QueryNode';
import type { TimeRange } from './TimeRange';

// Log anomalies ("Anomalies" on the histogram).
export interface PatternChangesRequest {
  query?: QueryNode | null;
  current: TimeRange;
  baseline: TimeRange;
  minCount?: number;
  limit?: number;
}

export type PatternChangeKind = 'new' | 'spike' | 'drop';

export interface PatternChange {
  hash: string;
  pattern: string;
  sample: string;
  level: string;
  current: number;
  baseline: number;
  change: PatternChangeKind;
  /** Current against the baseline scaled to the same length; huge when new. */
  ratio: number;
}

export interface PatternChangesResponse {
  changes: PatternChange[];
  currentTotal: number;
  baselineTotal: number;
}
