export interface MetricsStats {
  totalSamples: number;
  totalSizeMb: number;
  rawSizeMb: number;
  oldestSampleTime: string;
  newestSampleTime: string;
}
