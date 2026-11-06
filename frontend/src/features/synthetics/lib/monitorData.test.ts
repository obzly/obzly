import { describe, expect, it } from 'vitest';

import { reuseUnchanged } from './monitorData';
import type { MonitorData, MonitorDataResult } from './useMonitorData';

const monitor = (overrides: Partial<MonitorData> = {}): MonitorData => ({
  state: { status: 'up', locations: [{ location: 'a', up: true, warn: false }] },
  uptime: 0.99,
  p95Ms: 210,
  strip: ['up', 'up', 'none'],
  ...overrides,
});

const result = (data: Record<string, MonitorData>): MonitorDataResult => ({
  data: new Map(Object.entries(data)),
  errors: [],
  loading: false,
  from: 1,
  to: 2,
  stepSeconds: 60,
});

describe('reuseUnchanged', () => {
  it('returns the previous result when a refresh brought the same numbers', () => {
    const previous = result({ c1: monitor(), c2: monitor() });
    expect(reuseUnchanged(previous, result({ c1: monitor(), c2: monitor() }))).toBe(previous);
  });

  it('keeps unchanged monitors and swaps only the changed one', () => {
    const previous = result({ c1: monitor(), c2: monitor() });
    const next = reuseUnchanged(
      previous,
      result({ c1: monitor(), c2: monitor({ strip: ['up', 'down', 'none'] }) }),
    );
    expect(next).not.toBe(previous);
    expect(next.data.get('c1')).toBe(previous.data.get('c1'));
    expect(next.data.get('c2')?.strip).toEqual(['up', 'down', 'none']);
  });

  it('notices a moved window, a new error and a removed monitor', () => {
    const previous = result({ c1: monitor(), c2: monitor() });
    expect(
      reuseUnchanged(previous, { ...result({ c1: monitor(), c2: monitor() }), to: 3 }),
    ).not.toBe(previous);
    expect(
      reuseUnchanged(previous, { ...result({ c1: monitor(), c2: monitor() }), errors: ['x'] }),
    ).not.toBe(previous);
    expect(reuseUnchanged(previous, result({ c1: monitor() })).data.size).toBe(1);
  });
});
