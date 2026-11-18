import { type DependencyList, useEffect, useState } from 'react';

export interface Loaded<T> {
  data?: T;
  error?: string;
  loading: boolean;
}

/**
 * One panel's request. Each panel loads on its own, so they run in parallel and one failing
 * shows "n/a" in that panel only. A reload keeps the last answer on screen until the new one
 * lands; a slower, stale answer is dropped.
 */
export const useLoad = <T>(
  load: ((signal: AbortSignal) => Promise<T>) | undefined,
  deps: DependencyList,
): Loaded<T> => {
  const [state, setState] = useState<Loaded<T>>({ loading: !!load });

  useEffect(() => {
    if (!load) {
      setState({ loading: false });
      return;
    }
    const controller = new AbortController();
    setState((previous) => ({ ...previous, loading: true }));
    load(controller.signal).then(
      (data) => {
        if (!controller.signal.aborted) setState({ data, loading: false });
      },
      (reason: unknown) => {
        if (controller.signal.aborted) return;
        setState({ error: (reason as Error)?.message || 'Failed to load', loading: false });
      },
    );
    return () => controller.abort();
  }, deps);

  return state;
};
