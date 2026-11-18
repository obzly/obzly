import type { AggregationBucket, LogMeasure, PanelType } from '../../../entity/dashboards';
import type { ConditionNode, QueryNode } from '../../../entity/query';
import { normalizeAttributeName } from '../../../shared/lib/attributeNames';
import { timezoneShiftMs, unshiftTimezone } from '../../../shared/time';

// The pure maths behind Logs › Chart: chart kinds over the search, the
// weekday × hour and value-band heatmaps, range comparisons, the shape of a
// SQL result, and the search written as SQL.

export type ChartKind = 'series' | 'top' | 'table' | 'donut' | 'heatmap' | 'compare';
export type MeasureChoice = Extract<LogMeasure, 'count' | 'distinct' | 'avg' | 'p95'>;
export type CompareKind = 'prev' | 'day' | 'week' | 'deploy' | 'custom';
export type ValueUnit = 'ms' | 's' | 'bytes' | 'number';
export type ChangeTone = 'good' | 'bad' | 'neutral';

/** logs_1m's dimensions (backend aggregate_rollups.go rollupFields). */
export const ROLLUP_FIELDS: ReadonlySet<string> = new Set([
  'level',
  'service_name',
  'k8s_namespace_name',
  'k8s_container_name',
  'k8s_pod_name',
  'pattern_hash',
]);

/** Columns of SQL mode's "logs" table; anything else is read from `fields`. */
const SQL_COLUMNS = new Set(['timestamp', 'level', 'message', 'client_ip', 'trace_id', 'span_id']);

export const WEEKDAYS = ['Mon', 'Tue', 'Wed', 'Thu', 'Fri', 'Sat', 'Sun'] as const;

const HOUR_MS = 3_600_000;
const DAY_MS = 24 * HOUR_MS;

/** The dashboard panel kind a chart is saved as (heatmaps and comparisons aren't panels). */
export const PANEL_TYPE_FOR_KIND: Partial<Record<ChartKind, PanelType>> = {
  series: 'line',
  top: 'rankedList',
  table: 'table',
  donut: 'donut',
};

export const KIND_NAMES: Record<ChartKind, string> = {
  series: 'time series',
  top: 'top values',
  table: 'table',
  donut: 'donut',
  heatmap: 'heatmap',
  compare: 'comparison',
};

/** "lines", "distinct trace_id", "avg duration_ms", "p95 duration_ms". */
export const measureLabel = (measure: LogMeasure, field?: string): string => {
  if (measure === 'count') return 'lines';
  return `${measure} ${field || '…'}`;
};

/** A count (or a distinct count) adds up to a whole; an average or percentile doesn't. */
export const isAdditiveMeasure = (measure: LogMeasure): boolean => measure === 'count';

/** Donuts draw counts only. */
export const donutFits = (measure: LogMeasure): boolean =>
  measure === 'count' || measure === 'distinct';

export const unitForField = (field?: string): ValueUnit => {
  const name = (field ?? '').toLowerCase();
  if (/(^|_)(ms|millis|milliseconds)$/.test(name) || /duration|latency|elapsed/.test(name)) {
    return 'ms';
  }
  if (/(^|_)(s|sec|secs|seconds)$/.test(name)) return 's';
  if (/bytes|size/.test(name)) return 'bytes';
  return 'number';
};

const trimNumber = (value: number, digits: number): string =>
  Number(value.toFixed(digits)).toLocaleString('en-US', { maximumFractionDigits: digits });

export const formatBytes = (value: number): string => {
  const units = ['B', 'KB', 'MB', 'GB', 'TB'];
  let scaled = value;
  let unit = 0;
  while (Math.abs(scaled) >= 1024 && unit < units.length - 1) {
    scaled /= 1024;
    unit++;
  }
  return `${trimNumber(scaled, scaled >= 100 || unit === 0 ? 0 : 1)} ${units[unit]}`;
};

/** A value in its field's unit: "1.94 s", "412 ms", "2.4 MB", "8,120". */
export const formatInUnit = (value: number, unit: ValueUnit): string => {
  switch (unit) {
    case 'ms':
      if (Math.abs(value) >= 1000) return `${trimNumber(value / 1000, 2)} s`;
      return `${trimNumber(value, Math.abs(value) < 10 ? 1 : 0)} ms`;
    case 's':
      return `${trimNumber(value, 2)} s`;
    case 'bytes':
      return formatBytes(value);
    default:
      return trimNumber(value, 2);
  }
};

