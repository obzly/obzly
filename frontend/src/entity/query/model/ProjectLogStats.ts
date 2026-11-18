export interface LogsStats {
  totalLogs: number;
  totalSizeMb: number;
  rawSizeMb: number;
  /** Part of totalSizeMb already in object storage (0 without it); the rest is fresh segments on local disk. */
  objectStorageSizeMb?: number;
  oldestLogTime: string;
  newestLogTime: string;
}
