// The same shape for every panel source: a log count or a span duration is just "value".
export interface AggregationBucket {
  timestamp?: string;
  groupKey?: string;
  value: number;
}

// A logs_sql panel's result: its SQL can project any columns.
export interface PanelTable {
  columns: string[];
  rows: unknown[][];
  truncated?: boolean;
}

export interface AggregationResponse {
  buckets: AggregationBucket[];
  // Set (and buckets empty) for a logs_sql panel.
  table?: PanelTable;
}
