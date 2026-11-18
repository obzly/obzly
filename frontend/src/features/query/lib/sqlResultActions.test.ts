import { describe, expect, it } from 'vitest';

import {
  sqlAlertDraft,
  sqlAlertUnavailableReason,
  sqlPanel,
  sqlPanelType,
  sqlResultName,
} from './sqlResultActions';

const SQL = 'SELECT service, count(*) AS errors FROM logs GROUP BY 1';

describe('sqlPanelType', () => {
  it('saves the chart on screen as its panel kind', () => {
    expect(sqlPanelType('chart', 'series')).toBe('line');
    expect(sqlPanelType('chart', 'top')).toBe('rankedList');
    expect(sqlPanelType('chart', 'donut')).toBe('donut');
  });

  it('saves the Lines view, or a result with no chart, as a table', () => {
    expect(sqlPanelType('lines', 'series')).toBe('table');
    expect(sqlPanelType('chart', undefined)).toBe('table');
  });
});

describe('sqlResultName', () => {
  it('names the compared number by the first label', () => {
    expect(sqlResultName(SQL, ['service', 'errors'], [['checkout', 3]])).toBe('errors by service');
    expect(
      sqlResultName(SQL, ['service', 'value', 'p95'], [['checkout', 3, 120]]),
      'the value column is what a rule compares',
    ).toBe('value by service');
  });

  it('falls back to the SQL on one line, shortened', () => {
    const long = `SELECT count(*)\n  FROM logs WHERE message LIKE '%${'x'.repeat(100)}%'`;
    const name = sqlResultName(long, ['count_star()'], [[3]]);
    expect(name.startsWith('SQL: SELECT count(*) FROM logs')).toBe(true);
    expect(name.length).toBeLessThanOrEqual(85);
    expect(name.endsWith('…')).toBe(true);
  });
});

describe('sqlPanel and sqlAlertDraft', () => {
  it('carry the executed SQL as a logs_sql source', () => {
    expect(sqlPanel(` ${SQL} `, 'rankedList', 'Errors')).toEqual({
      title: 'Errors',
      type: 'rankedList',
      source: 'logs_sql',
      sql: SQL,
    });
    expect(sqlAlertDraft(SQL, ['service', 'errors'], [['checkout', 3]])).toEqual({
      name: 'errors by service',
      source: 'logs_sql',
      query: { sql: SQL },
      comparison: '>',
      threshold: 0,
      windowSeconds: 300,
    });
  });

  it('refuses an alert on a result without numbers', () => {
    expect(sqlAlertUnavailableReason(['message'], [['boom']])).toMatch(/no number column/);
    expect(sqlAlertUnavailableReason(['service', 'n'], [['checkout', 1]])).toBeNull();
    expect(sqlAlertUnavailableReason(['service'], [])).toBeNull();
  });
});
