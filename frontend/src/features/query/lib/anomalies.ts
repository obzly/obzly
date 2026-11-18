import type dayjs from 'dayjs';

import type { PatternChangesResponse } from '../../../entity/query';

export type Baseline = 'previous' | 'day' | 'week';

interface Range {
  from: dayjs.Dayjs;
  to: dayjs.Dayjs;
}

/** The range a search window is compared with. */
export const baselineRange = (range: Range, baseline: Baseline): Range => {
  if (baseline === 'day')
    return { from: range.from.subtract(1, 'day'), to: range.to.subtract(1, 'day') };
  if (baseline === 'week')
    return { from: range.from.subtract(1, 'week'), to: range.to.subtract(1, 'week') };
  const length = range.to.diff(range.from);
  return { from: range.from.subtract(length, 'ms'), to: range.from };
};

/** New, spiking and dropping patterns together; null when the backend didn't count. */
export const anomalyCount = (response: PatternChangesResponse): number | null =>
  response.counted
    ? (response.newCount ?? 0) + (response.spikeCount ?? 0) + (response.dropCount ?? 0)
    : null;
