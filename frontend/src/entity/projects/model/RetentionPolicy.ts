export type RetentionSignal = 'logs' | 'traces' | 'metrics';

// Keeps `rate` of the logs of `service` (any if empty) at `level` (any if
// empty); the first matching rule wins.
export interface LogSampleRule {
  service: string;
  level: string;
  rate: number;
}

// One signal's retention and sampling for a project. Zero days mean the
// project's log retention.
export interface RetentionPolicy {
  projectId: string;
  signal: RetentionSignal;
  retentionDays: number;
  // metrics: keep 5 m / 1 h rollups this long after samples age out (0: off)
  rollupDays: number;
  // traces: share kept besides error and slow traces (1: all)
  sampleRate: number;
  // traces: always keep a trace with a span this slow (0: off)
  slowMs: number;
  // logs: head sampling at ingest
  sampleRules: LogSampleRule[];
  updatedAt?: string;
}
