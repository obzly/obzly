export interface ExecuteSQLQueryRequest {
  sql: string;
  timeRange: { from: string; to: string };
}

// Generic table: a user's SELECT can return any columns.
export interface SQLQueryResponse {
  columns: string[];
  rows: unknown[][];
  rowCount: number;
  truncated: boolean;
  executedInMs: string;
}
