import { useCallback } from 'react';

import { useTheme } from '../../../shared/theme';

// Hashed, not random, so a service keeps its colour across renders and views.
// `colors` is the theme's chart series (useTheme().chartColors).
export function colorForService(serviceName: string, colors: string[]): string {
  let hash = 0;
  for (let i = 0; i < serviceName.length; i++) {
    hash = (hash * 31 + serviceName.charCodeAt(i)) >>> 0;
  }
  return colors[hash % colors.length];
}

/** colorForService bound to the active theme's chart colours. */
export function useServiceColor(): (serviceName: string) => string {
  const { chartColors } = useTheme();
  return useCallback(
    (serviceName: string) => colorForService(serviceName, chartColors),
    [chartColors],
  );
}

export function formatDurationMs(durationMs: number): string {
  if (durationMs < 1) return `${(durationMs * 1000).toFixed(0)}µs`;
  if (durationMs < 1000) return `${durationMs.toFixed(durationMs < 10 ? 2 : 0)}ms`;
  return `${(durationMs / 1000).toFixed(2)}s`;
}
