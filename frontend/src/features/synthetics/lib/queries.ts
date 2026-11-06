// PromQL for synthetics_* metrics. Older probes split one monitor's results
// per location into several series (tls_verified, service_version), so every
// query aggregates by the labels it means (check_id, location, phase, step)
// instead of trusting one series per location.
import type { PromQLSeries } from '../../../entity/synthetics';

/** `metric{a,b}`, or the bare metric without matchers. */
export const selector = (metric: string, ...matchers: (string | undefined)[]): string => {
  const kept = matchers.filter((matcher): matcher is string => !!matcher);
  return kept.length ? `${metric}{${kept.join(',')}}` : metric;
};

/**
 * Matches the given monitors, or nothing when they are every monitor of the
 * destination: only probes write synthetics_* there, and a 250-monitor
 * regex is ~9 KB per query. Callers drop series of unknown monitors.
 */
export const idsMatcher = (ids: string[], total: number): string | undefined =>
  ids.length >= total ? undefined : `check_id=~"${ids.join('|')}"`;

/**
 * Mean of a metric over windowSeconds, every sample counted once whichever
 * series it is in (an average of per-series averages would weigh a short
 * series of timeouts like the rest).
 */
export const mean = (by: string, metricSelector: string, windowSeconds: number): string => {
  const group = by ? ` by (${by})` : '';
  return `sum${group} (sum_over_time(${metricSelector}[${windowSeconds}s])) / sum${group} (count_over_time(${metricSelector}[${windowSeconds}s]))`;
};

/** Share of runs that passed over windowSeconds. */
export const passRatio = (by: string, match: string | undefined, windowSeconds: number): string =>
  mean(by, selector('synthetics_up', match), windowSeconds);

/**
 * A quantile per `by` over windowSeconds of the samples of every series
 * merged: the subquery takes each resolution-long window's sample (at most
 * one run when resolution is under the interval), so a short series of
 * timeouts doesn't stand for the whole monitor as quantile_over_time per
 * series and max would.
 */
export const mergedQuantile = (
  quantile: number,
  by: string,
  metricSelector: string,
  windowSeconds: number,
  resolution: number,
): string =>
  `quantile_over_time(${quantile}, (max by (${by}) (max_over_time(${metricSelector}[${resolution}s])))[${windowSeconds}s:${resolution}s])`;

/** Subquery resolution for a monitor interval: under half of it, so two runs rarely share a window. */
export const quantileResolution = (intervalSeconds: number): number =>
  Math.max(30, Math.floor(intervalSeconds / 2));

/** How far back the latest run counts: two missed runs and a minute of slack. */
export const statusWindow = (intervalSeconds: number): number => 2 * intervalSeconds + 60;

/** Series grouped by one label's value. */
export const indexBy = (series: PromQLSeries[], label: string): Map<string, PromQLSeries[]> => {
  const index = new Map<string, PromQLSeries[]>();
  for (const item of series) {
    const key = item.labels[label] ?? '';
    const list = index.get(key);
    if (list) list.push(item);
    else index.set(key, [item]);
  }
  return index;
};

/** Items grouped by a key, in first-seen order. */
export const groupBy = <T, K>(items: T[], keyOf: (item: T) => K): Map<K, T[]> => {
  const groups = new Map<K, T[]>();
  for (const item of items) {
    const key = keyOf(item);
    const list = groups.get(key);
    if (list) list.push(item);
    else groups.set(key, [item]);
  }
  return groups;
};

export const lastValue = (series?: PromQLSeries): number | undefined =>
  series?.points[series.points.length - 1]?.value;

/** Each label value's latest value (instant queries have one point per series). */
export const latestBy = (series: PromQLSeries[], label: string): Map<string, number> => {
  const values = new Map<string, number>();
  for (const item of series) {
    const value = lastValue(item);
    if (value !== undefined) values.set(item.labels[label] ?? '', value);
  }
  return values;
};

const keyOf = (series: PromQLSeries, labels: string[]) =>
  labels.map((label) => series.labels[label] ?? '').join('\u0000');

const pick = (series: PromQLSeries, labels: string[]) =>
  Object.fromEntries(labels.map((label) => [label, series.labels[label] ?? '']));

/**
 * Σ sums / Σ counts per key labels, from instant sum_over_time and
 * count_over_time series, e.g. a step's success over the chosen locations.
 */
export const ratioBy = (
  sums: PromQLSeries[],
  counts: PromQLSeries[],
  labels: string[],
): PromQLSeries[] => {
  const totals = new Map<string, { labels: Record<string, string>; sum: number; count: number }>();
  const add = (series: PromQLSeries, field: 'sum' | 'count') => {
    const value = lastValue(series);
    if (value === undefined) return;
    const key = keyOf(series, labels);
    const total = totals.get(key) ?? { labels: pick(series, labels), sum: 0, count: 0 };
    total[field] += value;
    totals.set(key, total);
  };
  sums.forEach((series) => add(series, 'sum'));
  counts.forEach((series) => add(series, 'count'));
  return [...totals.values()]
    .filter((total) => total.count > 0)
    .map((total) => ({
      labels: total.labels,
      points: [{ timestamp: '', value: total.sum / total.count }],
    }));
};

/** One series per key labels, the highest value at each timestamp. */
export const maxBy = (series: PromQLSeries[], labels: string[]): PromQLSeries[] => {
  const merged = new Map<string, { labels: Record<string, string>; values: Map<string, number> }>();
  for (const item of series) {
    const key = keyOf(item, labels);
    const entry = merged.get(key) ?? { labels: pick(item, labels), values: new Map() };
    for (const point of item.points) {
      const current = entry.values.get(point.timestamp);
      entry.values.set(
        point.timestamp,
        current === undefined ? point.value : Math.max(current, point.value),
      );
    }
    merged.set(key, entry);
  }
  return [...merged.values()].map((entry) => ({
    labels: entry.labels,
    points: [...entry.values]
      .sort((a, b) => Date.parse(a[0]) - Date.parse(b[0]))
      .map(([timestamp, value]) => ({ timestamp, value })),
  }));
};

/** Whether two query answers hold the same series and points, to skip a re-render. */
export const sameSeries = (a: PromQLSeries[], b: PromQLSeries[]): boolean =>
  a.length === b.length &&
  a.every((item, index) => {
    const other = b[index];
    const keys = Object.keys(item.labels);
    return (
      keys.length === Object.keys(other.labels).length &&
      keys.every((key) => item.labels[key] === other.labels[key]) &&
      item.points.length === other.points.length &&
      item.points.every(
        (point, at) =>
          point.timestamp === other.points[at].timestamp && point.value === other.points[at].value,
      )
    );
  });
