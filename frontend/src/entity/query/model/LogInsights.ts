import type { QueryNode } from './QueryNode';
import type { TimeRange } from './TimeRange';

// The Logs page's Patterns, Volume & noise and Health map tabs and its
// "Explain this spike" panel (all read per-minute counts on the Site).

export type PatternTrendChange = 'new' | 'spike' | 'drop' | '';

export interface PatternTrendsRequest {
  query?: QueryNode | null;
  timeRange: Required<TimeRange>;
  /** Whole minutes; omitted picks one giving at most 60 buckets. */
  bucketMs?: number;
  limit?: number;
}

export interface PatternTrend {
  hash: string;
  pattern: string;
  sample: string;
  level: string;
  services: string[];
  count: number;
  /** Lines per bucket, one per PatternTrendsResponse.buckets. */
  series: number[];
  /** Lines in the same window 24 h earlier. */
  yesterday: number;
  /** Lines in the 7 days before the range. */
  previousWeek: number;
  change: PatternTrendChange;
}

export interface PatternTrendsResponse {
  bucketMs: number;
  buckets: string[];
  total: number;
  distinct: number;
  /** Lines per bucket of the patterns not listed. */
  other: number[];
  newCount: number;
  spikeCount: number;
  dropCount: number;
  patterns: PatternTrend[];
  partial: boolean;
  /** Set when the comparison reaches back before the current pattern version: regrouped patterns are left out of new and dropping. */
  patternVersionSince?: string;
}

export interface ExplainRequest {
  query?: QueryNode | null;
  current: Required<TimeRange>;
  baseline: Required<TimeRange>;
  minCount?: number;
}

/** k8s_replicaset is derived from pod names; it filters as k8s_pod_name containing it. */
export interface ExplainField {
  field: string;
  value: string;
  current: number;
  baseline: number;
  currentShare: number;
  baselineShare: number;
}

export interface ExplainResponse {
  currentTotal: number;
  baselineTotal: number;
  fields: ExplainField[];
  partial: boolean;
}

export interface VolumeSeries {
  name: string;
  counts: number[];
  total: number;
  /** The services outside the top ones, together. */
  other?: boolean;
}

export interface NoisyPattern {
  hash: string;
  pattern: string;
  sample: string;
  service: string;
  level: string;
  /** Lines over the last 7 days. */
  count: number;
  perDay: number;
}

export interface VolumeResponse {
  days: string[];
  services: VolumeSeries[];
  /** Lines per level over the last 7 days. */
  levels: { name: string; count: number }[];
  today: number;
  yesterdaySoFar: number;
  /** Stored (compressed) bytes per line; 0 when unknown. */
  bytesPerLine: number;
  rawBytesPerLine: number;
  storedBytes: number;
  noisy: NoisyPattern[];
  partial: boolean;
}

export type HealthMapGroupBy = 'k8s' | 'host' | 'service';

export interface HealthMapRequest {
  query?: QueryNode | null;
  timeRange: Required<TimeRange>;
  groupBy: HealthMapGroupBy;
}

export interface HealthTile {
  name: string;
  total: number;
  errors: number;
  warnings: number;
  /** Lines in the same window 24 h earlier. */
  previous: number;
}

export interface HealthGroup {
  name: string;
  total: number;
  tiles: HealthTile[];
}

export interface HealthMapResponse {
  groupBy: HealthMapGroupBy;
  groups: HealthGroup[];
  hiddenTiles: number;
  partial: boolean;
}
