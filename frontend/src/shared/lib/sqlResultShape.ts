// The shape of a SQL result (columns + rows): which charts it can draw and
// the data each one reads. Shared by Logs › SQL results and logs_sql
// dashboard panels, so a saved panel draws what the results card showed.

export type SqlChartKind = 'series' | 'top' | 'donut';

export interface SqlChartShape {
  kinds: SqlChartKind[];
  timeColumn: number | null;
  labelColumn: number | null;
  valueColumns: number[];
}

const ISO_DATE = /^\d{4}-\d{2}-\d{2}[T ]\d{2}:\d{2}/;

const isNumeric = (value: unknown): boolean =>
  (typeof value === 'number' && Number.isFinite(value)) ||
  (typeof value === 'string' && value.trim() !== '' && Number.isFinite(Number(value)));

const isTime = (value: unknown): boolean =>
  typeof value === 'string' && ISO_DATE.test(value) && !Number.isNaN(Date.parse(value));

const everyValue = (rows: unknown[][], column: number, test: (value: unknown) => boolean) => {
  let seen = false;
  for (const row of rows) {
    const value = row[column];
    if (value === null || value === undefined) continue;
    if (!test(value)) return false;
    seen = true;
  }
  return seen;
};

/**
 * The chart kinds a SQL result's shape fits: a time column and numbers draw a
 * time series; a label and a number draw top values (and a donut when no
 * number is negative).
 */
export const sqlChartShape = (columns: string[], rows: unknown[][]): SqlChartShape => {
  let timeColumn: number | null = null;
  let labelColumn: number | null = null;
  const valueColumns: number[] = [];
  columns.forEach((_name, i) => {
    if (timeColumn === null && everyValue(rows, i, isTime)) timeColumn = i;
    else if (everyValue(rows, i, isNumeric)) valueColumns.push(i);
    else if (labelColumn === null) labelColumn = i;
  });
  const kinds: SqlChartKind[] = [];
  if (valueColumns.length > 0) {
    if (timeColumn !== null) kinds.push('series');
    if (labelColumn !== null) {
      kinds.push('top');
      if (rows.every((row) => Number(row[valueColumns[0]] ?? 0) >= 0)) kinds.push('donut');
    }
  }
  return { kinds, timeColumn, labelColumn, valueColumns };
};

/** The kind on screen: the one picked when the shape still fits it, else the first that fits. */
export const resolveSqlChartKind = (
  shape: SqlChartShape,
  picked: SqlChartKind | null,
): SqlChartKind | undefined => (picked && shape.kinds.includes(picked) ? picked : shape.kinds[0]);

/** Series of [time, value] points: one per label (first number column) or one per number column. */
export const sqlSeries = (
  shape: SqlChartShape,
  columns: string[],
  rows: unknown[][],
): { key: string; points: [number, number | null][] }[] => {
  const time = shape.timeColumn;
  if (time === null || shape.valueColumns.length === 0) return [];
  const toPoint = (row: unknown[], column: number): [number, number | null] => [
    Date.parse(String(row[time])),
    row[column] === null || row[column] === undefined ? null : Number(row[column]),
  ];
  const sorted = [...rows].sort(
    (a, b) => Date.parse(String(a[time])) - Date.parse(String(b[time])),
  );
  if (shape.labelColumn !== null) {
    const label = shape.labelColumn;
    const byLabel = new Map<string, [number, number | null][]>();
    for (const row of sorted) {
      const key = String(row[label] ?? '');
      byLabel.set(key, [...(byLabel.get(key) ?? []), toPoint(row, shape.valueColumns[0])]);
    }
    return [...byLabel.entries()].map(([key, points]) => ({ key, points }));
  }
  return shape.valueColumns.map((column) => ({
    key: columns[column],
    points: sorted.map((row) => toPoint(row, column)),
  }));
};

/** Label and first number per row, largest first. */
export const sqlTopRows = (
  shape: SqlChartShape,
  rows: unknown[][],
): { key: string; value: number }[] => {
  if (shape.labelColumn === null || shape.valueColumns.length === 0) return [];
  const label = shape.labelColumn;
  const value = shape.valueColumns[0];
  return rows
    .map((row) => ({ key: String(row[label] ?? ''), value: Number(row[value] ?? 0) }))
    .sort((a, b) => b.value - a.value);
};

/** A single number for a stat: the first numeric value of the first row, or null. */
export const sqlFirstNumber = (rows: unknown[][]): number | null => {
  const value = rows[0]?.find(isNumeric);
  return value === undefined ? null : Number(value);
};
