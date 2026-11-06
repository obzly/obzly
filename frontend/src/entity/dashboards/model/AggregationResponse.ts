// The same shape for every panel source: a log count or a span duration is just "value".
export interface AggregationBucket {
  timestamp?: string;
  groupKey?: string;
  value: number;
}

export interface AggregationResponse {
  buckets: AggregationBucket[];
}
