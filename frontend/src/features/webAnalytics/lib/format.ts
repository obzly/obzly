// Pure formatting for the Web analytics view.

/** "2.41 M", "18.4 k", "912": compact counts as the board shows them. */
export const formatCount = (n: number): string => {
  if (n >= 1e9) return `${(n / 1e9).toFixed(2)} B`;
  if (n >= 1e6) return `${(n / 1e6).toFixed(2)} M`;
  if (n >= 1e4) return `${(n / 1e3).toFixed(1)} k`;
  return Math.round(n).toLocaleString('en-US');
};

/** Response body bytes as KB/MB/GB/TB (binary). */
export const formatBytes = (bytes: number): string => {
  const units = ['B', 'KB', 'MB', 'GB', 'TB', 'PB'];
  let value = bytes;
  let unit = 0;
  while (value >= 1024 && unit < units.length - 1) {
    value /= 1024;
    unit++;
  }
  if (unit === 0) return `${Math.round(value)} B`;
  return `${value >= 100 ? value.toFixed(0) : value.toFixed(1)} ${units[unit]}`;
};

/** A share (0–1) as a percent with one decimal, two below 1%. */
export const formatShare = (share: number): string => {
  if (!Number.isFinite(share)) return 'n/a';
  const percent = share * 100;
  return `${percent.toFixed(percent > 0 && percent < 1 ? 2 : 1)}%`;
};

export const formatMs = (ms: number): string =>
  ms >= 1000 ? `${(ms / 1000).toFixed(ms >= 10000 ? 0 : 2)} s` : `${Math.round(ms)} ms`;

let regionNames: Intl.DisplayNames | null | undefined;

/** An ISO country code's English name; "??" is a database miss. */
export const countryName = (code: string): string => {
  if (code === '??' || !code) return 'Unknown';
  if (regionNames === undefined) {
    try {
      regionNames = new Intl.DisplayNames(['en'], { type: 'region' });
    } catch {
      regionNames = null;
    }
  }
  try {
    return regionNames?.of(code.toUpperCase()) ?? code;
  } catch {
    return code;
  }
};

export type RangePreset = '15m' | '1h' | '24h' | '7d' | '30d';

export const RANGE_PRESETS: { value: RangePreset; label: string; text: string; ms: number }[] = [
  { value: '15m', label: '15m', text: 'last 15 minutes', ms: 15 * 60_000 },
  { value: '1h', label: '1h', text: 'last hour', ms: 3_600_000 },
  { value: '24h', label: '24h', text: 'last 24 hours', ms: 86_400_000 },
  { value: '7d', label: '7d', text: 'last 7 days', ms: 7 * 86_400_000 },
  { value: '30d', label: '30d', text: 'last 30 days', ms: 30 * 86_400_000 },
];

export const rangeStart = (preset: RangePreset, now: Date): Date => {
  const found = RANGE_PRESETS.find((p) => p.value === preset) ?? RANGE_PRESETS[2];
  return new Date(now.getTime() - found.ms);
};

const pad = (n: number) => String(n).padStart(2, '0');

/** "14:00", or "Mon 18" for buckets of a day or more. */
export const bucketLabel = (iso: string, bucketSeconds: number): string => {
  const date = new Date(iso);
  if (bucketSeconds >= 86_400) {
    return date.toLocaleDateString('en-US', { weekday: 'short', day: 'numeric' });
  }
  if (bucketSeconds >= 21_600) {
    return `${date.toLocaleDateString('en-US', { weekday: 'short' })} ${pad(date.getHours())}:00`;
  }
  return `${pad(date.getHours())}:${pad(date.getMinutes())}`;
};

/** Up to five evenly spaced axis labels for a series. */
export const axisLabels = (starts: string[], bucketSeconds: number): string[] => {
  if (starts.length === 0) return [];
  if (starts.length <= 5) return starts.map((s) => bucketLabel(s, bucketSeconds));
  const last = starts.length - 1;
  return [0, 1, 2, 3, 4].map((i) => bucketLabel(starts[Math.round((i * last) / 4)], bucketSeconds));
};

/** "13:04" in the viewer's time zone. */
export const clockTime = (iso: string): string => {
  const date = new Date(iso);
  return `${pad(date.getHours())}:${pad(date.getMinutes())}`;
};
