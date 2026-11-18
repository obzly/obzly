import { describe, expect, it } from 'vitest';

import type { Session } from '../../../entity/correlation';
import type { InventorySummary } from '../../../entity/inventory';
import type { TraceSummaryResponse } from '../../../entity/traces';
import {
  errorRate,
  formatRate,
  infraRows,
  logVolume,
  overviewWindow,
  peakBar,
  percentChange,
  pointChange,
  requestBars,
  serviceErrorRows,
  sessionsPerBucket,
  summarizeIncidents,
} from './overview';

const MINUTE = 60_000;
const HOUR = 60 * MINUTE;
const NOW = Date.parse('2026-11-18T12:00:00Z');

const session = (start: string, end: string, serviceName = 'checkout'): Session => ({
  id: start,
  projectId: 'p',
  serviceName,
  windowStart: start,
  windowEnd: end,
  triggerType: 'error_log',
  createdAt: start,
});

describe('overviewWindow', () => {
  it('ends at the next whole minute and compares with the same length before', () => {
    const window = overviewWindow('24h', NOW + 1000);
    expect(window.to).toBe(NOW + MINUTE);
    expect(window.from).toBe(NOW + MINUTE - 24 * HOUR);
    expect(window.previousFrom).toBe(NOW + MINUTE - 48 * HOUR);
    expect(window.bucketMs).toBe(HOUR);
  });

  it('buckets in whole minutes for every range', () => {
    for (const range of ['1h', '24h', '7d', '30d'] as const) {
      expect(overviewWindow(range, NOW).bucketMs % MINUTE).toBe(0);
    }
  });
});

describe('percentChange and pointChange', () => {
  it('colours by good or bad, not by sign', () => {
    expect(percentChange(90, 100, false)).toEqual({ text: '−10.0%', tone: 'good' });
    expect(percentChange(110, 100, false)).toEqual({ text: '+10.0%', tone: 'bad' });
    expect(percentChange(110, 100, true)).toEqual({ text: '+10.0%', tone: 'good' });
  });

  it('has nothing to say without a previous value', () => {
    expect(percentChange(5, undefined, true)).toBeUndefined();
    expect(percentChange(5, 0, true)).toBeUndefined();
    expect(pointChange(1.2, undefined)).toBeUndefined();
  });

  it('treats a rising error rate as bad', () => {
    expect(pointChange(1.8, 1.4)).toEqual({ text: '+0.4 pt', tone: 'bad' });
    expect(pointChange(1.0, 1.4)).toEqual({ text: '−0.4 pt', tone: 'good' });
    expect(pointChange(1.0, 1.02)).toEqual({ text: '0.0 pt', tone: 'neutral' });
  });
});

describe('logVolume', () => {
  const window = { from: NOW - HOUR, to: NOW, previousFrom: NOW - 2 * HOUR, bucketMs: 5 * MINUTE };
  const at = (ms: number) => new Date(ms).toISOString();

  it('splits the histogram into the range and the period before it', () => {
    const volume = logVolume(
      [
        { timestamp: at(NOW - 90 * MINUTE), value: 1800, groupKey: 'info' },
        { timestamp: at(NOW - 60 * MINUTE), value: 3000, groupKey: 'info' },
        { timestamp: at(NOW - 60 * MINUTE), value: 600, groupKey: 'error' },
        { timestamp: at(NOW - 5 * MINUTE), value: 3600 },
      ],
      window,
    );
    expect(volume.total).toBe(7200);
    expect(volume.rate).toBe(2);
    expect(volume.previousRate).toBe(0.5);
    expect(volume.trend).toHaveLength(12);
    expect(volume.trend[0]).toBe(3600);
    expect(volume.trend[11]).toBe(3600);
  });

  it('leaves the bucket straddling the range start out of the range', () => {
    const shifted = {
      ...window,
      from: window.from + 30_000,
      previousFrom: window.previousFrom + 30_000,
      to: window.to + 30_000,
    };
    const volume = logVolume(
      [
        { timestamp: at(NOW - 60 * MINUTE), value: 600 },
        { timestamp: at(NOW - 55 * MINUTE), value: 330 },
      ],
      shifted,
    );
    // From the first whole bucket (55 min ago) to the end: 55.5 minutes.
    expect(volume.total).toBe(330);
    expect(volume.rate).toBeCloseTo(330 / (55.5 * 60));
    expect(volume.previousRate).toBeCloseTo(600 / 3600);
  });

  it('leaves the previous rate unknown when nothing was logged then', () => {
    const volume = logVolume([{ timestamp: at(NOW - MINUTE), value: 60 }], window);
    expect(volume.previousRate).toBeUndefined();
  });
});

