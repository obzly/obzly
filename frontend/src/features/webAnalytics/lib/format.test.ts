import { describe, expect, it } from 'vitest';

import { axisLabels, countryName, formatBytes, formatCount, formatMs, formatShare } from './format';

describe('formatCount', () => {
  it('compacts like the board', () => {
    expect(formatCount(912)).toBe('912');
    expect(formatCount(9999)).toBe('9,999');
    expect(formatCount(18_400)).toBe('18.4 k');
    expect(formatCount(2_410_000)).toBe('2.41 M');
    expect(formatCount(3_200_000_000)).toBe('3.20 B');
  });
});

describe('formatBytes', () => {
  it('uses binary units', () => {
    expect(formatBytes(512)).toBe('512 B');
    expect(formatBytes(1536)).toBe('1.5 KB');
    expect(formatBytes(186 * 1024 ** 3)).toBe('186 GB');
  });
});

describe('formatShare and formatMs', () => {
  it('keeps small shares readable', () => {
    expect(formatShare(0.842)).toBe('84.2%');
    expect(formatShare(0.0023)).toBe('0.23%');
    expect(formatShare(Number.NaN)).toBe('n/a');
    expect(formatMs(312.4)).toBe('312 ms');
    expect(formatMs(1500)).toBe('1.50 s');
  });
});

describe('countryName', () => {
  it('names ISO codes and the database miss', () => {
    expect(countryName('DE')).toBe('Germany');
    expect(countryName('??')).toBe('Unknown');
  });
});

describe('axisLabels', () => {
  it('picks five evenly spaced buckets', () => {
    const starts = Array.from({ length: 24 }, (_, i) =>
      new Date(Date.UTC(2026, 10, 18, i)).toISOString(),
    );
    expect(axisLabels(starts, 3600)).toHaveLength(5);
    expect(axisLabels([], 3600)).toEqual([]);
  });
});
