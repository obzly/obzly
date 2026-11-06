import type { AggregationBucket } from '../../../entity/dashboards/model/AggregationResponse';

/**
 * Adds a streamed panel query's buckets into totals (keyed by time and
 * group) and returns every bucket so far, oldest first then by group - the
 * order a one-shot panel query answers in.
 */
export const mergeBuckets = (
  totals: Map<string, AggregationBucket>,
  buckets: AggregationBucket[],
): AggregationBucket[] => {
  for (const bucket of buckets) {
    const key = `${bucket.timestamp ?? ''}|${bucket.groupKey ?? ''}`;
    const existing = totals.get(key);
    totals.set(
      key,
      existing ? { ...existing, value: existing.value + bucket.value } : { ...bucket },
    );
  }
  return [...totals.values()].sort((a, b) => {
    const byTime = (a.timestamp ?? '').localeCompare(b.timestamp ?? '');
    return byTime !== 0 ? byTime : (a.groupKey ?? '').localeCompare(b.groupKey ?? '');
  });
};
