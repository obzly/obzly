import { describe, expect, it } from 'vitest';

import type { AggregationBucket } from '../../../entity/dashboards';
import type { QueryNode } from '../../../entity/query';
import {
  andQuery,
  bandClauses,
  bandFindings,
  bandLabel,
  bandMatrix,
  chartSql,
  comparisonWindows,
  defaultNumericField,
  deltaRows,
  firstSeen,
  foldWeekdayHour,
  formatMeasureValue,
  inCondition,
  latestSlot,
  measureLabel,
  nextBand,
  queryToSql,
  rankNumericFields,
  ratioText,
  seriesOnGrid,
  timeGrid,
  topRows,
  usesRollups,
  weekdayHourPeak,
} from './logsChart';

const at = (iso: string) => new Date(iso).toISOString();
const errors: QueryNode = {
  type: 'condition',
  condition: { field: 'level', operator: 'equals', value: 'ERROR' },
};

describe('measures', () => {
  it('labels and formats each measure, n/a when unknown', () => {
    expect(measureLabel('count')).toBe('lines');
    expect(measureLabel('p95', 'duration_ms')).toBe('p95 duration_ms');
    expect(formatMeasureValue(8120, 'count')).toBe('8,120');
    expect(formatMeasureValue(1940, 'avg', 'duration_ms')).toBe('1.94 s');
    expect(formatMeasureValue(412, 'p95', 'duration_ms')).toBe('412 ms');
    expect(formatMeasureValue(2_516_582, 'avg', 'response_bytes')).toBe('2.4 MB');
    expect(formatMeasureValue(null, 'avg', 'duration_ms')).toBe('n/a');
  });

  it('knows when the backend reads per-minute counts', () => {
    expect(usesRollups(errors, 'service_name', 'count')).toBe(true);
    expect(usesRollups(errors, 'http_status_code', 'count')).toBe(false);
    expect(usesRollups(errors, 'level', 'avg')).toBe(false);
    const messageSearch: QueryNode = {
      type: 'condition',
      condition: { field: 'message', operator: 'contains', value: 'timeout' },
    };
    expect(usesRollups(messageSearch, 'level', 'count')).toBe(false);
  });
});

describe('top values and series', () => {
  const buckets: AggregationBucket[] = [
    { groupKey: 'checkout', value: 30 },
    { groupKey: 'search', value: 10 },
    { groupKey: 'checkout', value: 10 },
  ];

  it('sums per group, largest first, with shares only for counts', () => {
    expect(topRows(buckets, true)).toEqual([
      { key: 'checkout', value: 40, share: 0.8 },
      { key: 'search', value: 10, share: 0.2 },
    ]);
    expect(topRows(buckets, false)[0].share).toBeNull();
  });

  it('places buckets on the grid, filling gaps with 0 for counts and null otherwise', () => {
    const grid = timeGrid(
      Date.parse('2026-11-18T10:00:30Z'),
      Date.parse('2026-11-18T10:02:00Z'),
      60_000,
    );
    expect(grid.map((t) => new Date(t).toISOString())).toEqual([
      '2026-11-18T10:00:00.000Z',
      '2026-11-18T10:01:00.000Z',
      '2026-11-18T10:02:00.000Z',
    ]);
    const timed: AggregationBucket[] = [
      { timestamp: at('2026-11-18T10:00:00Z'), groupKey: 'a', value: 1 },
      { timestamp: at('2026-11-18T10:02:00Z'), groupKey: 'a', value: 2 },
      { timestamp: at('2026-11-18T10:01:00Z'), groupKey: 'b', value: 9 },
    ];
    expect(seriesOnGrid(timed, grid, 0)).toEqual([
      { key: 'b', values: [0, 9, 0] },
      { key: 'a', values: [1, 0, 2] },
    ]);
    expect(seriesOnGrid(timed, grid, null)[1].values).toEqual([1, null, 2]);
    expect(firstSeen(timed).get('a')).toBe(Date.parse('2026-11-18T10:00:00Z'));
  });
});