/** A measure's value; "n/a" when unknown, never 0. */
export const formatMeasureValue = (
  value: number | null | undefined,
  measure: LogMeasure,
  field?: string,
): string => {
  if (value === null || value === undefined || Number.isNaN(value)) return 'n/a';
  if (measure === 'count' || measure === 'distinct')
    return Math.round(value).toLocaleString('en-US');
  return formatInUnit(value, unitForField(field));
};

const normalizeField = (field: string): string => normalizeAttributeName(field.trim());

/** Whether the query only filters on logs_1m's dimensions (backend onRollupFields). */
export const onRollupFields = (node: QueryNode | null | undefined): boolean => {
  if (!node) return true;
  if (node.condition) return ROLLUP_FIELDS.has(normalizeField(node.condition.field));
  return (node.logic?.children ?? []).every(onRollupFields);
};

/** Whether the backend answers this chart from the per-minute counts. */
export const usesRollups = (
  query: QueryNode | null | undefined,
  split: string,
  measure: LogMeasure,
): boolean =>
  measure === 'count' &&
  (!split || ROLLUP_FIELDS.has(normalizeField(split))) &&
  onRollupFields(query);

export interface TopRow {
  key: string;
  value: number;
  /** The row's part of the total; null when the measure doesn't add up. */
  share: number | null;
}

/** Values per group, largest first (summed over buckets when they carry time). */
export const topRows = (buckets: AggregationBucket[], additive: boolean): TopRow[] => {
  const totals = new Map<string, number>();
  for (const bucket of buckets) {
    const key = bucket.groupKey ?? '';
    totals.set(key, (totals.get(key) ?? 0) + bucket.value);
  }
  const total = [...totals.values()].reduce((sum, value) => sum + value, 0);
  return [...totals.entries()]
    .map(([key, value]) => ({
      key,
      value,
      share: additive && total > 0 ? value / total : null,
    }))
    .sort((a, b) => b.value - a.value || a.key.localeCompare(b.key));
};

export interface GridSeries {
  key: string;
  values: (number | null)[];
}

/** Bucket start times from `fromMs` to `toMs` on the backend's epoch-aligned grid. */
export const timeGrid = (fromMs: number, toMs: number, bucketMs: number): number[] => {
  const grid: number[] = [];
  for (let t = Math.floor(fromMs / bucketMs) * bucketMs; t <= toMs; t += bucketMs) grid.push(t);
  return grid;
};

/**
 * One row of values per group on the time grid, largest group first. Empty
 * buckets are `fill`: 0 for a count, null (a gap) for an average.
 */
export const seriesOnGrid = (
  buckets: AggregationBucket[],
  grid: number[],
  fill: number | null,
): GridSeries[] => {
  const index = new Map(grid.map((t, i) => [t, i]));
  const byKey = new Map<string, (number | null)[]>();
  const totals = new Map<string, number>();
  for (const bucket of buckets) {
    if (!bucket.timestamp) continue;
    const i = index.get(Date.parse(bucket.timestamp));
    if (i === undefined) continue;
    const key = bucket.groupKey ?? '';
    const values = byKey.get(key) ?? grid.map(() => fill);
    values[i] = (values[i] ?? 0) + bucket.value;
    byKey.set(key, values);
    totals.set(key, (totals.get(key) ?? 0) + bucket.value);
  }
  return [...byKey.entries()]
    .sort((a, b) => (totals.get(b[0]) ?? 0) - (totals.get(a[0]) ?? 0) || a[0].localeCompare(b[0]))
    .map(([key, values]) => ({ key, values }));
};

/** The first bucket each group has lines in (bucket precision). */
export const firstSeen = (buckets: AggregationBucket[]): Map<string, number> => {
  const first = new Map<string, number>();
  for (const bucket of buckets) {
    if (!bucket.timestamp || bucket.value <= 0) continue;
    const key = bucket.groupKey ?? '';
    const ts = Date.parse(bucket.timestamp);
    const known = first.get(key);
    if (known === undefined || ts < known) first.set(key, ts);
  }
  return first;
};

// ---- Weekday × hour --------------------------------------------------------

