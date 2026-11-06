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
