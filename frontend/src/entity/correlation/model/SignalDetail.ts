export interface LogSignalContent {
  level: string;
  message: string;
  timestamp: string;
}

export interface SpanSignalContent {
  name: string;
  serviceName: string;
  statusCode: string;
  traceId: string;
  spanId: string;
  startTime: string;
}

// 'metric' has no content type yet; the UI renders it generically.
export type SignalType = 'log' | 'span' | 'metric';

export interface SignalDetail {
  signalType: SignalType;
  occurredAt: string;
  available: boolean;

  log?: LogSignalContent;
  span?: SpanSignalContent;
}
