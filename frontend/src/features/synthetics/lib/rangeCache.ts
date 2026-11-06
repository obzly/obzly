/** How long values over the whole range (uptime, percentiles, certificates) are reused. */
export const RANGE_VALUES_TTL_MS = 5 * 60_000;

export interface RangeCache<T> {
  get: (key: string) => T | undefined;
  /** The value for key, loading (and keeping) it when missing or expired. */
  load: (key: string, load: () => Promise<T>) => Promise<T>;
}

/**
 * Remembers loaded values per key for ttlMs: values over a day or a week
 * barely move in a minute, and their queries are the expensive ones. Only
 * successful loads are kept.
 */
export const rangeCache = <T>(
  ttlMs = RANGE_VALUES_TTL_MS,
  now: () => number = Date.now,
): RangeCache<T> => {
  const entries = new Map<string, { at: number; value: T }>();
  const get = (key: string) => {
    const entry = entries.get(key);
    if (!entry) return undefined;
    if (now() - entry.at < ttlMs) return entry.value;
    entries.delete(key);
    return undefined;
  };
  return {
    get,
    load: async (key, load) => {
      const cached = get(key);
      if (cached !== undefined) return cached;
      const value = await load();
      const at = now();
      for (const [other, entry] of entries) {
        if (at - entry.at >= ttlMs) entries.delete(other);
      }
      entries.set(key, { at, value });
      return value;
    },
  };
};
