// 0 means "Off".
export type AutoRefreshIntervalMs = 0 | 5000 | 10000 | 30000 | 60000 | 300000;

export const AUTO_REFRESH_OPTIONS: { label: string; value: AutoRefreshIntervalMs }[] = [
  { label: 'Off', value: 0 },
  { label: '5s', value: 5000 },
  { label: '10s', value: 10000 },
  { label: '30s', value: 30000 },
  { label: '1m', value: 60000 },
  { label: '5m', value: 300000 },
];
