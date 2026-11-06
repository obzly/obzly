import { describe, expect, it } from 'vitest';

import { costCell, errorRate, parsePrice, toUsagePoints } from './llmApps';

describe('costCell', () => {
  it('shows the cost, "no price set" or a dash', () => {
    expect(costCell({ costUsd: 48.2, hasTokens: true, unpriced: false })).toBe('$48.20');
    expect(costCell({ costUsd: null, hasTokens: true, unpriced: true })).toBe('no price set');
    expect(costCell({ costUsd: null, hasTokens: false, unpriced: false })).toBe('-');
    expect(costCell({ costUsd: 1.5, hasTokens: true, unpriced: true })).toBe('$1.50 + unpriced');
  });
});

describe('errorRate', () => {
  it('is 0 without calls', () => {
    expect(errorRate(3, 0)).toBe(0);
    expect(errorRate(1, 4)).toBe(0.25);
  });
});

describe('toUsagePoints', () => {
  it('sums input and output tokens per model', () => {
    expect(
      toUsagePoints([{ bucket: 1, model: '', calls: 2, inputTokens: 10, outputTokens: 5 }]),
    ).toEqual([{ bucket: 1, key: '(no model)', costUsd: 0, tokens: 15, requests: 2 }]);
  });
});

describe('parsePrice', () => {
  it('reads empty as not set and rejects negatives', () => {
    expect(parsePrice(' ')).toBeNull();
    expect(parsePrice('3.00')).toBe(3);
    expect(parsePrice('-1')).toBe('invalid');
    expect(parsePrice('abc')).toBe('invalid');
  });
});
