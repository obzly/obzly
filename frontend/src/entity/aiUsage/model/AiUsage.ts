export interface AiUsageFilter {
  from: string;
  to: string;
  user?: string;
  model?: string;
  terminal?: string;
}

export interface AiUsageOverviewParams extends AiUsageFilter {
  groupBy?: 'model' | 'user';
  bucketSeconds?: number;
}

export interface AiUsageSessionsParams extends AiUsageFilter {
  search?: string;
  sortBy?: 'lastSeen' | 'cost' | 'requests' | 'tokens';
  limit: number;
  offset: number;
}

/** costUsd is Claude Code's API-equivalent estimate, not a subscription charge. */
export interface AiUsageSummary {
  requests: number;
  costUsd: number;
  inputTokens: number;
  outputTokens: number;
  cacheReadTokens: number;
  cacheCreationTokens: number;
  sessions: number;
  users: number;
  prompts: number;
  apiErrors: number;
  toolCalls: number;
  avgDurationMs: number;
  p50TtftMs: number;
}

export interface AiUsageTimeseriesPoint {
  /** Bucket start, Unix ms. */
  bucket: number;
  key: string;
  costUsd: number;
  tokens: number;
  requests: number;
}

export interface AiUsageBreakdownRow {
  key: string;
  requests: number;
  costUsd: number;
  inputTokens: number;
  outputTokens: number;
  cacheReadTokens: number;
  cacheCreationTokens: number;
  sessions: number;
  lastSeen: number;
}

export interface AiUsageToolRow {
  tool: string;
  calls: number;
  failures: number;
  rejections: number;
  avgDurationMs: number;
  p95DurationMs: number;
}

export interface AiUsageErrorRow {
  statusCode: string;
  error: string;
  model: string;
  count: number;
  lastSeen: number;
}

export interface AiUsageOverview {
  summary: AiUsageSummary;
  /** Newest matching event in the range, Unix ms (0 when none). */
  latestEventAt: number;
  timeseries: AiUsageTimeseriesPoint[];
  bucketSeconds: number;
  users: AiUsageBreakdownRow[];
  models: AiUsageBreakdownRow[];
  tools: AiUsageToolRow[];
  errors: AiUsageErrorRow[];
  filterOptions: { users: string[]; models: string[]; terminals: string[] };
}

export interface AiUsageSession {
  sessionId: string;
  user: string;
  terminal: string;
  firstSeen: number;
  lastSeen: number;
  requests: number;
  costUsd: number;
  tokens: number;
  prompts: number;
  toolCalls: number;
  errors: number;
  models: string[];
}

export interface AiUsageSessionsResponse {
  sessions: AiUsageSession[];
  total: number;
}

export interface AiUsageSessionEvent {
  /** Unix ms. */
  timestamp: number;
  /** Without the "claude_code." prefix, e.g. "api_request". */
  event: string;
  fields: Record<string, string>;
}

export interface AiUsageSessionEventsResponse {
  events: AiUsageSessionEvent[];
  truncated: boolean;
}

export interface AiUsageEventsParams extends AiUsageFilter {
  event?: string;
  sessionId?: string;
  search?: string;
  limit: number;
  offset: number;
}

export interface AiUsageEventsResponse {
  /** Newest first. */
  events: AiUsageSessionEvent[];
  total: number;
  eventTypes: string[];
}
