// A Site's data per hour, and data kept.
export type UsageSignal = 'logs' | 'metrics' | 'traces';

export interface UsageHour {
  hour: string;
  signal: UsageSignal;
  rawBytes: number;
  rows: number;
}

export interface UsageStorage {
  signal: UsageSignal;
  /** Compressed, as stored. */
  bytes: number;
  rawBytes: number;
  oldest: string | null;
}

export interface SystemUsage {
  hours: UsageHour[];
  storage: UsageStorage[];
}
