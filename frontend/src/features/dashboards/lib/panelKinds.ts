import type {
  AggregationBucket,
  Panel,
  PanelHigherIs,
  PanelSource,
  PanelThresholds,
  PanelType,
  SpanMetric,
} from '../../../entity/dashboards';
import type { ChangeTone, StripState } from '../../../shared/ui/v2';
import { formatByUnit } from '../ui/echartsOption';

// What each panel kind needs from its query, and the pure maths behind the v2
// kinds (ranked list, donut, gauge, stacked bars, status strips, stat with trend).

/** Kinds drawn per time bucket; the editor offers a "Time bucket" for these. */
export const isTimeBucketedType = (type: PanelType): boolean =>
  type === 'line' || type === 'bar' || type === 'stackedBar' || type === 'statusStrip';

/** Kinds that break a value down by group; logs panels of these need a group-by field. */
export const groupsIntoDiscreteValues = (type: PanelType): boolean =>
  type === 'pie' || type === 'table' || type === 'rankedList' || type === 'donut';

const ADDITIVE_SPAN_METRICS: SpanMetric[] = ['count', 'error_count'];

/**
 * Whether bucket values can be summed into a total (log counts, span counts).
 * A metric series or a span duration can't: its total is unknown.
 */
export const isAdditive = (panel: Pick<Panel, 'source' | 'metric' | 'measure'>): boolean => {
  const source = panel.source ?? 'logs';
  // A logs average, percentile or distinct count doesn't add up across buckets.
  if (source === 'logs')
    return !panel.measure || panel.measure === 'count' || panel.measure === 'sum';
  if (source === 'spans') return ADDITIVE_SPAN_METRICS.includes(panel.metric ?? 'count');
  return false;
};

/** Why a kind can't show this source/metric, or null when it can. */
export const kindUnavailableReason = (
  type: PanelType,
  source: PanelSource,
  metric: SpanMetric | undefined,
): string | null => {
  const isDuration = source === 'spans' && !ADDITIVE_SPAN_METRICS.includes(metric ?? 'count');
  if (isDuration && type === 'stackedBar') return "durations don't add up; use a line chart";
  if (isDuration && type === 'donut') return "durations aren't parts of a whole";
  return null;
};

/** The display settings a type keeps; everything else is dropped on save. */
export interface PanelSettings {
  min?: number;
  max?: number;
  thresholds?: PanelThresholds;
  higherIs?: PanelHigherIs;
}

export const usesRange = (type: PanelType): boolean => type === 'gauge';
export const usesThresholds = (type: PanelType): boolean =>
  type === 'gauge' || type === 'statusStrip';
export const usesHigherIs = (type: PanelType): boolean =>
  type === 'statTrend' || usesThresholds(type);

export const settingsForType = (type: PanelType, settings: PanelSettings): PanelSettings => {
  const out: PanelSettings = {};
  if (usesRange(type)) {
    if (settings.min !== undefined) out.min = settings.min;
    if (settings.max !== undefined) out.max = settings.max;
  }
  if (usesThresholds(type) && settings.thresholds) {
    const { warn, critical } = settings.thresholds;
    if (warn !== undefined || critical !== undefined) {
      out.thresholds = {
        ...(warn !== undefined && { warn }),
        ...(critical !== undefined && { critical }),
      };
    }
  }
  if (usesHigherIs(type) && settings.higherIs) out.higherIs = settings.higherIs;
  return out;
};

/** The editor's checks, mirroring the backend's; null when the settings are fine. */
export const validateSettings = (
  type: PanelType,
  settings: PanelSettings,
  unit: Panel['unit'],
): string | null => {
  const kept = settingsForType(type, settings);
  if (kept.min !== undefined && kept.max !== undefined && kept.min >= kept.max) {
    return 'The gauge minimum must be less than its maximum';
  }
  if (type === 'gauge' && kept.max === undefined && unit !== 'percent') {
    return 'A gauge needs a maximum (or the percent unit, which runs 0-100%)';
  }
  if (type === 'statusStrip' && !kept.thresholds) {
    return 'Status strips need a warn or critical threshold to colour the cells';
  }
  const { warn, critical } = kept.thresholds ?? {};
  if (warn !== undefined && critical !== undefined) {
    if (kept.higherIs === 'good' && warn < critical) {
      return 'When higher is good, the warn threshold must be at or above the critical one';
    }
    if (kept.higherIs !== 'good' && warn > critical) {
      return 'The warn threshold must be at or below the critical one';
    }
  }
  return null;
};

/** A threshold state for a value; null when the panel sets no thresholds. */
export const thresholdState = (
  value: number,
  thresholds: PanelThresholds | undefined,
  higherIs: PanelHigherIs | undefined,
): Exclude<StripState, 'none'> | null => {
  if (!thresholds || (thresholds.warn === undefined && thresholds.critical === undefined)) {
    return null;
  }
  const breaches = (limit: number | undefined) =>
    limit !== undefined && (higherIs === 'good' ? value <= limit : value >= limit);
  if (breaches(thresholds.critical)) return 'fail';
  if (breaches(thresholds.warn)) return 'warn';
  return 'ok';
};

