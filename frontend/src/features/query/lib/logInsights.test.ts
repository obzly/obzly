import { describe, expect, it } from 'vitest';

import type { PatternTrend, PatternTrendsResponse } from '../../../entity/query';
import {
  currentSampleRate,
  explainHeadline,
  explainLift,
  formatBytes,
  formatCount,
  healthColor,
  healthMetric,
  healthTileFilter,
  matchesPatternFilter,
  mixHex,
  samplingPreview,
  samplingSuggestion,
  sortTrends,
  stackPatterns,
  trendChangeBadge,
  withSampleRule,
} from './logInsights';

const trend = (overrides: Partial<PatternTrend>): PatternTrend => ({
  hash: 'h',
  pattern: 'p',
  sample: 's',
  level: 'info',
  services: [],
  count: 10,
  series: [5, 5],
  yesterday: 10,
  previousWeek: 70,
  change: '',
  ...overrides,
});

const status = { ok: '#00ff00', warn: '#ffff00', bad: '#ff0000', noData: '#cccccc' };

describe('logInsights', () => {
  it('formats counts and bytes, unknown as n/a', () => {
    expect(formatCount(812)).toBe('812');
    expect(formatCount(18240)).toBe('18 k');
    expect(formatCount(1.9e6)).toBe('1.9 M');
    expect(formatBytes(1.4 * 1024 ** 3)).toBe('1.4 GB');
    expect(formatBytes(600 * 1024 ** 2)).toBe('600 MB');
    expect(formatBytes(null)).toBe('n/a');
  });

  it('labels pattern changes', () => {
    expect(trendChangeBadge(trend({ change: 'new', yesterday: 0 }))).toEqual({
      text: 'new',
      tone: 'bad',
    });
    expect(
      trendChangeBadge(trend({ change: 'spike', count: 488, yesterday: 41, level: 'warn' })),
    ).toEqual({
      text: '×11.9',
      tone: 'warn',
    });
    expect(trendChangeBadge(trend({ change: 'drop', count: 1104, yesterday: 1920 })).text).toBe(
      '−43%',
    );
    expect(trendChangeBadge(trend({ count: 6410, yesterday: 5920 })).text).toBe('steady');
    expect(trendChangeBadge(trend({ count: 13, yesterday: 10 })).text).toBe('+30%');
    expect(trendChangeBadge(trend({ count: 3, yesterday: 0 })).text).toBe('no baseline');
  });

  it('filters and sorts patterns: changed first, then by lines', () => {
    const list = [
      trend({ hash: 'a', count: 100 }),
      trend({ hash: 'b', change: 'drop', count: 1 }),
      trend({ hash: 'c', change: 'new', count: 5 }),
      trend({ hash: 'd', change: 'spike', count: 50 }),
    ];
    expect(sortTrends(list).map((p) => p.hash)).toEqual(['c', 'd', 'b', 'a']);
    expect(list.filter((p) => matchesPatternFilter(p, 'spiking')).map((p) => p.hash)).toEqual([
      'd',
    ]);
    expect(list.filter((p) => matchesPatternFilter(p, 'all'))).toHaveLength(4);
  });

  it('stacks the biggest patterns and folds the rest into Other', () => {
    const response = {
      other: [1, 2],
      patterns: [
        trend({ hash: 'a', count: 10, series: [4, 6] }),
        trend({ hash: 'b', count: 3, series: [1, 2] }),
        trend({ hash: 'c', count: 0, series: [0, 0] }),
      ],
    } as PatternTrendsResponse;
    const stack = stackPatterns(response, 1);
    expect(stack.map((s) => s.hash)).toEqual(['a', null]);
    expect(stack[1].values).toEqual([2, 4]);
  });

  it('explains lift and builds a headline from real fields', () => {
    const field = {
      field: 'service_name',
      value: 'payments-api',
      current: 71,
      baseline: 0,
      currentShare: 0.71,
      baselineShare: 0,
    };
    expect(explainLift(field)).toBe('new');
    expect(explainLift({ ...field, baseline: 12, baselineShare: 0.12 })).toBe('×5.9');
    expect(explainHeadline([], [])).toBeNull();
    const summary = explainHeadline(
      [field],
      [
        {
          hash: 'x',
          pattern: '',
          sample: '',
          level: 'error',
          current: 5,
          baseline: 0,
          change: 'new',
          ratio: 1e9,
        },
      ],
    );
    expect(summary?.headline).toBe('Most of the extra lines carry service_name = payments-api.');
    expect(summary?.detail).toContain('1 new message pattern appear');
  });

  it('colours health tiles by error share and change, unknown as no data', () => {
    const tile = { name: 'w', total: 100, errors: 12, warnings: 8, previous: 0 };
    expect(healthMetric(tile, 'err')).toBe(12);
    expect(healthMetric(tile, 'warn')).toBe(20);
    expect(healthMetric(tile, 'chg')).toBeNull();
    expect(healthColor(null, 'chg', status)).toBe(status.noData);
    expect(healthColor(0, 'err', status)).toBe('#00ff00');
    expect(healthColor(4, 'err', status)).toBe('#ffff00');
    expect(healthColor(25, 'err', status)).toBe('#ff0000');
    expect(mixHex('#000000', '#ffffff', 0.5)).toBe('#808080');
  });

  it('suggests sampling by level and previews savings', () => {
    expect(samplingSuggestion({ level: 'error' }).keep).toBeNull();
    expect(samplingSuggestion({ level: 'debug' }).keep).toBe(0);
    expect(samplingSuggestion({ level: 'info' }).keep).toBe(10);
    expect(samplingPreview(1000, 10, 100)).toEqual({
      keptPerDay: 100,
      savedBytesPerMonth: 2_700_000,
    });
    expect(samplingPreview(1000, 10, 0).savedBytesPerMonth).toBeNull();
  });

  it('puts a pattern rule first, replacing the same one, and finds the rate that applies', () => {
    const rules = [
      { service: '', level: 'info', rate: 0.5 },
      { service: 'web', level: 'info', pattern: 'abc', rate: 0.25 },
    ];
    const next = withSampleRule(rules, {
      service: 'web',
      level: 'info',
      pattern: 'abc',
      rate: 0.1,
    });
    expect(next).toEqual([
      { service: 'web', level: 'info', pattern: 'abc', rate: 0.1 },
      { service: '', level: 'info', rate: 0.5 },
    ]);
    expect(currentSampleRate(rules, 'web', 'INFO', 'zzz')).toBe(0.5);
    expect(currentSampleRate(next, 'web', 'info', 'abc')).toBe(0.1);
    expect(currentSampleRate(next, 'web', 'warn', 'abc')).toBe(1);
  });

  it('builds a health tile filter, skipping placeholder names', () => {
    expect(healthTileFilter('k8s', 'shop', 'checkout').clauses).toEqual([
      'k8s_namespace_name=shop',
      'k8s_pod_name:checkout',
    ]);
    expect(healthTileFilter('k8s', '(outside Kubernetes)', 'cron').clauses).toEqual([
      'service_name=cron',
    ]);
    expect(healthTileFilter('host', 'node a', '(unknown)').clauses).toEqual(['host_name="node a"']);
    expect(healthTileFilter('service', 'all services', 'web').node).toEqual({
      type: 'condition',
      condition: { field: 'service_name', operator: 'equals', value: 'web' },
    });
  });
});
