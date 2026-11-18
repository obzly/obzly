import { useEffect, useState } from 'react';

import { type AggregationBucket, type Panel, dashboardApi } from '../../../../entity/dashboards';

export interface PanelQuery {
  panel: Omit<Panel, 'id' | 'title' | 'gridPos'>;
  from: number;
  to: number;
}

export interface PanelData {
  /** null while loading or after an error. */
  buckets: AggregationBucket[] | null;
  error: string | null;
  isLoading: boolean;
}

const asPanel = (q: PanelQuery['panel']): Panel => ({
  ...q,
  id: 'logs-chart',
  title: 'Logs chart',
  gridPos: { x: 0, y: 0, w: 6, h: 8 },
});

/** Runs a panel definition through the dashboards panel query, as a saved panel would run. */
export const runPanelQuery = async (
  projectId: string,
  query: PanelQuery,
  signal?: AbortSignal,
): Promise<AggregationBucket[]> => {
  const response = await dashboardApi.executePanelQuery(
    projectId,
    {
      panel: asPanel(query.panel),
      timeRange: { from: new Date(query.from).toISOString(), to: new Date(query.to).toISOString() },
    },
    signal,
  );
  return response.buckets;
};

/**
 * Several panel queries at once, re-run when their definition or `refreshToken`
 * changes; a null entry is skipped (its result stays null).
 */
export const usePanelData = (
  projectId: string,
  queries: (PanelQuery | null)[],
  refreshToken: number,
): PanelData[] => {
  const key = JSON.stringify(queries);
  const [state, setState] = useState<{ key: string; data: PanelData[] }>({ key: '', data: [] });

  useEffect(() => {
    const abort = new AbortController();
    const parsed = JSON.parse(key) as (PanelQuery | null)[];
    setState({
      key,
      data: parsed.map((q) => ({ buckets: null, error: null, isLoading: q !== null })),
    });
    parsed.forEach((q, i) => {
      if (!q) return;
      runPanelQuery(projectId, q, abort.signal)
        .then((buckets) => {
          if (abort.signal.aborted) return;
          setState((prev) =>
            prev.key !== key
              ? prev
              : {
                  key,
                  data: prev.data.map((d, j) =>
                    j === i ? { buckets, error: null, isLoading: false } : d,
                  ),
                },
          );
        })
        .catch((e: unknown) => {
          if (abort.signal.aborted) return;
          const message = e instanceof Error ? e.message : 'Query failed';
          setState((prev) =>
            prev.key !== key
              ? prev
              : {
                  key,
                  data: prev.data.map((d, j) =>
                    j === i ? { buckets: null, error: message, isLoading: false } : d,
                  ),
                },
          );
        });
    });
    return () => abort.abort();
  }, [projectId, key, refreshToken]);

  if (state.key !== key) {
    return queries.map((q) => ({ buckets: null, error: null, isLoading: q !== null }));
  }
  return state.data;
};
