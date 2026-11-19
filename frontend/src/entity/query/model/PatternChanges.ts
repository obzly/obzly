import type { QueryNode } from './QueryNode';
import type { TimeRange } from './TimeRange';

// Log anomalies ("Anomalies" on the histogram).
export interface PatternChangesRequest {
  query?: QueryNode | null;
  current: TimeRange;
  baseline: TimeRange;
  minCount?: number;
  limit?: number;
  /**
   * Only the counts per kind (no list), for the Anomalies button: over whole minutes, from
   * per-minute counts, cached briefly. Not answered (counted: false) when the query needs
   * raw logs (e.g. a message search).
   */
  countOnly?: boolean;
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
  /** Patterns that changed each way, before the list's limit. */
  newCount?: number;
  spikeCount?: number;
  dropCount?: number;
  /** False when a countOnly request wasn't answered. */
  counted?: boolean;
  /** Set when the baseline reaches back before the current pattern version: regrouped patterns are left out of new and dropped (countOnly counts neither). */
  patternVersionSince?: string;
}
