import { useEffect, useState } from 'react';

import { type QueryNode, queryApi } from '../../../entity/query';
import { anomalyCount, baselineRange } from '../lib/anomalies';
import type { TimeRange } from './TimeRangePickerComponent';

/**
 * How many patterns changed in the search window against the previous period (the
 * Anomalies drawer's default), from the backend's count-only mode; null while unknown,
 * on failure, or when the search can't be counted cheaply.
 */
export const useAnomalyCount = (
  projectId: string,
  query: QueryNode | null,
  timeRange: TimeRange | null,
  enabled: boolean,
  refreshToken?: number,
): number | null => {
  const [count, setCount] = useState<number | null>(null);

  useEffect(() => {
    setCount(null);
    if (!enabled || !timeRange) return;
    const abort = new AbortController();
    const base = baselineRange(timeRange, 'previous');
    queryApi
      .getPatternChanges(
        projectId,
        {
          query: query ?? undefined,
          current: { from: timeRange.from.toISOString(), to: timeRange.to.toISOString() },
          baseline: { from: base.from.toISOString(), to: base.to.toISOString() },
          countOnly: true,
        },
        abort.signal,
      )
      .then((response) => {
        if (!abort.signal.aborted) setCount(anomalyCount(response));
      })
      .catch(() => {
        // The count is a hint on a button; the drawer reports errors.
      });
    return () => abort.abort();
  }, [projectId, query, timeRange, enabled, refreshToken]);

  return count;
};
