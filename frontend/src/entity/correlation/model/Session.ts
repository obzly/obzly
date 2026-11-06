export type TriggerType = 'error_log' | 'span_error_status' | 'metric_threshold' | 'trace_id_link';

export interface Session {
  id: string;
  projectId: string;
  serviceName: string;
  windowStart: string;
  windowEnd: string;
  // Plain string so an unknown trigger type still renders.
  triggerType: string;
  triggerSummary?: string;
  createdAt: string;
}

export interface ListSessionsResponse {
  sessions: Session[];
}