describe('weekday × hour', () => {
  it('folds buckets into Monday-first weekdays and hours in the timezone', () => {
    // 2026-11-19 is a Thursday.
    const grid = foldWeekdayHour(
      [
        { timestamp: at('2026-11-19T02:00:00Z'), value: 5 },
        { timestamp: at('2026-11-19T02:15:00Z'), value: 1 },
        { timestamp: at('2026-11-16T23:00:00Z'), value: 2 },
      ],
      'UTC',
    );
    expect(grid[3][2]).toBe(6);
    expect(grid[0][23]).toBe(2);
    const peak = weekdayHourPeak(grid);
    expect(peak).toMatchObject({ weekday: 3, hour: 2, value: 6 });
    expect(peak?.timesAverage).toBeCloseTo(6 / (8 / 168));
  });

  it('finds the newest occurrence of a weekday hour inside the range', () => {
    const from = Date.parse('2026-11-12T00:00:00Z');
    const to = Date.parse('2026-11-19T12:00:00Z');
    expect(latestSlot(3, 2, from, to, 'UTC')).toEqual({
      from: Date.parse('2026-11-19T02:00:00Z'),
      to: Date.parse('2026-11-19T03:00:00Z'),
    });
    // A Sunday hour before the range starts isn't in it.
    expect(latestSlot(6, 5, Date.parse('2026-11-17T00:00:00Z'), to, 'UTC')).toBeNull();
  });
});

describe('value bands', () => {
  it('steps 1-2-5 bands and labels them in the field unit', () => {
    expect(nextBand(1)).toBe(2);
    expect(nextBand(2)).toBe(5);
    expect(nextBand(5)).toBe(10);
    expect(nextBand(500)).toBe(1000);
    expect(bandLabel(500, 'duration_ms')).toBe('500 ms–1 s');
    expect(bandLabel(1024, 'bytes')).toBe('1 KB–2 KB');
    expect(bandLabel(0, 'duration_ms')).toBe('≤ 0');
    expect(bandClauses('duration_ms', 50)).toEqual(['duration_ms>=50', 'duration_ms<100']);
  });

  it('fills every band between the extremes, largest first, and spots a new band', () => {
    const columns = [0, 1, 2, 3].map((i) => Date.parse('2026-11-18T10:00:00Z') + i * 300_000);
    const buckets: AggregationBucket[] = [
      ...columns.map((t) => ({
        timestamp: new Date(t).toISOString(),
        groupKey: '50.0',
        value: 100,
      })),
      { timestamp: new Date(columns[3]).toISOString(), groupKey: '1000.0', value: 40 },
    ];
    const matrix = bandMatrix(buckets, columns);
    expect(matrix.bands).toEqual([1000, 500, 200, 100, 50]);
    expect(matrix.cells[4]).toEqual([100, 100, 100, 100]);
    expect(matrix.cells[0]).toEqual([0, 0, 0, 40]);
    expect(matrix.max).toBe(100);
    expect(bandFindings(matrix)).toEqual({ dominant: 50, newBand: 1000, newSince: columns[3] });
  });
});

