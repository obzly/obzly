import { describe, expect, it } from 'vitest';

import { sqlPanelData } from './sqlPanel';

const byService = {
  columns: ['service', 'errors'],
  rows: [
    ['search', 2],
    ['checkout', 9],
  ],
};

describe('sqlPanelData', () => {
  it('reads a time column and labels as one series per label', () => {
    const data = sqlPanelData('line', {
      columns: ['minute', 'service', 'errors'],
      rows: [
        ['2026-11-18T10:01:00Z', 'checkout', 3],
        ['2026-11-18T10:00:00Z', 'checkout', 2],
        ['2026-11-18T10:00:00Z', 'search', null],
      ],
    });
    expect(data).toEqual({
      buckets: [
        { timestamp: '2026-11-18T10:00:00.000Z', groupKey: 'checkout', value: 2 },
        { timestamp: '2026-11-18T10:01:00.000Z', groupKey: 'checkout', value: 3 },
      ],
      problem: null,
    });
  });

  it('draws a single unlabelled number column as one legend-less line', () => {
    const data = sqlPanelData('line', {
      columns: ['minute', 'lines'],
      rows: [['2026-11-18T10:00:00Z', 4]],
    });
    expect(data.buckets).toEqual([{ timestamp: '2026-11-18T10:00:00.000Z', value: 4 }]);
  });

  it('ranks label rows for a ranked list or donut', () => {
    const expected = [
      { groupKey: 'checkout', value: 9 },
      { groupKey: 'search', value: 2 },
    ];
    expect(sqlPanelData('rankedList', byService).buckets).toEqual(expected);
    expect(sqlPanelData('donut', byService).buckets).toEqual(expected);
  });

  it('shows the first number of the first row as a stat', () => {
    expect(sqlPanelData('stat', byService)).toEqual({ buckets: [{ value: 2 }], problem: null });
  });

  it('says why a result does not fit the kind', () => {
    expect(sqlPanelData('line', byService).problem).toMatch(/time column/);
    expect(
      sqlPanelData('donut', { columns: ['service', 'delta'], rows: [['checkout', -1]] }).problem,
    ).toMatch(/no negative/);
    expect(sqlPanelData('stat', { columns: ['service'], rows: [['checkout']] }).problem).toMatch(
      /no number/,
    );
    expect(sqlPanelData('table', { columns: ['service'], rows: [] }).problem).toBe(
      'No rows in this range',
    );
  });

  it('leaves a table to show its rows as they are', () => {
    expect(sqlPanelData('table', byService)).toEqual({ buckets: [], problem: null });
  });
});
