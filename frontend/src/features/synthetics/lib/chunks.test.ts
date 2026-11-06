import { describe, expect, it } from 'vitest';

import type { CheckView, PromQLResponse } from '../../../entity/synthetics';
import { queryInChunks } from './chunks';

const checks = (count: number) =>
  Array.from({ length: count }, (_, i) => ({ id: `c${i}` }) as CheckView);

// Answers like a Site with a series limit: one series per check, truncated past `limit`.
const site =
  (limit: number, calls: string[][]) =>
  async (ids: string[]): Promise<PromQLResponse[]> => {
    calls.push(ids);
    const series = ids.map((id) => ({ labels: { check_id: id }, points: [] }));
    return [
      { resultType: 'vector', series: series.slice(0, limit), truncated: ids.length > limit },
    ];
  };

describe('queryInChunks', () => {
  it('splits monitors into chunks and merges every series', async () => {
    const calls: string[][] = [];
    const [result] = await queryInChunks(checks(600), site(10000, calls), 250);
    expect(calls.map((ids) => ids.length)).toEqual([250, 250, 100]);
    expect(result.series).toHaveLength(600);
    expect(result.truncated).toBe(false);
  });

  it('retries truncated chunks in halves until each fits an older 100-series Site', async () => {
    const calls: string[][] = [];
    const [result] = await queryInChunks(checks(250), site(100, calls), 250);
    expect(new Set(result.series.map((item) => item.labels.check_id)).size).toBe(250);
    expect(Math.max(...calls.slice(1).map((ids) => ids.length))).toBeLessThanOrEqual(125);
  });

  it('returns nothing for no monitors', async () => {
    expect(await queryInChunks([], site(100, []))).toEqual([]);
  });
});
