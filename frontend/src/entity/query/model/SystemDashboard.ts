export interface SystemDashboard {
  totalLogs: number;
  totalSizeMb: number;
  rawSizeMb: number;
  oldestLogTime: string;
  newestLogTime: string;

  ingestionWindowMinutes: number;
  ingestedCount: number;
  ingestedRawBytes: number;
  avgIngestionRatePerMin: number;

  cpuPercent: number;
  memoryUsedBytes: number;
  memoryTotalBytes: number;
  diskUsedBytes: number;
  diskTotalBytes: number;
}
