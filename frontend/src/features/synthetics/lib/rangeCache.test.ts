import { describe, expect, it, vi } from 'vitest';

import { rangeCache } from './rangeCache';

describe('rangeCache', () => {
  it('reuses a value until it expires, per key', async () => {
    let now = 0;
    const cache = rangeCache<number>(5 * 60_000, () => now);
    const load = vi.fn(async () => now);
    expect(await cache.load('24h|a', load)).toBe(0);
    now = 4 * 60_000;
    expect(await cache.load('24h|a', load)).toBe(0);
    expect(await cache.load('7d|a', load)).toBe(now);
    expect(load).toHaveBeenCalledTimes(2);
    now = 5 * 60_000;
    expect(await cache.load('24h|a', load)).toBe(now);
    expect(load).toHaveBeenCalledTimes(3);
  });

  it('keeps nothing from a failed load', async () => {
    const cache = rangeCache<number>();
    await expect(cache.load('k', () => Promise.reject(new Error('aborted')))).rejects.toThrow();
    expect(cache.get('k')).toBeUndefined();
    expect(await cache.load('k', async () => 7)).toBe(7);
  });
});
