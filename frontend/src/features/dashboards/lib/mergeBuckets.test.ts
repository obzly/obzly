import { describe, expect, it } from 'vitest';

import { mergeBuckets } from './mergeBuckets';

describe('mergeBuckets', () => {
  it('adds a bucket that comes again and keeps time then group order', () => {
    const totals = new Map();
    mergeBuckets(totals, [
      { timestamp: '2026-10-13T01:00:00Z', groupKey: 'info', value: 2 },
      { timestamp: '2026-10-13T00:00:00Z', groupKey: 'error', value: 1 },
    ]);
    const merged = mergeBuckets(totals, [
      { timestamp: '2026-10-13T01:00:00Z', groupKey: 'info', value: 3 },
      { timestamp: '2026-10-13T01:00:00Z', groupKey: 'error', value: 4 },
    ]);
    expect(merged).toEqual([
      { timestamp: '2026-10-13T00:00:00Z', groupKey: 'error', value: 1 },
      { timestamp: '2026-10-13T01:00:00Z', groupKey: 'error', value: 4 },
      { timestamp: '2026-10-13T01:00:00Z', groupKey: 'info', value: 5 },
    ]);
  });
});
