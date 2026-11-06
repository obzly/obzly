import type { LogItem } from './LogItem';

// One NDJSON line of a streamed search (POST /logs/query/stream): "rows"
// (a time window's rows, in page order), "progress" (a window read without
// any), then "done" or "error".
export interface SearchFrame {
  type: 'rows' | 'progress' | 'heartbeat' | 'done' | 'error';
  logs?: LogItem[];
  window?: { from: string; to: string };
  nextCursor?: string;
  total?: number;
  countSkipped?: boolean;
  executedIn?: string;
  status?: number;
  error?: string;
  partial?: boolean;
  searchedThrough?: string;
}
