import type { CheckView, PromQLResponse } from '../../../entity/synthetics';

// Monitors per query: a few series each (one per location), well under the 10000-series limit.
const CHUNK = 250;

/**
 * Runs the queries for `size` monitors at a time and merges the series. A truncated answer (an older
 * Site keeps its 100-series limit) is retried in halves, so every monitor gets its results.
 */
export const queryInChunks = async (
  checks: CheckView[],
  run: (ids: string[]) => Promise<PromQLResponse[]>,
  size = CHUNK,
): Promise<PromQLResponse[]> => {
  const parts: PromQLResponse[][] = [];
  const load = async (ids: string[]): Promise<void> => {
    const results = await run(ids);
    if (ids.length > 1 && results.some((result) => result.truncated)) {
      const half = Math.ceil(ids.length / 2);
      await Promise.all([load(ids.slice(0, half)), load(ids.slice(half))]);
      return;
    }
    parts.push(results);
  };
  const ids = checks.map((check) => check.id);
  const chunks: string[][] = [];
  for (let i = 0; i < ids.length; i += size) chunks.push(ids.slice(i, i + size));
  await Promise.all(chunks.map(load));
  if (parts.length === 0) return [];
  return parts[0].map((first, index) => ({
    ...first,
    series: parts.flatMap((part) => part[index].series),
    truncated: false,
  }));
};
