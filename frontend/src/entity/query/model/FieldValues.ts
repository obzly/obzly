import type { FacetSample } from './LogPattern';
import type { QueryNode } from './QueryNode';
import type { TimeRange } from './TimeRange';

export interface FieldValuesRequest {
  field: string;
  query?: QueryNode | null;
  timeRange?: TimeRange;
  limit?: number;
  /** Count from the range's newest end until this many logs matched. */
  sampleRows?: number;
  /** For a log source field (service, namespace, workload, container, pod,
   * node, cluster, host), also list values seen recently: see `recent`. */
  includeRecent?: boolean;
}

/** A value not in `values` whose sources logged in the last 36 h or in the range. */
export interface RecentFieldValue {
  value: string;
  lastSeen: string;
  /** Logged inside the range (left out of `values` by the sample or limit). */
  inRange?: boolean;
}

export interface FieldValue {
  value: string;
  count: number;
}

export interface FieldValuesResponse extends FacetSample {
  values: FieldValue[];
  /** With includeRecent, newest first and without counts. */
  recent?: RecentFieldValue[];
  /** Recent values weren't looked up: the query filters on other fields. */
  recentSkipped?: boolean;
}
