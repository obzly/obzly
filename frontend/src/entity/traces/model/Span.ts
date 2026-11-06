export interface SpanEvent {
  name: string;
  timestamp: string;
  attributes?: Record<string, unknown>;
}

export interface Span {
  id: string;
  traceId: string;
  spanId: string;
  parentSpanId?: string;
  name: string;
  serviceName: string;
  kind: string;
  startTime: string;
  endTime: string;
  durationMs: number;
  statusCode: string;
  statusMessage?: string;
  attributes?: Record<string, unknown>;
  resourceAttributes?: Record<string, unknown>;
  events?: SpanEvent[];
}
