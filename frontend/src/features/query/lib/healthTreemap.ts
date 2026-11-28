import type { HealthGroup, HealthTile } from '../../../entity/query';

export interface Rect {
  x: number;
  y: number;
  w: number;
  h: number;
}

export interface MapTile {
  tile: HealthTile;
  rect: Rect;
  /** Set on a "+N more" tile: the tiles rolled into it. */
  merged?: string[];
}

export interface MapBlock {
  group: HealthGroup;
  rect: Rect;
  tiles: MapTile[];
  /** Set on a "+N more" block: the groups rolled into it. */
  merged?: string[];
}

export interface HealthMapLayout {
  /** Smallest tile that can show its name (px); smaller ones roll into "+N more". */
  tile: { w: number; h: number };
  /** Smallest group block, header included (px). */
  group: { w: number; h: number };
  /** Height of a group's name row; 0 lays the tiles out without one. */
  header: number;
}

/**
 * Squarified treemap (Bruls, Huizing, van Wijk): fills rows along the shorter
 * side while that keeps the worst aspect ratio from growing, so tiles stay
 * close to square instead of becoming slivers. Items must be sorted by weight,
 * largest first; items without weight are left out.
 */
export const squarify = <T>(
  items: T[],
  weight: (item: T) => number,
  rect: Rect,
): { item: T; rect: Rect }[] => {
  const weighted = items.filter((item) => weight(item) > 0);
  const total = weighted.reduce((sum, item) => sum + weight(item), 0);
  if (!weighted.length || rect.w <= 0 || rect.h <= 0) return [];
  const scale = (rect.w * rect.h) / total;
  const areas = weighted.map((item) => weight(item) * scale);
  const placed: { item: T; rect: Rect }[] = [];
  const free = { ...rect };
  let row: number[] = [];

  const worst = (indexes: number[], side: number) => {
    const values = indexes.map((i) => areas[i]);
    const sum = values.reduce((a, b) => a + b, 0);
    return Math.max(
      (side * side * Math.max(...values)) / (sum * sum),
      (sum * sum) / (side * side * Math.min(...values)),
    );
  };
  const layRow = () => {
    const sum = row.reduce((s, i) => s + areas[i], 0);
    if (free.w >= free.h) {
      const width = Math.min(sum / free.h, free.w);
      let y = free.y;
      for (const i of row) {
        const height = areas[i] / width;
        placed.push({ item: weighted[i], rect: { x: free.x, y, w: width, h: height } });
        y += height;
      }
      free.x += width;
      free.w -= width;
    } else {
      const height = Math.min(sum / free.w, free.h);
      let x = free.x;
      for (const i of row) {
        const width = areas[i] / height;
        placed.push({ item: weighted[i], rect: { x, y: free.y, w: width, h: height } });
        x += width;
      }
      free.y += height;
      free.h -= height;
    }
    row = [];
  };

  for (let i = 0; i < weighted.length;) {
    const side = Math.min(free.w, free.h);
    if (!row.length || worst([...row, i], side) <= worst(row, side)) {
      row.push(i);
      i++;
    } else {
      layRow();
    }
  }
  if (row.length) layRow();
  return placed;
};

/**
 * Splits items (largest first) into the ones big enough to read in this area
 * and the rest, which the map shows as one "+N more" item. The first item is
 * always shown and a single leftover is shown as itself. restWeight is the
 * weight to lay the rest out with: grown, when needed, so "+N more" is
 * readable too, without pushing a shown item below minArea.
 */
export const splitReadable = <T>(
  items: T[],
  weight: (item: T) => number,
  area: number,
  minArea: number,
): { shown: T[]; rest: T[]; restWeight: number } => {
  const total = items.reduce((sum, item) => sum + weight(item), 0);
  if (total <= 0 || minArea >= area) return { shown: items, rest: [], restWeight: 0 };
  let count = 0;
  while (count < items.length && (count === 0 || (weight(items[count]) / total) * area >= minArea))
    count++;
  if (items.length - count === 1) count++;
  if (count === items.length) return { shown: items, rest: [], restWeight: 0 };

  // The rest takes at least minArea, leaving area - minArea for the shown items.
  let shownWeight = items.slice(0, count).reduce((sum, item) => sum + weight(item), 0);
  while (count > 1 && (weight(items[count - 1]) / shownWeight) * (area - minArea) < minArea) {
    count--;
    shownWeight -= weight(items[count]);
  }
  const restWeight = total - shownWeight;
  const readable = (minArea * shownWeight) / (area - minArea);
  return {
    shown: items.slice(0, count),
    rest: items.slice(count),
    restWeight: Math.max(restWeight, readable),
  };
};

/** One tile adding up several (a "+N more" tile). */
export const sumTiles = (name: string, tiles: HealthTile[]): HealthTile => ({
  name,
  total: tiles.reduce((sum, t) => sum + t.total, 0),
  errors: tiles.reduce((sum, t) => sum + t.errors, 0),
  warnings: tiles.reduce((sum, t) => sum + t.warnings, 0),
  previous: tiles.reduce((sum, t) => sum + t.previous, 0),
});

const byTotal = <T extends { total: number }>(items: T[]) =>
  [...items].sort((a, b) => b.total - a.total);

/**
 * Lays the health map out in width × height: groups as blocks with their name
 * on top and their tiles inside, every block and tile big enough to read.
 */