/** Hourly buckets unless the timezone is off the hour (India, Nepal): then quarter hours. */
export const heatmapBucketMs = (atMs: number, timezone: string): number =>
  (timezoneShiftMs(atMs, timezone) / 60_000) % 60 === 0 ? HOUR_MS : 15 * 60_000;

/** Lines per weekday (Monday first) and hour of the day, in the timezone. */
export const foldWeekdayHour = (buckets: AggregationBucket[], timezone: string): number[][] => {
  const grid = WEEKDAYS.map(() => Array.from({ length: 24 }, () => 0));
  for (const bucket of buckets) {
    if (!bucket.timestamp) continue;
    const ts = Date.parse(bucket.timestamp);
    const wall = new Date(ts + timezoneShiftMs(ts, timezone));
    grid[(wall.getUTCDay() + 6) % 7][wall.getUTCHours()] += bucket.value;
  }
  return grid;
};

/** The newest occurrence of a weekday's hour inside [fromMs, toMs], or null. */
export const latestSlot = (
  weekday: number,
  hour: number,
  fromMs: number,
  toMs: number,
  timezone: string,
): { from: number; to: number } | null => {
  const wallEnd = toMs + timezoneShiftMs(toMs, timezone);
  let wall = Math.floor(wallEnd / HOUR_MS) * HOUR_MS;
  for (let i = 0; i < 24 * 7 * 6; i++, wall -= HOUR_MS) {
    const date = new Date(wall);
    if ((date.getUTCDay() + 6) % 7 !== weekday || date.getUTCHours() !== hour) continue;
    const start = unshiftTimezone(wall, timezone);
    if (start + HOUR_MS <= fromMs) return null;
    return { from: Math.max(start, fromMs), to: Math.min(start + HOUR_MS, toMs) };
  }
  return null;
};

export interface WeekdayHourPeak {
  weekday: number;
  hour: number;
  value: number;
  /** How many times the average hour; null when every hour is empty. */
  timesAverage: number | null;
}

export const weekdayHourPeak = (grid: number[][]): WeekdayHourPeak | null => {
  let best: WeekdayHourPeak | null = null;
  let total = 0;
  grid.forEach((row, weekday) =>
    row.forEach((value, hour) => {
      total += value;
      if (!best || value > best.value) best = { weekday, hour, value, timesAverage: null };
    }),
  );
  if (!best || total === 0) return null;
  const peak: WeekdayHourPeak = best;
  return { ...peak, timesAverage: peak.value / (total / (7 * 24)) };
};

// ---- Value bands -----------------------------------------------------------

const mantissa = (value: number): number => value / 10 ** Math.floor(Math.log10(value) + 1e-9);

/** The band above a 1-2-5 band's lower bound (1 → 2 → 5 → 10). */
export const nextBand = (lower: number): number => {
  if (lower <= 0) return 0;
  const m = mantissa(lower);
  const factor = m < 1.5 ? 2 : m < 3.5 ? 2.5 : 2;
  return Number((lower * factor).toPrecision(6));
};

/** "500 ms–1 s", "1 KB–2 KB", "≤ 0". */
export const bandLabel = (lower: number, field?: string): string => {
  if (lower <= 0) return '≤ 0';
  const unit = unitForField(field);
  return `${formatInUnit(lower, unit)}–${formatInUnit(nextBand(lower), unit)}`;
};

export interface BandMatrix {
  columns: number[];
  /** Band lower bounds, largest first, every band between the extremes. */
  bands: number[];
  /** cells[band row][column]. */
  cells: number[][];
  max: number;
}

// More rows than this would be too thin to see.
const MAX_BAND_ROWS = 24;

