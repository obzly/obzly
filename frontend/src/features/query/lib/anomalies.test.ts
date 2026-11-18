import dayjs from 'dayjs';
import { describe, expect, it } from 'vitest';

import { anomalyCount, baselineRange } from './anomalies';

describe('baselineRange', () => {
  const range = { from: dayjs('2026-11-18T10:00:00Z'), to: dayjs('2026-11-18T11:00:00Z') };

  it('takes the period just before for "previous"', () => {
    const base = baselineRange(range, 'previous');
    expect(base.from.toISOString()).toBe('2026-11-18T09:00:00.000Z');
    expect(base.to.toISOString()).toBe('2026-11-18T10:00:00.000Z');
  });

  it('shifts by a day or a week', () => {
    expect(baselineRange(range, 'day').from.toISOString()).toBe('2026-11-17T10:00:00.000Z');
    expect(baselineRange(range, 'week').to.toISOString()).toBe('2026-11-11T11:00:00.000Z');
  });
});

describe('anomalyCount', () => {
  const base = { changes: [], currentTotal: 0, baselineTotal: 0 };

  it('adds the three kinds when counted', () => {
    expect(anomalyCount({ ...base, counted: true, newCount: 2, spikeCount: 1, dropCount: 4 })).toBe(
      7,
    );
    expect(anomalyCount({ ...base, counted: true, newCount: 0, spikeCount: 0, dropCount: 0 })).toBe(
      0,
    );
  });

  it('is unknown when the backend did not count (or is older)', () => {
    expect(anomalyCount({ ...base, counted: false, newCount: 0 })).toBeNull();
    expect(anomalyCount(base)).toBeNull();
  });
});
