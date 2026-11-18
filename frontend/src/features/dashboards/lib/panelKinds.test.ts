import { describe, expect, it } from 'vitest';

import {
  changeVsPrevious,
  foldIntoOther,
  formatWindow,
  gaugeRatio,
  isAdditive,
  kindUnavailableReason,
  settingsForType,
  stackedSummary,
  statusRows,
  summaryValue,
  thresholdState,
  validateSettings,
  valuesByGroup,
  valuesOnGrid,
} from './panelKinds';

const MIN = 60_000;
const at = (minute: number) => new Date(Date.UTC(2026, 10, 18, 10, minute)).toISOString();
const grid = { fromMs: Date.parse(at(0)), toMs: Date.parse(at(4)), bucketMs: MIN };

describe('settingsForType', () => {
  it('keeps only the settings a type uses', () => {
    const settings = {
      min: 0,
      max: 1,
      thresholds: { warn: 0.8, critical: 0.9 },
      higherIs: 'bad' as const,
    };
    expect(settingsForType('gauge', settings)).toEqual(settings);
    expect(settingsForType('statusStrip', settings)).toEqual({
      thresholds: { warn: 0.8, critical: 0.9 },
      higherIs: 'bad',
    });
    expect(settingsForType('statTrend', settings)).toEqual({ higherIs: 'bad' });
    expect(settingsForType('line', settings)).toEqual({});
    expect(settingsForType('stat', settings)).toEqual({});
  });

  it('drops empty thresholds', () => {
    expect(settingsForType('gauge', { thresholds: {} })).toEqual({});
  });
});

describe('validateSettings', () => {
  it('needs a gauge maximum unless the unit is percent', () => {
    expect(validateSettings('gauge', {}, undefined)).toMatch(/maximum/);
    expect(validateSettings('gauge', {}, 'percent')).toBeNull();
    expect(validateSettings('gauge', { max: 100 }, undefined)).toBeNull();
  });

  it('rejects a min at or above the max', () => {
    expect(validateSettings('gauge', { min: 5, max: 5 }, undefined)).toMatch(/minimum/);
  });

  it('needs a threshold for status strips', () => {
    expect(validateSettings('statusStrip', {}, undefined)).toMatch(/threshold/);
    expect(validateSettings('statusStrip', { thresholds: { critical: 0 } }, undefined)).toBeNull();
  });

  it('checks threshold order against the direction', () => {
    const rising = { thresholds: { warn: 90, critical: 80 } };
    expect(validateSettings('gauge', { max: 100, ...rising }, undefined)).toMatch(/at or below/);
    expect(validateSettings('gauge', { max: 100, ...rising, higherIs: 'good' }, undefined)).toBe(
      null,
    );
    expect(
      validateSettings(
        'gauge',
        { max: 100, thresholds: { warn: 10, critical: 30 }, higherIs: 'good' },
        undefined,
      ),
    ).toMatch(/at or above/);
  });

  it('ignores settings the type drops', () => {
    expect(validateSettings('line', { min: 5, max: 1 }, undefined)).toBeNull();
  });
});

describe('thresholdState', () => {
  it('breaches upwards by default and downwards when higher is good', () => {
    const t = { warn: 80, critical: 90 };
    expect(thresholdState(50, t, undefined)).toBe('ok');
    expect(thresholdState(85, t, 'bad')).toBe('warn');
    expect(thresholdState(95, t, undefined)).toBe('fail');
    const good = { warn: 30, critical: 10 };
    expect(thresholdState(50, good, 'good')).toBe('ok');
    expect(thresholdState(20, good, 'good')).toBe('warn');
    expect(thresholdState(10, good, 'good')).toBe('fail');
  });

  it('is null without thresholds', () => {
    expect(thresholdState(1, undefined, undefined)).toBeNull();
    expect(thresholdState(1, {}, undefined)).toBeNull();
  });
});

describe('gaugeRatio', () => {
  it('places a value between min and max, clamped', () => {
    expect(gaugeRatio(50, 0, 100, undefined)).toBe(0.5);
    expect(gaugeRatio(150, 0, 100, undefined)).toBe(1);
    expect(gaugeRatio(0.42, undefined, undefined, 'percent')).toBe(0.42);
  });

  it('is null without a maximum', () => {
    expect(gaugeRatio(5, undefined, undefined, undefined)).toBeNull();
  });
});

describe('isAdditive and kindUnavailableReason', () => {
  it('treats counts as additive and durations and metrics as not', () => {
    expect(isAdditive({})).toBe(true);
    expect(isAdditive({ source: 'spans', metric: 'error_count' })).toBe(true);
    expect(isAdditive({ source: 'spans', metric: 'p95_duration_ms' })).toBe(false);
    expect(isAdditive({ source: 'metrics' })).toBe(false);
  });

  it('treats a logs count or sum as additive and an average or percentile as not', () => {
    expect(isAdditive({ measure: 'count' })).toBe(true);
    expect(isAdditive({ measure: 'sum' })).toBe(true);
    expect(isAdditive({ measure: 'avg' })).toBe(false);
    expect(isAdditive({ measure: 'p95' })).toBe(false);
    expect(isAdditive({ measure: 'distinct' })).toBe(false);
  });

  it('disables stacked bars and donuts for span durations only', () => {
    expect(kindUnavailableReason('stackedBar', 'spans', 'p95_duration_ms')).toMatch(/add up/);
    expect(kindUnavailableReason('donut', 'spans', 'avg_duration_ms')).toMatch(/parts/);
    expect(kindUnavailableReason('donut', 'spans', 'count')).toBeNull();
    expect(kindUnavailableReason('rankedList', 'spans', 'p95_duration_ms')).toBeNull();
    expect(kindUnavailableReason('stackedBar', 'metrics', undefined)).toBeNull();
  });
});

