import { describe, expect, it } from 'vitest';

import type { PromQLSeries } from '../../../entity/synthetics';
import {
  groupBy,
  idsMatcher,
  indexBy,
  maxBy,
  mean,
  mergedQuantile,
  passRatio,
  quantileResolution,
  ratioBy,
  sameSeries,
  selector,
  statusWindow,
} from './queries';

const series = (labels: Record<string, string>, values: [string, number][]): PromQLSeries => ({
  labels,
  points: values.map(([timestamp, value]) => ({ timestamp, value })),
});

describe('selector and idsMatcher', () => {
  it('drops the ID regex when the chunk is the whole destination', () => {
    expect(idsMatcher(['a', 'b'], 2)).toBeUndefined();
    expect(idsMatcher(['a'], 2)).toBe('check_id=~"a"');
    expect(selector('synthetics_up', idsMatcher(['a', 'b'], 2))).toBe('synthetics_up');
    expect(selector('synthetics_up', 'phase="total"', idsMatcher(['a'], 3))).toBe(
      'synthetics_up{phase="total",check_id=~"a"}',
    );
  });
});

describe('aggregation across split series', () => {
  it('counts uptime as passed runs over all runs, not an average of per-series averages', () => {
    expect(passRatio('check_id', undefined, 86400)).toBe(
      'sum by (check_id) (sum_over_time(synthetics_up[86400s])) / sum by (check_id) (count_over_time(synthetics_up[86400s]))',
    );
    expect(mean('', 'synthetics_response_bytes{check_id="c1"}', 60)).toBe(
      'sum (sum_over_time(synthetics_response_bytes{check_id="c1"}[60s])) / sum (count_over_time(synthetics_response_bytes{check_id="c1"}[60s]))',
    );
  });

  it('merges series into one sample per window before taking a quantile', () => {
    expect(mergedQuantile(0.95, 'location', 'd', 3600, 30)).toBe(
      'quantile_over_time(0.95, (max by (location) (max_over_time(d[30s])))[3600s:30s])',
    );
  });

  it('keeps windows under the interval and status windows at two runs and a minute', () => {
    expect(quantileResolution(60)).toBe(30);
    expect(quantileResolution(30)).toBe(30);
    expect(quantileResolution(300)).toBe(150);
    expect(statusWindow(60)).toBe(180);
    expect(statusWindow(3600)).toBe(7260);
  });
});

describe('series helpers', () => {
  it('indexes and groups once instead of searching per monitor', () => {
    const index = indexBy(
      [series({ check_id: 'a' }, []), series({ check_id: 'b' }, []), series({ check_id: 'a' }, [])],
      'check_id',
    );
    expect(index.get('a')).toHaveLength(2);
    expect([...groupBy([1, 2, 3, 4], (n) => n % 2)]).toEqual([
      [1, [1, 3]],
      [0, [2, 4]],
    ]);
  });

  it('combines per-location step success as passed over runs', () => {
    const passed = [
      series({ step_index: '1', step: 'Sign in', location: 'a' }, [['', 9]]),
      series({ step_index: '1', step: 'Sign in', location: 'b' }, [['', 1]]),
    ];
    const runs = [
      series({ step_index: '1', step: 'Sign in', location: 'a' }, [['', 10]]),
      series({ step_index: '1', step: 'Sign in', location: 'b' }, [['', 10]]),
    ];
    const [combined] = ratioBy(passed, runs, ['step_index', 'step']);
    expect(combined.labels).toEqual({ step_index: '1', step: 'Sign in' });
    expect(combined.points[0].value).toBe(0.5);
  });

  it('merges per-location series by the highest value per timestamp', () => {
    const [merged] = maxBy(
      [
        series({ step: 's', location: 'a' }, [
          ['2026-10-25T08:00:00Z', 1],
          ['2026-10-25T08:01:00Z', 5],
        ]),
        series({ step: 's', location: 'b' }, [['2026-10-25T08:00:00Z', 3]]),
      ],
      ['step'],
    );
    expect(merged.points.map((point) => point.value)).toEqual([3, 5]);
  });

  it('tells equal answers apart from changed ones', () => {
    const a = [series({ location: 'a' }, [['t', 1]])];
    expect(sameSeries(a, [series({ location: 'a' }, [['t', 1]])])).toBe(true);
    expect(sameSeries(a, [series({ location: 'a' }, [['t', 0]])])).toBe(false);
    expect(sameSeries(a, [series({ location: 'b' }, [['t', 1]])])).toBe(false);
  });
});
