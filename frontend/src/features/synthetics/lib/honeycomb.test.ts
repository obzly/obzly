import { describe, expect, it } from 'vitest';

import { groupByFolder, hexRadius, latencyBand, layoutHexes, topFolder } from './honeycomb';

describe('layoutHexes', () => {
  it('fills rows to the width and shifts every other row by half a cell', () => {
    const r = 10;
    const cellWidth = Math.sqrt(3) * r;
    const layout = layoutHexes(7, 100, r, 2);
    expect(layout.columns).toBe(4);
    expect(layout.cells).toHaveLength(7);
    expect(layout.cells[4].cx - layout.cells[0].cx).toBeCloseTo((cellWidth + 2) / 2);
    expect(layout.cells[4].cy).toBeGreaterThan(layout.cells[0].cy);
  });

  it('keeps every cell inside the width', () => {
    const r = 6;
    const layout = layoutHexes(300, 320, r);
    const right = Math.max(...layout.cells.map((cell) => cell.cx + (Math.sqrt(3) * r) / 2));
    expect(right).toBeLessThanOrEqual(320);
  });

  it('has no height without cells', () => {
    expect(layoutHexes(0, 300, 10).height).toBe(0);
  });
});

describe('hexRadius', () => {
  it('shrinks cells as monitors grow', () => {
    expect(hexRadius(20)).toBeGreaterThan(hexRadius(100));
    expect(hexRadius(100)).toBeGreaterThan(hexRadius(500));
    expect(hexRadius(500, true)).toBeLessThan(hexRadius(500));
  });
});

describe('latencyBand', () => {
  it('bands p95 values and marks missing timings', () => {
    expect(latencyBand(undefined)).toBe(-1);
    expect(latencyBand(120)).toBe(0);
    expect(latencyBand(300)).toBe(1);
    expect(latencyBand(1499)).toBe(2);
    expect(latencyBand(9000)).toBe(3);
  });
});

describe('groupByFolder', () => {
  it('groups by top-level folder, top level last, order kept', () => {
    const groups = groupByFolder([
      { id: 'a', folder: 'Payments/EU' },
      { id: 'b', folder: '' },
      { id: 'c', folder: 'Identity' },
      { id: 'd', folder: 'Payments/US' },
    ]);
    expect(groups.map((g) => [g.folder, g.items.map((i) => i.id)])).toEqual([
      ['Identity', ['c']],
      ['Payments', ['a', 'd']],
      ['', ['b']],
    ]);
  });

  it('reads the top folder of a path', () => {
    expect(topFolder('Payments/EU')).toBe('Payments');
    expect(topFolder('')).toBe('');
  });
});
