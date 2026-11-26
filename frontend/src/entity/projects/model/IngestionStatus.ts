export type IngestionHealth = 'sending' | 'quiet' | 'stopped';

export type IngestionSignal = 'logs' | 'traces' | 'metrics';

export type SourceIdentifiedBy =
  'obzly_source' | 'k8s_workload' | 'service' | 'k8s_cluster' | 'host' | 'unidentified';

export interface IngestionSignalStatus {
  signal: IngestionSignal;
  health: IngestionHealth;
  firstSeen: string;
  lastSeen: string;
  /** Records (logs, spans, metric data points) accepted in the rate window. */
  recentCount: number;
  perMinute: number;
}

export interface IngestionSourceLocation {
  host?: string;
  cluster?: string;
  namespace?: string;
  workloadKind?: string;
  workload?: string;
  serviceName?: string;
  serviceNamespace?: string;
  sdkLanguage?: string;
}

export interface IngestionSource {
  id: string;
  name: string;
  identifiedBy: SourceIdentifiedBy;
  /** Data Sources catalog id, when recognisable. */
  dataSource?: string;
  location: IngestionSourceLocation;
  health: IngestionHealth;
  firstSeen: string;
  lastSeen: string;
  signals: IngestionSignalStatus[];
}

/** Records a receiver refused for one signal and reason. */
export interface IngestionRefusal {
  signal: IngestionSignal;
  /** An error code such as RATE_LIMIT_EXCEEDED or LOG_TOO_LARGE, or OTHER. */
  reason: string;
  count: number;
  lastAt: string;
  /** The sender was told to retry (429/503): late, not lost. Otherwise dropped. */
  retried: boolean;
}

export interface IngestionStatus {
  sources: IngestionSource[];
  truncated: boolean;
  generatedAt: string;
  quietAfterMinutes: number;
  stoppedAfterHours: number;
  rateWindowMinutes: number;
  /** Refused in the last refusedWindowHours, most first; absent from older Sites. */
  refused?: IngestionRefusal[];
  refusedWindowHours?: number;
}
