import type { RuleRequest } from '../../../entity/alerting';
import type { Panel, PanelType } from '../../../entity/dashboards';
import { type SqlChartKind, sqlChartShape } from '../../../shared/lib/sqlResultShape';

// Logs › SQL results › Save to dashboard and Create alert: what the result on
// screen becomes as a logs_sql panel or rule.

const SQL_PANEL_TYPE_FOR_KIND: Record<SqlChartKind, PanelType> = {
  series: 'line',
  top: 'rankedList',
  donut: 'donut',
};

const SQL_KIND_NAMES: Record<SqlChartKind, string> = {
  series: 'time series',
  top: 'ranked list',
  donut: 'donut',
};

const MAX_NAME_LENGTH = 80;

/** The panel kind: the chart on screen, or a table from the Lines view or a shapeless result. */
export const sqlPanelType = (view: 'lines' | 'chart', kind: SqlChartKind | undefined): PanelType =>
  view === 'chart' && kind ? SQL_PANEL_TYPE_FOR_KIND[kind] : 'table';

export const sqlPanelTypeName = (view: 'lines' | 'chart', kind: SqlChartKind | undefined) =>
  view === 'chart' && kind ? SQL_KIND_NAMES[kind] : 'table';

/** "SQL: SELECT level, count(*) …", one line, for a name when the columns don't give one. */
const sqlSnippet = (sql: string): string => {
  const line = sql.replace(/\s+/g, ' ').trim();
  return `SQL: ${line.length > MAX_NAME_LENGTH ? `${line.slice(0, MAX_NAME_LENGTH - 1)}…` : line}`;
};

/**
 * "errors by service" from the result's first label and the number a rule
 * compares (the "value" column, else the last numeric one); else the SQL.
 */
export const sqlResultName = (sql: string, columns: string[], rows: unknown[][]): string => {
  const shape = sqlChartShape(columns, rows);
  const named = columns.findIndex((c) => c.toLowerCase() === 'value');
  const value = named >= 0 ? named : shape.valueColumns[shape.valueColumns.length - 1];
  if (value === undefined || shape.labelColumn === null) return sqlSnippet(sql);
  return `${columns[value]} by ${columns[shape.labelColumn]}`;
};

export const sqlPanel = (
  sql: string,
  type: PanelType,
  title: string,
): Omit<Panel, 'id' | 'gridPos'> => ({
  title,
  type,
  source: 'logs_sql',
  sql: sql.trim(),
});

/** A rule over the same SQL: fires when any row's number goes above 0, checked over 5 minutes. */
export const sqlAlertDraft = (
  sql: string,
  columns: string[],
  rows: unknown[][],
): Partial<RuleRequest> => ({
  name: sqlResultName(sql, columns, rows),
  source: 'logs_sql',
  query: { sql: sql.trim() },
  comparison: '>',
  threshold: 0,
  windowSeconds: 300,
});

/** Why a rule can't watch this result, or null: a rule compares a number per row. */
export const sqlAlertUnavailableReason = (columns: string[], rows: unknown[][]): string | null =>
  rows.length > 0 && sqlChartShape(columns, rows).valueColumns.length === 0
    ? 'An alert compares a number per row; this result has no number column'
    : null;
