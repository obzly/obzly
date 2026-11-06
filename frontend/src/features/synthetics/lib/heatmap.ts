import type { PromQLSeries } from '../../../entity/synthetics';

const HOUR_MS = 3_600_000;

/** The heatmap's window: whole local clock hours, ending with the current one. */
export const hourWindow = (seconds: number, now = Date.now()): { from: number; to: number } => {
  const hour = new Date(now);
  hour.setMinutes(0, 0, 0);
  const to = hour.getTime() + HOUR_MS;
  return { from: to - seconds * 1000, to };
};

export interface HeatmapGrid {
  names: string[];
  /** Each column's hour start. */
  hours: number[];
  /** [column, row, percent passed]. */
  cells: [number, number, number][];
}

/**
 * Hourly pass ratios per location as heatmap cells. The point at t covers the
 * hour before it, so its column is that hour's start, and the point at `from`
 * (the hour before the range) is dropped.
 */
export const heatmapGrid = (series: PromQLSeries[], from: number): HeatmapGrid => {
  const names = series.map((item) => item.labels.location).sort();
  const starts = new Set<number>();
  for (const item of series) {
    for (const point of item.points) {
      const at = Date.parse(point.timestamp);
      if (at > from) starts.add(at - HOUR_MS);
    }
  }
  const hours = [...starts].sort((a, b) => a - b);
  const column = new Map(hours.map((hour, index) => [hour, index]));
  const row = new Map(names.map((name, index) => [name, index]));
  const cells: [number, number, number][] = [];
  for (const item of series) {
    for (const point of item.points) {
      const index = column.get(Date.parse(point.timestamp) - HOUR_MS);
      if (index === undefined) continue;
      cells.push([index, row.get(item.labels.location) ?? 0, Math.round(point.value * 1000) / 10]);
    }
  }
  return { names, hours, cells };
};

const clock = new Intl.DateTimeFormat(undefined, { hour: '2-digit', minute: '2-digit' });
const dayAndClock = new Intl.DateTimeFormat(undefined, {
  day: 'numeric',
  month: 'short',
  hour: '2-digit',
  minute: '2-digit',
});

/** A column label: the hour, with the date when the range spans days. */
export const hourLabel = (hour: number, withDate: boolean): string =>
  (withDate ? dayAndClock : clock).format(hour);
