// Mirrors backend internal/features/webanalytics/dto.go.

export interface WebAnalyticsAttribute {
  name: string;
  value: string;
}

export interface WebAnalyticsSource {
  key: string;
  name: string;
  defaultName: string;
  /** obzly_source, k8s_workload, service, k8s_cluster, host or unidentified. */
  identifiedBy: string;
  identity: WebAnalyticsAttribute[];
  kind: string;
  /** The log format, e.g. "combined", "JSON (Envoy)", "OTel names"; "" when unknown. */
  format: string;
  /** Normalized field name → the name it was read from. */
  fields: Record<string, string>;
  where: string;
  hosts: string[];
  requests: number;
  share: number;
  hasReferrer: boolean;
  hasUserAgent: boolean;
  hasDuration: boolean;
  hasTls: boolean;
  mergedFrom: string[];
  mergedInto?: string;
  firstSeen: string;
  lastSeen: string;
}

export interface WebAnalyticsTotals {
  requests: number;
  /** null when no selected source logs client addresses. */
  visitors: number | null;
  failed: number;
  notFound: number;
  bytes: number;
  botRequests: number;
  /** null when no selected source logs a duration. */
  p95Ms: number | null;
  durationSources: string[];
}

export interface WebAnalyticsBucket {
  start: string;
  live: boolean;
  requests: number;
  status2xx: number;
  status3xx: number;
  status4xx: number;
  status5xx: number;
  other: number;
  bytes: number;
  visitors: number;
}

export interface WebAnalyticsRow {
  key: string;
  hits: number;
  visitors?: number;
  bytes: number;
  errors5xx: number;
  lastSeen?: string;
  tag?: string;
  country?: string;
}

export interface WebAnalyticsPanels {
  pages: WebAnalyticsRow[];
  notFound: WebAnalyticsRow[];
  static: WebAnalyticsRow[];
  referrers: WebAnalyticsRow[];
  ips: WebAnalyticsRow[];
  browsers: WebAnalyticsRow[];
  systems: WebAnalyticsRow[];
  devices: WebAnalyticsRow[];
  countries: WebAnalyticsRow[];
  vhosts: WebAnalyticsRow[];
  methods: WebAnalyticsRow[];
  protocols: WebAnalyticsRow[];
  tls: WebAnalyticsRow[];
  status: WebAnalyticsRow[];
}

export interface WebAnalyticsGeo {
  enabled: boolean;
  available: boolean;
  databaseType?: string;
  attribution: string;
  error?: string;
}

export interface WebAnalyticsReport {
  from: string;
  to: string;
  bucketSeconds: number;
  rollups: {
    used: boolean;
    hours: number;
    from: string | null;
    to: string | null;
    lastBuiltAt: string | null;
    missing: number;
  };
  live: { from: string; to: string; rows: number; durationMs: number; cached: boolean };
  sources: WebAnalyticsSource[];
  selected: string[];
  hosts: { host: string; requests: number }[];
  totals: WebAnalyticsTotals;
  previous: WebAnalyticsTotals | null;
  series: WebAnalyticsBucket[];
  heatmap: { hour: string; requests: number }[];
  panels: WebAnalyticsPanels;
  geo: WebAnalyticsGeo;
  ipMode: WebAnalyticsIpMode;
  excludeBots: boolean;
}

export type WebAnalyticsIpMode = 'truncate' | 'hash' | 'full';

export interface WebAnalyticsSettings {
  ipMode: WebAnalyticsIpMode;
  /** 0 = like the logs. */
  retentionDays: number;
  logRetentionDays: number;
  excludeBots: boolean;
  staticExtensions: string;
  geoEnabled: boolean;
  geo: WebAnalyticsGeo;
  sources: WebAnalyticsSource[];
  build: { hour: string; builtAt: string; durationMs: number; rowsRead: number } | null;
  storage: { hours: number; bytes: number };
  schema: { name: string; label: string; aliases: string[] }[];
  canManage: boolean;
  buildDelayMinutes: number;
}

export interface WebAnalyticsSettingsUpdate {
  ipMode?: WebAnalyticsIpMode;
  retentionDays?: number;
  excludeBots?: boolean;
  staticExtensions?: string;
  geoEnabled?: boolean;
  sources?: { key: string; name: string; mergedInto: string }[];
}

export interface WebAnalyticsReportRequest {
  from: string;
  to?: string;
  sources?: string[];
  host?: string;
}