/** Where a value sits on a gauge (0..1), or null when the gauge has no maximum. */
export const gaugeRatio = (
  value: number,
  min: number | undefined,
  max: number | undefined,
  unit: Panel['unit'],
): number | null => {
  const lo = min ?? 0;
  const hi = max ?? (unit === 'percent' ? 1 : undefined);
  if (hi === undefined || hi <= lo) return null;
  return Math.min(Math.max((value - lo) / (hi - lo), 0), 1);
};

/** A metrics panel returns every point of every series; summaries use each series' latest. */
export const latestPerSeries = (buckets: AggregationBucket[]): AggregationBucket[] => {
  const latest = new Map<string, AggregationBucket>();
  for (const bucket of buckets) {
    const key = bucket.groupKey ?? '';
    const current = latest.get(key);
    if (!current || (bucket.timestamp ?? '') >= (current.timestamp ?? '')) latest.set(key, bucket);
  }
  return [...latest.values()];
};

/** All series summed per timestamp, oldest first. */
export const totalPerTimestamp = (buckets: AggregationBucket[]): number[] => {
  const totals = new Map<string, number>();
  for (const bucket of buckets) {
    if (!bucket.timestamp) continue;
    totals.set(bucket.timestamp, (totals.get(bucket.timestamp) ?? 0) + bucket.value);
  }
  return [...totals.entries()].sort((a, b) => (a[0] < b[0] ? -1 : 1)).map(([, value]) => value);
};

/**
 * One number for a panel: the sum of its buckets, or for metrics the sum of
 * each series' latest point. Null when a metrics panel returned nothing
 * (no data, not zero); an empty logs or span count is a real 0.
 */
export const summaryValue = (buckets: AggregationBucket[], isMetrics: boolean): number | null => {
  const rows = isMetrics ? latestPerSeries(buckets) : buckets;
  if (isMetrics && rows.length === 0) return null;
  return rows.reduce((sum, bucket) => sum + bucket.value, 0);
};

/** Values per group (the latest for metrics), largest first. */
export const valuesByGroup = (
  buckets: AggregationBucket[],
  isMetrics: boolean,
): { key: string; value: number }[] => {
  const rows = isMetrics ? latestPerSeries(buckets) : buckets;
  const totals = new Map<string, number>();
  for (const bucket of rows) {
    const key = bucket.groupKey || '(none)';
    totals.set(key, (totals.get(key) ?? 0) + bucket.value);
  }
  return [...totals.entries()]
    .map(([key, value]) => ({ key, value }))
    .sort((a, b) => b.value - a.value);
};

/** The largest maxSlices-1 groups plus "Other" for the rest (a donut's slices). */
export const foldIntoOther = (
  rows: { key: string; value: number }[],
  maxSlices = 6,
): { key: string; value: number }[] => {
  if (rows.length <= maxSlices) return rows;
  const kept = rows.slice(0, maxSlices - 1);
  const rest = rows.slice(maxSlices - 1).reduce((sum, row) => sum + row.value, 0);
  return [...kept, { key: 'Other', value: rest }];
};

export interface ChangeText {
  text: string;
  tone: ChangeTone;
}

/**
 * A change pill against the previous window: percent change (points for a
 * percent unit), green or red by whether the direction is good, neutral
 * when the panel doesn't say. "n/a" when either side is unknown.
 */
export const changeVsPrevious = (
  current: number | null,
  previous: number | null,
  unit: Panel['unit'],
  higherIs: PanelHigherIs | undefined,
): ChangeText => {
  if (current === null || previous === null) return { text: 'n/a', tone: 'neutral' };

  const toneFor = (delta: number): ChangeTone => {
    if (delta === 0 || !higherIs) return 'neutral';
    return delta > 0 === (higherIs === 'good') ? 'good' : 'bad';
  };
  const signed = (n: number, digits: number) =>
    `${n > 0 ? '+' : '−'}${Math.abs(n).toFixed(digits)}`;

  if (unit === 'percent') {
    const points = (current - previous) * 100;
    if (Math.abs(points) < 0.05) return { text: 'steady', tone: 'neutral' };
    return { text: `${signed(points, 1)} pt`, tone: toneFor(points) };
  }
  if (previous === 0) {
    if (current === 0) return { text: 'steady', tone: 'neutral' };
    return { text: 'up from 0', tone: toneFor(current) };
  }
  const pct = ((current - previous) / Math.abs(previous)) * 100;
  const digits = Math.abs(pct) < 10 ? 1 : 0;
  if (Number(Math.abs(pct).toFixed(digits)) === 0) return { text: 'steady', tone: 'neutral' };
  return { text: `${signed(pct, digits)}%`, tone: toneFor(pct) };
};

/** "15m", "24h", "7d": a window length for "vs previous …". */
export const formatWindow = (ms: number): string => {
  const minutes = Math.round(ms / 60_000);
  if (minutes < 60) return `${Math.max(minutes, 1)}m`;
  const hours = Math.round(minutes / 60);
  if (hours < 72 || hours % 24 !== 0) return `${hours}h`;
  return `${hours / 24}d`;
};

