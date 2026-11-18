import type { AggregationBucket, PanelTable, PanelType } from '../../../entity/dashboards';
import {
  sqlChartShape,
  sqlFirstNumber,
  sqlSeries,
  sqlTopRows,
} from '../../../shared/lib/sqlResultShape';

// A logs_sql panel's columns and rows read as the buckets the other panel
// kinds draw, using the same shape inference as Logs › SQL results.

/** The kinds a SQL result can fill (backend dashboards_services.sqlPanelTypes). */
export const SQL_PANEL_TYPES: readonly PanelType[] = [
  'line',
  'rankedList',
  'donut',
  'table',
  'stat',
];

export interface SqlPanelData {
  buckets: AggregationBucket[];
  /** Why the result can't be drawn as this kind, shown in place of the chart. */
  problem: string | null;
}

export const sqlPanelData = (type: PanelType, table: PanelTable): SqlPanelData => {
  if (table.rows.length === 0) return { buckets: [], problem: 'No rows in this range' };
  const shape = sqlChartShape(table.columns, table.rows);
  switch (type) {
    case 'line': {
      if (!shape.kinds.includes('series')) {
        return { buckets: [], problem: 'A line chart needs a time column and a number column' };
      }
      const series = sqlSeries(shape, table.columns, table.rows);
      // One unlabelled number column draws a single, legend-less line.
      const grouped = series.length > 1 || shape.labelColumn !== null;
      return {
        buckets: series.flatMap((s) =>
          s.points
            .filter((point): point is [number, number] => point[1] !== null)
            .map(([time, value]) => ({
              timestamp: new Date(time).toISOString(),
              groupKey: grouped ? s.key : undefined,
              value,
            })),
        ),
        problem: null,
      };
    }
    case 'rankedList':
    case 'donut': {
      if (!shape.kinds.includes(type === 'donut' ? 'donut' : 'top')) {
        return {
          buckets: [],
          problem:
            type === 'donut'
              ? 'A donut needs a label column and a number column with no negative numbers'
              : 'A ranked list needs a label column and a number column',
        };
      }
      return {
        buckets: sqlTopRows(shape, table.rows).map((row) => ({
          groupKey: row.key,
          value: row.value,
        })),
        problem: null,
      };
    }
    case 'stat': {
      const value = sqlFirstNumber(table.rows);
      return value === null
        ? { buckets: [], problem: 'The first row has no number to show' }
        : { buckets: [{ value }], problem: null };
    }
    default:
      // A table shows the rows as they are.
      return { buckets: [], problem: null };
  }
};
