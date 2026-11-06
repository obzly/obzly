export type LlmGroupBy = 'service' | 'model' | 'provider' | 'operation';

export interface LlmOverviewParams {
  from: string;
  to: string;
  groupBy: LlmGroupBy;
  bucketSeconds?: number;
}

export interface LlmSummary {
  calls: number;
  inputTokens: number;
  outputTokens: number;
  cachedTokens: number;
  errors: number;
  p50Ms: number;
  p95Ms: number;
  services: number;
  /** Covers models with a price only. */
  costUsd: number;
  /** Models that used tokens but have no price. */
  unpricedModels: string[];
}

export interface LlmTimeseriesPoint {
  bucket: number;
  model: string;
  calls: number;
  inputTokens: number;
  outputTokens: number;
}

export interface LlmBreakdownRow {
  key: string;
  calls: number;
  inputTokens: number;
  outputTokens: number;
  errors: number;
  p95Ms: number;
  /** null when none of the group's tokens have a price. */
  costUsd: number | null;
  hasTokens: boolean;
  unpriced: boolean;
}

export interface LlmOverview {
  summary: LlmSummary;
  timeseries: LlmTimeseriesPoint[];
  bucketSeconds: number;
  groupBy: LlmGroupBy;
  breakdown: LlmBreakdownRow[];
  latestCallAt: number;
}

/** One call's metadata; message content is never returned. */
export interface LlmCall {
  kind: 'slow' | 'failed';
  timestamp: number;
  service: string;
  operation: string;
  model: string;
  provider: string;
  durationMs: number;
  inputTokens: number;
  outputTokens: number;
  isError: boolean;
  errorType: string;
  finishReasons: string[];
  traceId: string;
  spanId: string;
  serverAddress: string;
}

export interface LlmCallsResponse {
  slowest: LlmCall[];
  failed: LlmCall[];
}
