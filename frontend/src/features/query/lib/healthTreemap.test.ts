import { describe, expect, it } from 'vitest';

import type { HealthGroup, HealthTile } from '../../../entity/query';
import { layoutHealthMap, splitReadable, squarify } from './healthTreemap';

const tile = (name: string, total: number, errors = 0): HealthTile => ({
  name,
  total,
  errors,
  warnings: 0,
  previous: 0,
});
const group = (name: string, tiles: HealthTile[]): HealthGroup => ({
  name,
  tiles,
  total: tiles.reduce((sum, t) => sum + t.total, 0),
});
const LAYOUT = { tile: { w: 64, h: 34 }, group: { w: 96, h: 60 }, header: 20 };

describe('squarify', () => {
  it('fills the rectangle with areas in proportion to the weights', () => {
    const weights = [6, 6, 4, 3, 2, 2, 1];

    const placed = squarify(weights, (w) => w, { x: 0, y: 0, w: 600, h: 400 });

    expect(placed).toHaveLength(weights.length);
    const total = weights.reduce((a, b) => a + b, 0);
    placed.forEach(({ item, rect }) => {
      expect(rect.w * rect.h).toBeCloseTo((item / total) * 600 * 400, 3);
      expect(rect.x + rect.w).toBeLessThanOrEqual(600 + 1e-6);
      expect(rect.y + rect.h).toBeLessThanOrEqual(400 + 1e-6);
    });
  });

  it('keeps tiles far closer to square than one stacked column', () => {
    const weights = Array.from({ length: 12 }, (_, i) => 12 - i);

    const placed = squarify(weights, (w) => w, { x: 0, y: 0, w: 900, h: 380 });

    const worst = Math.max(...placed.map(({ rect }) => Math.max(rect.w / rect.h, rect.h / rect.w)));
    expect(worst).toBeLessThan(4);
  });
});

describe('splitReadable', () => {
  it('rolls items too small to read into the rest, which is readable itself', () => {
    const weights = [1000, 500, 5, 4, 3, 2, 1];

    const { shown, rest } = splitReadable(weights, (w) => w, 100_000, 3000);

    expect(shown).toEqual([1000, 500]);
    expect(rest).toEqual([5, 4, 3, 2, 1]);
  });

  it('shows a single leftover as itself', () => {
    const { shown, rest } = splitReadable([1000, 1], (w) => w, 100_000, 3000);

    expect(shown).toEqual([1000, 1]);
    expect(rest).toEqual([]);
  });
});

describe('layoutHealthMap', () => {
  it('rolls hundreds of small workloads into "+N more" tiles that stay readable', () => {
    const groups = [
      group('ns-big', [
        tile('admin', 29_000_000),
        ...Array.from({ length: 40 }, (_, i) => tile(`job-${i}`, 2_000 + i, 50)),
      ]),
      ...Array.from({ length: 60 }, (_, i) =>
        group(`ns-${i}`, [tile(`web-${i}`, 30_000 + i * 1_000)]),
      ),
    ];

    const blocks = layoutHealthMap(groups, 1500, 380, LAYOUT);

    const mergedGroups = blocks.find((b) => b.merged);
    expect(mergedGroups?.merged?.length).toBeGreaterThan(0);
    const big = blocks.find((b) => b.group.name === 'ns-big');
    const mergedTile = big?.tiles.find((t) => t.merged);
    expect(mergedTile?.merged).toHaveLength(40);
    expect(mergedTile?.tile.errors).toBe(40 * 50);
    for (const b of blocks) {
      expect(b.rect.w).toBeGreaterThanOrEqual(LAYOUT.group.w - 0.5);
      expect(b.rect.h).toBeGreaterThanOrEqual(LAYOUT.group.h - 0.5);
      for (const t of b.tiles) {
        expect(t.rect.w).toBeGreaterThanOrEqual(LAYOUT.tile.w - 0.5);
        expect(t.rect.h).toBeGreaterThanOrEqual(LAYOUT.tile.h - 0.5);
      }
    }
    const shownGroups =
      blocks.filter((b) => !b.merged).length + (mergedGroups?.merged?.length ?? 0);
    expect(shownGroups).toBe(groups.length);
  });

  it('lays one group out without room for a header when zoomed in', () => {
    const blocks = layoutHealthMap([group('ns', [tile('a', 10), tile('b', 5)])], 400, 300, {
      ...LAYOUT,
      header: 0,
    });

    expect(blocks).toHaveLength(1);
    expect(blocks[0].tiles.map((t) => t.rect.y)).toContain(0);
  });

  it('stacks a namespace too narrow for its workloads side by side', () => {
    const blocks = layoutHealthMap(
      [group('ns', [tile('frontend', 1_500_000), tile('vendor', 80_000), tile('api', 70_000)])],
      106,
      124,
      LAYOUT,
    );

    const tiles = blocks[0].tiles;
    expect(tiles.map((t) => t.tile.name)).toEqual(['frontend', '+2 more']);
    for (const t of tiles) {
      expect(t.rect.w).toBeGreaterThanOrEqual(LAYOUT.tile.w);
      expect(t.rect.h).toBeGreaterThanOrEqual(LAYOUT.tile.h);
    }
  });
});