describe('summaryValue and valuesByGroup', () => {
  const metricBuckets = [
    { timestamp: at(0), groupKey: 'a', value: 1 },
    { timestamp: at(1), groupKey: 'a', value: 3 },
    { timestamp: at(0), groupKey: 'b', value: 5 },
  ];

  it('sums each metric series latest point; no data is null', () => {
    expect(summaryValue(metricBuckets, true)).toBe(8);
    expect(summaryValue([], true)).toBeNull();
    expect(summaryValue([], false)).toBe(0);
  });

  it('ranks groups largest first', () => {
    expect(valuesByGroup(metricBuckets, true)).toEqual([
      { key: 'b', value: 5 },
      { key: 'a', value: 3 },
    ]);
    expect(valuesByGroup([{ value: 2 }], false)).toEqual([{ key: '(none)', value: 2 }]);
  });

  it('folds the tail into Other', () => {
    const rows = [5, 4, 3, 2, 1].map((value, i) => ({ key: `k${i}`, value }));
    expect(foldIntoOther(rows, 3)).toEqual([
      { key: 'k0', value: 5 },
      { key: 'k1', value: 4 },
      { key: 'Other', value: 6 },
    ]);
    expect(foldIntoOther(rows, 6)).toBe(rows);
  });
});

describe('changeVsPrevious', () => {
  it('colours by good or bad, not by sign', () => {
    expect(changeVsPrevious(82, 100, undefined, 'good')).toEqual({ text: '−18%', tone: 'bad' });
    expect(changeVsPrevious(82, 100, undefined, 'bad')).toEqual({ text: '−18%', tone: 'good' });
    expect(changeVsPrevious(106.1, 100, undefined, undefined)).toEqual({
      text: '+6.1%',
      tone: 'neutral',
    });
  });

  it('uses points for percents and says n/a when unknown', () => {
    expect(changeVsPrevious(0.952, 0.998, 'percent', 'good')).toEqual({
      text: '−4.6 pt',
      tone: 'bad',
    });
    expect(changeVsPrevious(null, 1, undefined, 'good')).toEqual({ text: 'n/a', tone: 'neutral' });
    expect(changeVsPrevious(1, null, undefined, 'good')).toEqual({ text: 'n/a', tone: 'neutral' });
  });

  it('handles a zero baseline and no change', () => {
    expect(changeVsPrevious(0, 0, undefined, 'good').text).toBe('steady');
    expect(changeVsPrevious(5, 0, undefined, 'bad')).toEqual({ text: 'up from 0', tone: 'bad' });
    expect(changeVsPrevious(100, 100, undefined, 'bad').text).toBe('steady');
  });
});

describe('formatWindow', () => {
  it('names a window length', () => {
    expect(formatWindow(15 * MIN)).toBe('15m');
    expect(formatWindow(24 * 60 * MIN)).toBe('24h');
    expect(formatWindow(7 * 24 * 60 * MIN)).toBe('7d');
  });
});

describe('valuesOnGrid and statusRows', () => {
  it('fills empty cells with 0 when additive and with null otherwise', () => {
    const buckets = [
      { timestamp: at(1), value: 2 },
      { timestamp: at(1), value: 3 },
    ];
    expect(valuesOnGrid(buckets, grid, true)).toEqual([0, 5, 0, 0]);
    expect(valuesOnGrid(buckets, grid, false)).toEqual([null, 3, null, null]);
  });

  it('colours cells by threshold, grey for no data, worst row first', () => {
    const buckets = [
      { timestamp: at(0), groupKey: 'pod-a', value: 1 },
      { timestamp: at(1), groupKey: 'pod-a', value: 1 },
      { timestamp: at(0), groupKey: 'pod-b', value: 1 },
      { timestamp: at(2), groupKey: 'pod-b', value: 0 },
    ];
    const rows = statusRows(buckets, grid, {
      additive: false,
      thresholds: { critical: 0 },
      higherIs: 'good',
      unit: undefined,
    });
    expect(rows.map((r) => r.key)).toEqual(['pod-b', 'pod-a']);
    expect(rows[0].cells.map((c) => c.state)).toEqual(['ok', 'none', 'fail', 'none']);
    expect(rows[0].latest).toBe(0);
    expect(rows[1].cells.map((c) => c.state)).toEqual(['ok', 'ok', 'none', 'none']);
  });
});

describe('stackedSummary', () => {
  const buckets = [
    { timestamp: at(0), groupKey: 'logs', value: 10 },
    { timestamp: at(0), groupKey: 'traces', value: 5 },
    { timestamp: at(1), groupKey: 'logs', value: 20 },
    { timestamp: at(1), groupKey: 'traces', value: 15 },
  ];

  it('finds the peak bucket, the total and each share', () => {
    const summary = stackedSummary(buckets, true);
    expect(summary.peak).toEqual({ timestamp: at(1), total: 35 });
    expect(summary.total).toBe(50);
    expect(summary.series).toEqual([
      { key: 'logs', colorIndex: 0, total: 30, share: 0.6 },
      { key: 'traces', colorIndex: 1, total: 20, share: 0.4 },
    ]);
  });

  it('has no total or shares when values do not add up', () => {
    const summary = stackedSummary(buckets, false);
    expect(summary.total).toBeNull();
    expect(summary.series[0].share).toBeNull();
    expect(summary.peak?.total).toBe(35);
  });
});