interface Grid {
  fromMs: number;
  toMs: number;
  bucketMs: number;
}

// At most this many cells per strip; a longer range shows the newest ones.
const MAX_CELLS = 120;

const gridOf = ({ fromMs, toMs, bucketMs }: Grid) => {
  const start = Math.floor(fromMs / bucketMs) * bucketMs;
  const count = Math.min(Math.max(Math.ceil((toMs - start) / bucketMs), 1), MAX_CELLS);
  const first = start + Math.max(Math.ceil((toMs - start) / bucketMs) - count, 0) * bucketMs;
  return { first, count };
};

/**
 * One value per time cell for a set of buckets: summed when additive (an
 * empty cell is then 0), the latest point otherwise (an empty cell is null,
 * no data).
 */
export const valuesOnGrid = (
  buckets: AggregationBucket[],
  grid: Grid,
  additive: boolean,
): (number | null)[] => {
  const { first, count } = gridOf(grid);
  const values: (number | null)[] = new Array(count).fill(additive ? 0 : null);
  const latestAt: number[] = new Array(count).fill(Number.NEGATIVE_INFINITY);
  for (const bucket of buckets) {
    if (!bucket.timestamp) continue;
    const ts = Date.parse(bucket.timestamp);
    // Older than the newest MAX_CELLS cells.
    if (ts < first) continue;
    const index = Math.min(Math.floor((ts - first) / grid.bucketMs), count - 1);
    if (additive) {
      values[index] = (values[index] ?? 0) + bucket.value;
    } else if (ts >= latestAt[index]) {
      latestAt[index] = ts;
      values[index] = bucket.value;
    }
  }
  return values;
};

export interface StatusRow {
  key: string;
  cells: { state: StripState; title: string }[];
  latest: number | null;
  worst: StripState;
}

const STATE_RANK: Record<StripState, number> = { fail: 3, warn: 2, ok: 1, none: 0 };

/** One status strip per group, a cell per time bucket; the worst rows first. */
export const statusRows = (
  buckets: AggregationBucket[],
  grid: Grid,
  options: {
    additive: boolean;
    thresholds: PanelThresholds | undefined;
    higherIs: PanelHigherIs | undefined;
    unit: Panel['unit'];
    maxRows?: number;
  },
): StatusRow[] => {
  const byGroup = new Map<string, AggregationBucket[]>();
  for (const bucket of buckets) {
    const key = bucket.groupKey || '(none)';
    byGroup.set(key, [...(byGroup.get(key) ?? []), bucket]);
  }
  const { first } = gridOf(grid);

  const rows = [...byGroup.entries()].map(([key, groupBuckets]): StatusRow => {
    const values = valuesOnGrid(groupBuckets, grid, options.additive);
    const cells = values.map((value, i): StatusRow['cells'][number] => {
      const at = new Date(first + i * grid.bucketMs).toLocaleString();
      if (value === null) return { state: 'none', title: `${at} · no data` };
      const state: StripState =
        thresholdState(value, options.thresholds, options.higherIs) ?? 'none';
      return { state, title: `${at} · ${formatByUnit(value, options.unit)}` };
    });
    const latest = [...values].reverse().find((v): v is number => v !== null) ?? null;
    const worst = cells.reduce<StripState>(
      (acc, cell) => (STATE_RANK[cell.state] > STATE_RANK[acc] ? cell.state : acc),
      'none',
    );
    return { key, cells, latest, worst };
  });

  rows.sort((a, b) => STATE_RANK[b.worst] - STATE_RANK[a.worst] || a.key.localeCompare(b.key));
  return rows.slice(0, options.maxRows ?? 20);
};

export interface StackedSummary {
  peak: { timestamp: string; total: number } | null;
  // Null when values don't add up (metrics, span durations).
  total: number | null;
  // In first-appearance order, the order ECharts assigns series colours in.
  series: { key: string; colorIndex: number; total: number; share: number | null }[];
}

/** The footer of a stacked bar panel: its peak bucket, the total and each series' share. */
export const stackedSummary = (buckets: AggregationBucket[], additive: boolean): StackedSummary => {
  const perTimestamp = new Map<string, number>();
  const perSeries = new Map<string, number>();
  for (const bucket of buckets) {
    const key = bucket.groupKey || '';
    perSeries.set(key, (perSeries.get(key) ?? 0) + bucket.value);
    if (bucket.timestamp) {
      perTimestamp.set(bucket.timestamp, (perTimestamp.get(bucket.timestamp) ?? 0) + bucket.value);
    }
  }

  let peak: StackedSummary['peak'] = null;
  for (const [timestamp, total] of perTimestamp) {
    if (!peak || total > peak.total) peak = { timestamp, total };
  }
  const total = additive ? [...perSeries.values()].reduce((sum, v) => sum + v, 0) : null;
  const series = [...perSeries.entries()].map(([key, seriesTotal], colorIndex) => ({
    key,
    colorIndex,
    total: seriesTotal,
    share: total ? seriesTotal / total : null,
  }));
  return { peak, total, series };
};
