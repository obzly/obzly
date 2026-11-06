import type { QueryNode } from './QueryNode';
import type { TimeRange } from './TimeRange';

export interface LogQueryRequest {
  query: QueryNode | null;
  timeRange?: TimeRange;
  limit?: number;
  offset?: number;
  sortOrder?: 'asc' | 'desc';
  // A previous response's nextCursor: the page after it (offset is ignored).
  cursor?: string;
  // false skips the count (total is then -1).
  trackTotal?: boolean;
}
