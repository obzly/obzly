import { useEffect, useState } from 'react';

import { type TraceSummaryTotals, traceApi } from '../../../entity/traces';

export type { TraceSummaryTotals } from '../../../entity/traces';

export interface TraceSummaryScope {
  from: string;
  to: string;
  serviceName?: string;
  operationName?: string;
  minDurationMs?: number;
  maxDurationMs?: number;
  errorsOnly?: boolean;
}

export interface TraceSummaryStats {
  current: TraceSummaryTotals;
  previous: TraceSummaryTotals;
  windowMinutes: number;
  /** Root spans (requests) when no service is picked; that service's spans otherwise. */
  rootSpansOnly: boolean;
  countTrend: number[];
  errorRateTrend: number[];
  p50Trend: number[];
  p95Trend: number[];
}

/** Traces matching the search; `capped` means "count or more". */
export interface TraceMatchCount {
  count: number;
  capped: boolean;
}

/**
 * Requests, errors and latency percentiles for the searched range (and the
 * same-length range before it), plus how many traces match, from one
 * backend call. With `showTiles` off only the count is asked for.
 */
export const useTraceSummary = (
  projectId: string,
  scope: TraceSummaryScope | null,
  showTiles: boolean,
): { stats: TraceSummaryStats | null; matching: TraceMatchCount | null; isLoading: boolean } => {
  const [stats, setStats] = useState<TraceSummaryStats | null>(null);
  const [matching, setMatching] = useState<TraceMatchCount | null>(null);
  const [isLoading, setIsLoading] = useState(false);

  useEffect(() => {
    if (!scope) return;

    const abort = new AbortController();
    setIsLoading(true);
    traceApi
      .getTraceSummary(
        projectId,
        {
          timeRange: { from: scope.from, to: scope.to },
          serviceName: scope.serviceName,
          operationName: scope.operationName,
          minDurationMs: scope.minDurationMs,
          maxDurationMs: scope.maxDurationMs,
          errorsOnly: scope.errorsOnly || undefined,
          countOnly: !showTiles || undefined,
        },
        abort.signal,
      )
      .then((response) => {
        if (abort.signal.aborted) return;
        setMatching({ count: response.matchingTraces, capped: response.matchingTracesCapped });
        setStats(
          showTiles
            ? {
                current: response.current,
                previous: response.previous,
                windowMinutes: response.windowMinutes,
                rootSpansOnly: response.rootSpansOnly,
                countTrend: response.countTrend,
                errorRateTrend: response.errorRateTrend,
                p50Trend: response.p50Trend,
                p95Trend: response.p95Trend,
              }
            : null,
        );
      })
      .catch(() => {
        if (abort.signal.aborted) return;
        // Unknown, never 0: the tiles show n/a and the header drops the count.
        setMatching(null);
        setStats(null);
      })
      .finally(() => {
        if (!abort.signal.aborted) setIsLoading(false);
      });

    return () => abort.abort();
  }, [
    projectId,
    scope?.from,
    scope?.to,
    scope?.serviceName,
    scope?.operationName,
    scope?.minDurationMs,
    scope?.maxDurationMs,
    scope?.errorsOnly,
    showTiles,
  ]);

  return { stats, matching, isLoading };
};
