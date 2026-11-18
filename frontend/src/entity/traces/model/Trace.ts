import type { Span } from './Span';

export interface TraceSummary {
  traceId: string;
  rootName: string;
  rootServiceName: string;
  startTime: string;
  durationMs: number;
  spanCount: number;
  errorCount: number;
  services: string[];
}

export interface ListTracesResponse {
  traces: TraceSummary[];
}

export interface GetTraceResponse {
  traceId: string;
  spans: Span[];
}

export interface ListServicesResponse {
  services: string[];
}

export interface ListOperationsResponse {
  operations: string[];
}

export interface ListTracesRequest {
  timeRange?: { from?: string; to?: string };
  serviceName?: string;
  operationName?: string;
  minDurationMs?: number;
  maxDurationMs?: number;
  errorsOnly?: boolean;
  limit?: number;
}

export interface TraceSummaryRequest {
  timeRange: { from: string; to: string };
  serviceName?: string;
  operationName?: string;
  minDurationMs?: number;
  maxDurationMs?: number;
  errorsOnly?: boolean;
  // Only the matching-traces count (the summary tiles are hidden).
  countOnly?: boolean;
}

/** null = unknown, never 0. */
export interface TraceSummaryTotals {
  count: number | null;
  errors: number | null;
  p50Ms: number | null;
  p95Ms: number | null;
}

export interface TraceSummaryResponse {
  current: TraceSummaryTotals;
  previous: TraceSummaryTotals;
  windowMinutes: number;
  rootSpansOnly: boolean;
  stepMs: number;
  countTrend: number[];
  // Percent, buckets with spans only.
  errorRateTrend: number[];
  p50Trend: number[];
  p95Trend: number[];
  // Capped at matchingTracesCap when matchingTracesCapped.
  matchingTraces: number;
  matchingTracesCapped: boolean;
  matchingTracesCap: number;
}

export interface ServiceErrorsRequest {
  timeRange: { from: string; to: string };
  limit?: number;
}

/** A service's entry spans (SERVER/CONSUMER spans and trace roots) and failed ones. */
export interface ServiceErrors {
  serviceName: string;
  requests: number;
  errors: number;
  previousRequests: number;
  previousErrors: number;
}

export interface ServiceErrorsResponse {
  /** After the clamp: ranges over 7 days count the last 7 days. */
  from: string;
  to: string;
  previousFrom: string;
  clamped: boolean;
  /** Most errors first. */
  services: ServiceErrors[];
}