export const bandMatrix = (buckets: AggregationBucket[], columns: number[]): BandMatrix => {
  const counts = new Map<number, Map<number, number>>();
  for (const bucket of buckets) {
    if (!bucket.timestamp || bucket.groupKey === undefined || bucket.groupKey === '') continue;
    const band = Number(bucket.groupKey);
    if (!Number.isFinite(band)) continue;
    const row = counts.get(band) ?? new Map<number, number>();
    const ts = Date.parse(bucket.timestamp);
    row.set(ts, (row.get(ts) ?? 0) + bucket.value);
    counts.set(band, row);
  }
  const positive = [...counts.keys()].filter((b) => b > 0).sort((a, b) => a - b);
  const bands: number[] = [];
  if (positive.length > 0) {
    const top = positive[positive.length - 1];
    for (
      let b = positive[0];
      b <= top * 1.000001 && bands.length < MAX_BAND_ROWS;
      b = nextBand(b)
    ) {
      bands.push(b);
    }
  }
  bands.reverse();
  if (counts.has(0)) bands.push(0);
  let max = 0;
  const cells = bands.map((band) => {
    const row = counts.get(band) ?? findBand(counts, band);
    return columns.map((t) => {
      const value = row?.get(t) ?? 0;
      max = Math.max(max, value);
      return value;
    });
  });
  return { columns, bands, cells, max };
};

// Bands rebuilt by nextBand can differ from the backend's key by float noise.
const findBand = (counts: Map<number, Map<number, number>>, band: number) => {
  for (const [key, row] of counts) {
    if (Math.abs(key - band) <= band * 1e-6) return row;
  }
  return undefined;
};

/** The band holding most lines, and a band that only shows up in the last quarter. */
export const bandFindings = (
  matrix: BandMatrix,
): { dominant: number | null; newBand: number | null; newSince: number | null } => {
  const { bands, cells, columns } = matrix;
  if (bands.length === 0 || columns.length === 0) {
    return { dominant: null, newBand: null, newSince: null };
  }
  const totals = cells.map((row) => row.reduce((a, b) => a + b, 0));
  const dominant = bands[totals.indexOf(Math.max(...totals))];
  const cut = Math.floor(columns.length * 0.75);
  let newBand: number | null = null;
  let newSince: number | null = null;
  let best = 0;
  cells.forEach((row, i) => {
    const before = row.slice(0, cut).reduce((a, b) => a + b, 0);
    const after = row.slice(cut).reduce((a, b) => a + b, 0);
    if (after > best && before <= after * 0.05 && bands[i] !== dominant) {
      best = after;
      newBand = bands[i];
      const firstColumn = row.findIndex((v, j) => j >= cut && v > 0);
      newSince = firstColumn >= 0 ? columns[firstColumn] : null;
    }
  });
  return { dominant, newBand, newSince };
};

/** The search clauses for a band, `field>=lower field<upper` (`field<=0` for the zero band). */
export const bandClauses = (field: string, lower: number): string[] =>
  lower <= 0 ? [`${field}<=0`] : [`${field}>=${lower}`, `${field}<${nextBand(lower)}`];

// ---- Compare ---------------------------------------------------------------

export interface CompareWindows {
  current: { from: number; to: number };
  baseline: { from: number; to: number };
}

/**
 * The two windows a comparison reads. Shifted kinds keep the search range and
 * move a copy back; around a rollout, the halves before and after it (the
 * same length, capped at now).
 */
export const comparisonWindows = (
  kind: CompareKind,
  fromMs: number,
  toMs: number,
  options: { rolloutMs?: number; customStartMs?: number; nowMs: number },
): CompareWindows | null => {
  const length = toMs - fromMs;
  const shifted = (by: number): CompareWindows => ({
    current: { from: fromMs, to: toMs },
    baseline: { from: fromMs - by, to: toMs - by },
  });
  switch (kind) {
    case 'prev':
      return shifted(length);
    case 'day':
      return shifted(DAY_MS);
    case 'week':
      return shifted(7 * DAY_MS);
    case 'custom':
      if (options.customStartMs === undefined) return null;
      return {
        current: { from: fromMs, to: toMs },
        baseline: { from: options.customStartMs, to: options.customStartMs + length },
      };
    case 'deploy': {
      if (options.rolloutMs === undefined) return null;
      const t = options.rolloutMs;
      const after = Math.min(t + length / 2, options.nowMs) - t;
      if (after <= 0) return null;
      return { current: { from: t, to: t + after }, baseline: { from: t - after, to: t } };
    }
  }
};

export interface DeltaRow {
  key: string;
  current: number;
  baseline: number;
  diff: number;
  change: string;
  tone: ChangeTone;
  /** Under the threshold: greyed out. */
  small: boolean;
}

/**
 * Per-group change between two equal-length windows, largest absolute change
 * first. More matching lines is bad, fewer is good; changes under `minLines`
 * are neutral.
 */
