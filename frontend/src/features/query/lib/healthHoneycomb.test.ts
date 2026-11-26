import { describe, expect, it } from 'vitest';

import { fitRadius, flowHeight, groupBox, hexScale, honeycombColumns } from './healthHoneycomb';

describe('honeycomb layout', () => {
  it('lays a group out roughly square, one row for a few tiles', () => {
    expect(honeycombColumns(1)).toBe(1);
    expect(honeycombColumns(3)).toBe(3);
    const columns = honeycombColumns(100);
    expect(Math.ceil(100 / columns)).toBeLessThanOrEqual(columns);
  });

  it('wraps groups into rows like flex-wrap', () => {
    const box = { width: 100, height: 50 };

    expect(flowHeight([box, box, box], 230)).toBe(50 + 12 + 50);
    expect(flowHeight([box, box, box], 400)).toBe(50);
  });

  it('uses bigger hexagons when the window has room for them', () => {
    const groups = [10, 3, ...Array.from({ length: 40 }, () => 3)];

    const small = fitRadius(groups, 900, 380);
    const tall = fitRadius(groups, 1400, 900);

    expect(tall).toBeGreaterThan(small);
    expect(
      flowHeight(
        groups.map((n) => groupBox(n, tall)),
        1400,
      ),
    ).toBeLessThanOrEqual(900);
  });

  it('falls back to the smallest hexagons when nothing fits', () => {
    expect(
      fitRadius(
        Array.from({ length: 500 }, () => 20),
        300,
        200,
      ),
    ).toBe(10);
  });

  it('sizes hexagons so their areas keep the ratio of log lines', () => {
    const big = hexScale(1_000_000, 1_000_000);
    const quarter = hexScale(250_000, 1_000_000);

    expect(big).toBe(1);
    expect((quarter * quarter) / (big * big)).toBeCloseTo(0.25);
    expect(hexScale(10, 1_000_000)).toBe(0.3);
  });
});
