// Weekday × hour grid of requests from hourly counts, in the viewer's time zone.

export interface LocalHour {
  /** A key per calendar day, e.g. "2026-11-18". */
  day: string;
  weekday: string;
  hour: number;
}

export interface HeatmapRow {
  day: string;
  label: string;
  cells: number[];
}

export interface Heatmap {
  rows: HeatmapRow[];
  max: number;
  busiest: { label: string; hour: number } | null;
  quietestHour: number | null;
}

const pad = (n: number) => String(n).padStart(2, '0');

export const localHour = (date: Date): LocalHour => ({
  day: `${date.getFullYear()}-${pad(date.getMonth() + 1)}-${pad(date.getDate())}`,
  weekday: date.toLocaleDateString('en-US', { weekday: 'short' }),
  hour: date.getHours(),
});

/** The last 7 calendar days (oldest first) up to now, each with 24 hourly cells. */
export const buildHeatmap = (
  hours: { hour: string; requests: number }[],
  now: Date,
  toLocal: (date: Date) => LocalHour = localHour,
): Heatmap => {
  const rows: HeatmapRow[] = [];
  for (let back = 6; back >= 0; back--) {
    const local = toLocal(new Date(now.getTime() - back * 86_400_000));
    rows.push({ day: local.day, label: local.weekday, cells: new Array(24).fill(0) });
  }
  const byDay = new Map(rows.map((row) => [row.day, row]));
  for (const item of hours) {
    const local = toLocal(new Date(item.hour));
    const row = byDay.get(local.day);
    if (row) row.cells[local.hour] += item.requests;
  }
  let max = 0;
  let busiest: Heatmap['busiest'] = null;
  const perHour = new Array(24).fill(0);
  for (const row of rows) {
    row.cells.forEach((value, hour) => {
      perHour[hour] += value;
      if (value > max) {
        max = value;
        busiest = { label: row.label, hour };
      }
    });
  }
  const anyRequests = perHour.some((v) => v > 0);
  const quietestHour = anyRequests ? perHour.indexOf(Math.min(...perHour)) : null;
  return { rows, max, busiest, quietestHour };
};