export const deltaRows = (
  current: { key: string; value: number }[],
  baseline: { key: string; value: number }[],
  minLines = 20,
): DeltaRow[] => {
  const now = new Map(current.map((r) => [r.key, r.value]));
  const then = new Map(baseline.map((r) => [r.key, r.value]));
  const keys = new Set([...now.keys(), ...then.keys()]);
  return [...keys]
    .map((key): DeltaRow => {
      const cur = now.get(key) ?? 0;
      const base = then.get(key) ?? 0;
      const diff = cur - base;
      const small = Math.abs(diff) < minLines;
      const change =
        base === 0
          ? cur > 0
            ? 'new'
            : '0%'
          : `${diff > 0 ? '+' : ''}${Math.round((diff / base) * 100)}%`;
      const tone: ChangeTone = small || diff === 0 ? 'neutral' : diff > 0 ? 'bad' : 'good';
      return { key, current: cur, baseline: base, diff, change, tone, small };
    })
    .sort((a, b) => Math.abs(b.diff) - Math.abs(a.diff) || a.key.localeCompare(b.key));
};

/** "5.7× higher", "2.1× lower", "about the same"; null when either side is unknown. */
export const ratioText = (current: number | null, baseline: number | null): string | null => {
  if (current === null || baseline === null) return null;
  if (baseline === 0) return current === 0 ? 'about the same' : null;
  const ratio = current / baseline;
  if (ratio >= 1.1) return `${trimNumber(ratio, 1)}× higher`;
  if (ratio <= 0.9 && current > 0) return `${trimNumber(1 / ratio, 1)}× lower`;
  if (current === 0) return 'down to none';
  return 'about the same';
};

// ---- The search as SQL -----------------------------------------------------

const sqlString = (value: unknown): string => `'${String(value).replace(/'/g, "''")}'`;

const jsonPath = (field: string): string =>
  sqlString(`$."${field.replace(/\\/g, '\\\\').replace(/"/g, '\\"')}"`);

/** A field as SQL mode reads it: a column, else text from the fields JSON. */
export const sqlField = (field: string): string => {
  const name = normalizeField(field);
  if (name === 'level') return 'lower(level)';
  if (SQL_COLUMNS.has(name)) return name;
  return `json_extract_string(fields, ${jsonPath(name)})`;
};

const sqlNumber = (field: string): string => `TRY_CAST(${sqlField(field)} AS DOUBLE)`;

const RANGE_SQL: Record<string, string> = {
  greater_than: '>',
  greater_or_equal: '>=',
  less_than: '<',
  less_or_equal: '<=',
};

const conditionSql = (condition: ConditionNode): string | null => {
  const name = normalizeField(condition.field);
  if (name === 'pattern_hash') return null; // computed by the backend, not a column
  const column = sqlField(name);
  const value = name === 'level' ? String(condition.value ?? '').toLowerCase() : condition.value;
  const list = (Array.isArray(value) ? value : [value]).map(sqlString).join(', ');
  switch (condition.operator) {
    case 'equals':
      return `${column} = ${sqlString(value)}`;
    case 'not_equals':
      return `(${column} IS NULL OR ${column} <> ${sqlString(value)})`;
    case 'contains':
      return `contains(${column}, ${sqlString(value)})`;
    case 'not_contains':
      return `(${column} IS NULL OR NOT contains(${column}, ${sqlString(value)}))`;
    case 'in':
      return `${column} IN (${list})`;
    case 'not_in':
      return `(${column} IS NULL OR ${column} NOT IN (${list}))`;
    case 'exists':
      return `(${column} IS NOT NULL AND ${column} <> '')`;
    case 'not_exists':
      return `(${column} IS NULL OR ${column} = '')`;
    default: {
      const op = RANGE_SQL[condition.operator];
      if (!op) return null;
      if (SQL_COLUMNS.has(name)) return `${column} ${op} ${sqlString(value)}`;
      return Number.isFinite(Number(value)) ? `${sqlNumber(name)} ${op} ${Number(value)}` : null;
    }
  }
};

