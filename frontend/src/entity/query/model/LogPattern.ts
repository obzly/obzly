import type { QueryNode } from './QueryNode';
import type { TimeRange } from './TimeRange';

// A message template; `hash` is what a `pattern.hash:<hash>` filter matches.
export interface LogPattern {
  hash: string;
  pattern: string;
  sample: string;
  count: number;
}

export interface LogPatternsRequest {
  query?: QueryNode | null;
  timeRange?: TimeRange;
  limit?: number;
  /** Count from the range's newest end until this many logs matched. */
  sampleRows?: number;
}

/** What a facet asked with `sampleRows` counted. */
export interface FacetSample {
  /** Set when the facet stopped early: it counted from here to the range's end. */
  sampledFrom?: string;
  matchedRows: number;
}

export interface LogPatternsResponse extends FacetSample {
  patterns: LogPattern[];
}
