export interface SpansStats {
  totalSpans: number;
  totalSizeMb: number;
  rawSizeMb: number;
  /** Part of totalSizeMb already in object storage (0 without it); the rest is fresh segments on local disk. */
  objectStorageSizeMb?: number;
  oldestSpanTime: string;
  newestSpanTime: string;
}