describe('compare', () => {
  const from = Date.parse('2026-11-18T14:00:00Z');
  const to = Date.parse('2026-11-18T15:00:00Z');

  it('shifts the range back for each kind', () => {
    const nowMs = to;
    expect(comparisonWindows('prev', from, to, { nowMs })?.baseline).toEqual({
      from: from - 3_600_000,
      to: from,
    });
    expect(comparisonWindows('week', from, to, { nowMs })?.baseline.from).toBe(
      from - 7 * 86_400_000,
    );
    expect(comparisonWindows('custom', from, to, { nowMs })).toBeNull();
    const rollout = Date.parse('2026-11-18T14:50:00Z');
    expect(comparisonWindows('deploy', from, to, { nowMs, rolloutMs: rollout })).toEqual({
      current: { from: rollout, to },
      baseline: { from: rollout - 600_000, to: rollout },
    });
  });

  it('ranks changes by size, rises bad, drops good, small ones neutral', () => {
    const rows = deltaRows(
      [
        { key: 'payments-api', value: 1184 },
        { key: 'search', value: 41 },
        { key: 'new-svc', value: 30 },
      ],
      [
        { key: 'payments-api', value: 61 },
        { key: 'search', value: 52 },
        { key: 'catalog', value: 300 },
      ],
    );
    expect(rows.map((r) => r.key)).toEqual(['payments-api', 'catalog', 'new-svc', 'search']);
    expect(rows[0]).toMatchObject({ change: '+1841%', tone: 'bad', small: false });
    expect(rows[1]).toMatchObject({ change: '-100%', tone: 'good' });
    expect(rows[2]).toMatchObject({ change: 'new', tone: 'bad' });
    expect(rows[3]).toMatchObject({ tone: 'neutral', small: true });
    expect(ratioText(570, 100)).toBe('5.7× higher');
    expect(ratioText(50, 100)).toBe('2× lower');
    expect(ratioText(100, 100)).toBe('about the same');
    expect(ratioText(null, 100)).toBeNull();
  });
});

describe('the search as SQL', () => {
  it('writes conditions against SQL mode columns and the fields JSON', () => {
    const query: QueryNode = {
      type: 'logical',
      logic: {
        operator: 'and',
        children: [
          errors,
          {
            type: 'condition',
            condition: { field: 'k8s.pod.name', operator: 'contains', value: "o'k" },
          },
          {
            type: 'condition',
            condition: { field: 'duration_ms', operator: 'greater_than', value: 500 },
          },
        ],
      },
    };
    expect(queryToSql(query)).toBe(
      `(lower(level) = 'error' AND contains(json_extract_string(fields, '$."k8s_pod_name"'), 'o''k') AND TRY_CAST(json_extract_string(fields, '$."duration_ms"') AS DOUBLE) > 500)`,
    );
    expect(
      queryToSql({
        type: 'condition',
        condition: { field: 'pattern.hash', operator: 'equals', value: 'x' },
      }),
    ).toBeNull();
  });

  it('builds the statement behind top values and a time series', () => {
    expect(
      chartSql({
        query: errors,
        kind: 'top',
        measure: 'count',
        split: 'service_name',
        top: 5,
        bucketMs: 60_000,
      }),
    ).toBe(
      `SELECT json_extract_string(fields, '$."service_name"') AS service_name, count(*) AS lines FROM logs WHERE lower(level) = 'error' GROUP BY 1 ORDER BY 2 DESC LIMIT 5`,
    );
    expect(
      chartSql({
        query: null,
        kind: 'series',
        measure: 'p95',
        measureField: 'duration_ms',
        split: '',
        top: 5,
        bucketMs: 60_000,
      }),
    ).toBe(
      `SELECT time_bucket(INTERVAL '60 seconds', timestamp) AS bucket, quantile_cont(TRY_CAST(json_extract_string(fields, '$."duration_ms"') AS DOUBLE), 0.95) AS p95_value FROM logs WHERE json_extract_string(fields, '$."duration_ms"') IS NOT NULL GROUP BY 1 ORDER BY 1`,
    );
  });
});

describe('query helpers', () => {
  it('ANDs nodes, skipping empty ones', () => {
    expect(andQuery(null, undefined)).toBeNull();
    expect(andQuery(errors, null)).toBe(errors);
    const both = andQuery(errors, inCondition('service_name', ['a']));
    expect(both?.logic?.children).toHaveLength(2);
  });

  it('ranks promoted number fields, then numeric-looking names', () => {
    const ranked = rankNumericFields(
      ['user', 'bytes', 'elapsed_ms', 'amount'],
      new Set(['amount']),
    );
    expect(ranked).toEqual(['amount', 'bytes', 'elapsed_ms', 'user']);
    expect(defaultNumericField(['user', 'region'], new Set())).toBeUndefined();
    expect(defaultNumericField(['user', 'duration_ms'], new Set())).toBe('duration_ms');
  });
});