/** The search as a SQL boolean over SQL mode's "logs" table; null when it can't be written. */
export const queryToSql = (node: QueryNode | null | undefined): string | null => {
  if (!node) return '';
  if (node.condition) return conditionSql(node.condition);
  const logic = node.logic;
  if (!logic || logic.children.length === 0) return '';
  const parts = logic.children.map(queryToSql);
  if (parts.some((p) => p === null)) return null;
  const present = (parts as string[]).filter((p) => p !== '');
  if (logic.operator === 'not') return present.length ? `NOT (${present.join(' AND ')})` : '';
  if (present.length === 0) return '';
  if (present.length === 1) return present[0];
  return `(${present.join(logic.operator === 'or' ? ' OR ' : ' AND ')})`;
};

const measureSql = (measure: LogMeasure, field?: string): string => {
  if (measure === 'count' || !field) return 'count(*)';
  if (measure === 'distinct') return `count(DISTINCT ${sqlField(field)})`;
  const quantile = { p50: '0.5', p95: '0.95', p99: '0.99' }[measure as string];
  if (quantile) return `quantile_cont(${sqlNumber(field)}, ${quantile})`;
  return `${measure}(${sqlNumber(field)})`;
};

export interface ChartSqlInput {
  query: QueryNode | null | undefined;
  kind: ChartKind;
  measure: LogMeasure;
  measureField?: string;
  split: string;
  top: number;
  bucketMs: number;
}

/** The SQL-mode statement behind a chart (null when the search can't be written as SQL). */
export const chartSql = (input: ChartSqlInput): string | null => {
  const where = queryToSql(input.query);
  if (where === null) return null;
  const alias = input.measure === 'count' ? 'lines' : `${input.measure}_value`;
  const value = `${measureSql(input.measure, input.measureField)} AS ${alias}`;
  const split = input.split ? `${sqlField(input.split)} AS ${normalizeField(input.split)}` : '';
  const filters = [
    where,
    input.measure === 'count' ? '' : `${sqlField(input.measureField ?? '')} IS NOT NULL`,
  ]
    .filter(Boolean)
    .join(' AND ');
  const whereClause = filters ? ` WHERE ${filters}` : '';
  if (input.kind === 'series' || input.kind === 'heatmap' || input.kind === 'compare') {
    const bucket = `time_bucket(INTERVAL '${Math.max(1, Math.round(input.bucketMs / 1000))} seconds', timestamp) AS bucket`;
    const columns = [bucket, split, value].filter(Boolean).join(', ');
    const groupBy = split ? 'GROUP BY 1, 2' : 'GROUP BY 1';
    return `SELECT ${columns} FROM logs${whereClause} ${groupBy} ORDER BY 1`;
  }
  if (!split) return `SELECT ${value} FROM logs${whereClause}`;
  return `SELECT ${split}, ${value} FROM logs${whereClause} GROUP BY 1 ORDER BY 2 DESC LIMIT ${input.top}`;
};

// ---- Query helpers ---------------------------------------------------------

export const inCondition = (field: string, values: string[]): QueryNode => ({
  type: 'condition',
  condition: { field, operator: 'in', value: values },
});

/** The nodes ANDed together, skipping empty ones; null when none is left. */
export const andQuery = (...nodes: (QueryNode | null | undefined)[]): QueryNode | null => {
  const present = nodes.filter((n): n is QueryNode => !!n);
  if (present.length === 0) return null;
  if (present.length === 1) return present[0];
  return { type: 'logical', logic: { operator: 'and', children: present } };
};

const NUMBER_HINT =
  /(^|_)(ms|s|sec|seconds|bytes|size|count|duration|latency|elapsed|status|code|len|length)($|_)/;

/**
 * Fields worth offering for a number (an average, a percentile, value bands):
 * promoted number fields first, then names that look numeric, then the rest.
 */
export const rankNumericFields = (
  fields: string[],
  promotedNumbers: ReadonlySet<string>,
): string[] => {
  const score = (name: string) =>
    promotedNumbers.has(name)
      ? 0
      : unitForField(name) !== 'number' || NUMBER_HINT.test(name)
        ? 1
        : 2;
  return [...fields].sort((a, b) => score(a) - score(b) || a.localeCompare(b));
};

/** The first field that looks numeric, for a measure's default. */
export const defaultNumericField = (
  fields: string[],
  promotedNumbers: ReadonlySet<string>,
): string | undefined =>
  rankNumericFields(fields, promotedNumbers).find(
    (name) =>
      promotedNumbers.has(name) || unitForField(name) !== 'number' || NUMBER_HINT.test(name),
  );