/**
 * Lays items out in rect so each one is at least minSize: items that would be
 * smaller roll into one "+N more" item (rest), which gets enough room to be
 * readable itself. Size is checked by width and height, not area alone: a
 * leftover strip next to a dominant item can have the area and still be a
 * sliver.
 */
export const fitReadable = <T>(
  items: T[],
  weight: (item: T) => number,
  rect: Rect,
  minSize: { w: number; h: number },
): { placed: { item: T; rect: Rect }[]; rest: T[]; restRect?: Rect } => {
  const fits = (r: Rect) => r.w >= minSize.w - 0.5 && r.h >= minSize.h - 0.5;
  const minArea = minSize.w * minSize.h;
  let { shown } = splitReadable(items, weight, rect.w * rect.h, minArea);
  let boost = 1;
  for (;;) {
    const rest = items.slice(shown.length);
    const shownWeight = shown.reduce((sum, item) => sum + weight(item), 0);
    const restWeight = rest.reduce((sum, item) => sum + weight(item), 0);
    const reserve =
      rest.length && rect.w * rect.h > minArea
        ? ((minArea * shownWeight) / (rect.w * rect.h - minArea)) * boost
        : 0;
    const entries: { item?: T; weight: number }[] = shown.map((item) => ({
      item,
      weight: weight(item),
    }));
    if (rest.length) entries.push({ weight: Math.max(restWeight, reserve) });
    const laid = squarify(byWeight(entries), (e) => e.weight, rect);
    const restRect = laid.find((l) => !l.item.item)?.rect;
    const small = laid.filter((l) => l.item.item && !fits(l.rect));
    if (entries.length === 1 || (!small.length && (!restRect || fits(restRect)))) {
      return {
        placed: laid
          .filter((l) => l.item.item)
          .map((l) => ({ item: l.item.item as T, rect: l.rect })),
        rest,
        restRect,
      };
    }
    if (shown.length > 1 && (small.length || boost >= MAX_BOOST)) {
      // Roll the smallest shown item in with the rest.
      shown = shown.slice(0, -1);
      boost = 1;
    } else if (boost < MAX_BOOST) {
      boost *= 1.4;
    } else {
      return stackTwo(items, weight, rect, minSize);
    }
  }
};

const MAX_BOOST = 64;

/**
 * Last resort when the biggest item and "+N more" can't sit side by side:
 * stack them along the longer side, or roll everything into "+N more".
 */
const stackTwo = <T>(
  items: T[],
  weight: (item: T) => number,
  rect: Rect,
  minSize: { w: number; h: number },
): { placed: { item: T; rect: Rect }[]; rest: T[]; restRect?: Rect } => {
  const total = items.reduce((sum, item) => sum + weight(item), 0);
  const restShare = total > 0 ? 1 - weight(items[0]) / total : 0.5;
  const rest = items.slice(1);
  if (rect.h >= 2 * minSize.h) {
    const h = Math.min(Math.max(rect.h * restShare, minSize.h), rect.h - minSize.h);
    return {
      placed: [{ item: items[0], rect: { ...rect, h: rect.h - h } }],
      rest,
      restRect: { ...rect, y: rect.y + rect.h - h, h },
    };
  }
  if (rect.w >= 2 * minSize.w) {
    const w = Math.min(Math.max(rect.w * restShare, minSize.w), rect.w - minSize.w);
    return {
      placed: [{ item: items[0], rect: { ...rect, w: rect.w - w } }],
      rest,
      restRect: { ...rect, x: rect.x + rect.w - w, w },
    };
  }
  return { placed: [], rest: items, restRect: rect };
};

export const layoutHealthMap = (
  groups: HealthGroup[],
  width: number,
  height: number,
  layout: HealthMapLayout,
): MapBlock[] => {
  const sorted = byTotal(groups.filter((g) => g.total > 0));
  const top = fitReadable(
    sorted,
    (g) => g.total,
    { x: 0, y: 0, w: width, h: height },
    layout.group,
  );
  const blocks: MapBlock[] = top.placed.map(({ item: group, rect }) => {
    const inner = { x: rect.x, y: rect.y + layout.header, w: rect.w, h: rect.h - layout.header };
    const fitted = fitReadable(byTotal(group.tiles), (t) => t.total, inner, layout.tile);
    const tiles: MapTile[] = fitted.placed.map(({ item: tile, rect: r }) => ({ tile, rect: r }));
    if (fitted.restRect && fitted.rest.length === 1) {
      // One tile needs no "+1 more": it shows itself in the room kept for it.
      tiles.push({ tile: fitted.rest[0], rect: fitted.restRect });
    } else if (fitted.restRect) {
      tiles.push({
        tile: sumTiles(`+${fitted.rest.length} more`, fitted.rest),
        rect: fitted.restRect,
        merged: fitted.rest.map((t) => t.name),
      });
    }
    return { group, rect, tiles };
  });
  if (top.restRect) {
    const tiles = top.rest.flatMap((g) => g.tiles);
    const tile = sumTiles(`+${top.rest.length} more`, tiles);
    blocks.push({
      group: { name: tile.name, total: tile.total, tiles },
      rect: top.restRect,
      merged: top.rest.map((g) => g.name),
      tiles: [{ tile, rect: top.restRect }],
    });
  }
  return blocks;
};

const byWeight = <T extends { weight: number }>(items: T[]) =>
  [...items].sort((a, b) => b.weight - a.weight);
