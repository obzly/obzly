import type { LogItem } from './LogItem';

export interface LogQueryResponse {
  logs: LogItem[];
  total: number;
  limit: number;
  offset: number;
  executedIn: string;
  // Fetches the next page; absent on the last page and from older Sites.
  nextCursor?: string;
  // Counting would read more than the Site's scan budget: total is -1.
  countSkipped?: boolean;
  // The search hit its time limit: it covered the range from its newest
  // end back to searchedThrough (from the oldest end up to it, ascending).
  partial?: boolean;
  searchedThrough?: string;
}
