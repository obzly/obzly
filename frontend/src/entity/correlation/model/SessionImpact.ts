// Error signals per service in a session's window, most first.
export interface ServiceImpact {
  serviceName: string;
  errorLogs: number;
  errorSpans: number;
  total: number;
}

export interface SessionImpact {
  sessionId: string;
  windowStart: string;
  windowEnd: string;
  services: ServiceImpact[];
}