describe('formatRate', () => {
  it('uses per second from one a second, per minute below', () => {
    expect(formatRate(880)).toEqual({ value: '880', unit: '/s' });
    expect(formatRate(0.5)).toEqual({ value: '30', unit: '/min' });
    expect(formatRate(12_345)).toEqual({ value: '12.3 k', unit: '/s' });
  });
});

describe('errorRate', () => {
  it('is unknown without requests, never 0', () => {
    expect(errorRate(0, 0)).toBeUndefined();
    expect(errorRate(null, 10)).toBeUndefined();
    expect(errorRate(1, 50)).toBe(2);
  });
});

describe('requestBars', () => {
  const summary: TraceSummaryResponse = {
    current: { count: 30, errors: 3, p50Ms: 10, p95Ms: 40 },
    previous: { count: 20, errors: 0, p50Ms: 10, p95Ms: 50 },
    windowMinutes: 3,
    rootSpansOnly: true,
    stepMs: MINUTE,
    countTrend: [10, 0, 20],
    errorRateTrend: [10, 10],
    p50Trend: [],
    p95Trend: [],
    matchingTraces: 30,
    matchingTracesCapped: false,
    matchingTracesCap: 10_000,
  };

  it('maps error rates onto the buckets with requests and marks incidents', () => {
    const bars = requestBars(summary, NOW, [
      session(
        new Date(NOW + 2 * MINUTE + 5000).toISOString(),
        new Date(NOW + 3 * MINUTE).toISOString(),
      ),
    ]);
    expect(bars).toEqual([
      { at: NOW, ok: 9, errors: 1, incidents: 0 },
      { at: NOW + MINUTE, ok: 0, errors: 0, incidents: 0 },
      { at: NOW + 2 * MINUTE, ok: 18, errors: 2, incidents: 1 },
    ]);
    expect(peakBar(bars, MINUTE)).toEqual({ at: NOW + 2 * MINUTE, perMinute: 20 });
  });
});

describe('serviceErrorRows', () => {
  it('computes rates and point changes, unknown when the previous period had no requests', () => {
    const rows = serviceErrorRows({
      from: '',
      to: '',
      previousFrom: '',
      clamped: false,
      services: [
        {
          serviceName: 'checkout',
          requests: 200,
          errors: 10,
          previousRequests: 100,
          previousErrors: 2,
        },
        { serviceName: 'search', requests: 50, errors: 1, previousRequests: 0, previousErrors: 0 },
        { serviceName: 'idle', requests: 0, errors: 0, previousRequests: 5, previousErrors: 0 },
      ],
    });
    expect(rows).toHaveLength(2);
    expect(rows[0]).toMatchObject({
      name: 'checkout',
      rate: 5,
      change: { text: '+3.0 pt', tone: 'bad' },
    });
    expect(rows[1].change).toBeUndefined();
  });
});

describe('summarizeIncidents', () => {
  it('groups sessions by how recently their last signal arrived', () => {
    const iso = (ms: number) => new Date(ms).toISOString();
    const summary = summarizeIncidents(
      [
        session(iso(NOW - 20 * MINUTE), iso(NOW - 5 * MINUTE)),
        session(iso(NOW - 3 * HOUR), iso(NOW - 2 * HOUR), 'payments'),
        session(iso(NOW - 50 * HOUR), iso(NOW - 49 * HOUR)),
      ],
      NOW,
    );
    expect(summary.counts).toEqual({ ongoing: 1, recent: 1, earlier: 1 });
    expect(summary.total).toBe(3);
    expect(summary.medianMinutes).toBe(60);
    expect(summary.topService).toBe('checkout');
  });

  it('has no median without sessions', () => {
    expect(summarizeIncidents([], NOW).medianMinutes).toBeUndefined();
  });

  it('counts sessions per bucket by when they opened', () => {
    const window = {
      from: NOW - HOUR,
      to: NOW,
      previousFrom: NOW - 2 * HOUR,
      bucketMs: 30 * MINUTE,
    };
    const iso = (ms: number) => new Date(ms).toISOString();
    expect(
      sessionsPerBucket(
        [session(iso(NOW - 50 * MINUTE), iso(NOW)), session(iso(NOW - 10 * MINUTE), iso(NOW))],
        window,
      ),
    ).toEqual([1, 1]);
  });
});

describe('infraRows', () => {
  it('lists kinds in hierarchy order with health counts', () => {
    const summary: InventorySummary = {
      total: 10,
      kinds: {
        pod: { healthy: 5, warning: 1 },
        node: { healthy: 2, critical: 1, unknown: 1 },
        namespace: {},
      },
      health: {},
      states: {},
      projects: [],
    };
    expect(infraRows(summary)).toEqual([
      { kind: 'node', total: 4, healthy: 2, warning: 0, critical: 1, other: 1 },
      { kind: 'pod', total: 6, healthy: 5, warning: 1, critical: 0, other: 0 },
    ]);
  });
});
