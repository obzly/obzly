import { describe, expect, it } from 'vitest';

import { bucketLabel, bucketTotals, peakBucket } from './series';

describe('bucketTotals', () => {
  it('sums groups per bucket and fills gaps with zero', () => {
    const points = [
      { bucket: 0, v: 1 },
      { bucket: 0, v: 2 },
      { bucket: 120_000, v: 4 },
    ];
    expect(bucketTotals(points, (p) => p.v, 60)).toEqual([
      { bucket: 0, value: 3 },
      { bucket: 60_000, value: 0 },
      { bucket: 120_000, value: 4 },
    ]);
  });

  it('is empty without points', () => {
    expect(bucketTotals([], () => 1, 60)).toEqual([]);
  });
});

describe('peakBucket', () => {
  it('finds the highest bucket, or null when all are zero', () => {
    expect(
      peakBucket([
        { bucket: 1, value: 2 },
        { bucket: 2, value: 5 },
      ]),
    ).toEqual({ bucket: 2, value: 5 });
    expect(peakBucket([{ bucket: 1, value: 0 }])).toBeNull();
  });
});

describe('bucketLabel', () => {
  it('names the bucket size', () => {
    expect(bucketLabel(3600)).toBe('hour');
    expect(bucketLabel(300)).toBe('5 minutes');
    expect(bucketLabel(86400)).toBe('day');
    expect(bucketLabel(45)).toBe('45 seconds');
  });
});
