import { describe, expect, it } from 'vitest';

import {
  resolveSqlChartKind,
  sqlChartShape,
  sqlFirstNumber,
  sqlSeries,
  sqlTopRows,
} from './sqlResultShape';

describe('SQL results', () => {
  it('offers a time series for a time column and top values for a label', () => {
    const columns = ['bucket', 'service_name', 'errors'];
    const rows = [
      ['2026-11-18 10:01:00', 'checkout', 3],
      ['2026-11-18 10:00:00', 'checkout', 2],
      ['2026-11-18 10:00:00', 'search', '1'],
    ];
    const shape = sqlChartShape(columns, rows);
    expect(shape).toEqual({
      kinds: ['series', 'top', 'donut'],
      timeColumn: 0,
      labelColumn: 1,
      valueColumns: [2],
    });
    expect(sqlSeries(shape, columns, rows)[0]).toEqual({
      key: 'checkout',
      points: [
        [Date.parse('2026-11-18 10:00:00'), 2],
        [Date.parse('2026-11-18 10:01:00'), 3],
      ],
    });
    const top = sqlChartShape(
      ['service_name', 'errors'],
      [
        ['a', 5],
        ['b', 9],
      ],
    );
    expect(top.kinds).toEqual(['top', 'donut']);
    expect(
      sqlTopRows(top, [
        ['a', 5],
        ['b', 9],
      ]),
    ).toEqual([
      { key: 'b', value: 9 },
      { key: 'a', value: 5 },
    ]);
    expect(sqlChartShape(['message'], [['x']]).kinds).toEqual([]);
  });
});

describe('resolveSqlChartKind', () => {
  const shape = sqlChartShape(['service', 'errors'], [['checkout', 3]]);

  it('keeps the picked kind while the result still fits it', () => {
    expect(resolveSqlChartKind(shape, 'donut')).toBe('donut');
  });

  it('falls back to the first kind that fits', () => {
    expect(resolveSqlChartKind(shape, 'series')).toBe('top');
    expect(resolveSqlChartKind(shape, null)).toBe('top');
    expect(resolveSqlChartKind(sqlChartShape(['message'], [['x']]), null)).toBeUndefined();
  });
});

describe('sqlFirstNumber', () => {
  it('reads the first numeric value of the first row', () => {
    expect(sqlFirstNumber([['checkout', '12.5', 3]])).toBe(12.5);
    expect(sqlFirstNumber([[null, 7]])).toBe(7);
  });

  it('is null without rows or numbers', () => {
    expect(sqlFirstNumber([])).toBeNull();
    expect(sqlFirstNumber([['checkout']])).toBeNull();
  });
});
