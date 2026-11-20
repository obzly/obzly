// Pointy-top hexagons: width √3·r, rows 1.5·r apart, odd rows shifted half a hexagon.
const SQRT3 = Math.sqrt(3);
export const HEX_GAP = 2;
// A group's name row, and the room between groups (Tailwind gap-x-5, gap-y-3).
const LABEL_HEIGHT = 20;
const LABEL_MIN_WIDTH = 110;
export const GROUP_GAP_X = 20;
export const GROUP_GAP_Y = 12;
// Largest first; the smallest is still easy to click.
const RADII = [44, 38, 32, 28, 24, 21, 18, 16, 14, 12, 10];

export interface GroupBox {
  columns: number;
  /** The hexagons' own size. */
  svgWidth: number;
  svgHeight: number;
  /** With the name row. */
  width: number;
  height: number;
}

/** Columns for a group's hexagons: roughly square, wider for big groups. */
export const honeycombColumns = (tiles: number): number =>
  Math.max(1, Math.min(tiles, Math.ceil(Math.sqrt(tiles * 1.6))));

export const groupBox = (tiles: number, r: number): GroupBox => {
  const columns = honeycombColumns(tiles);
  const rows = Math.ceil(tiles / columns);
  const w = SQRT3 * r;
  const svgWidth = columns * w + (rows > 1 ? w / 2 : 0) + HEX_GAP;
  const svgHeight = (rows - 1) * 1.5 * r + 2 * r + HEX_GAP;
  return {
    columns,
    svgWidth,
    svgHeight,
    width: Math.max(svgWidth, LABEL_MIN_WIDTH),
    height: svgHeight + LABEL_HEIGHT,
  };
};

/** Height of the boxes wrapped into rows of this width, as flex-wrap lays them out. */
export const flowHeight = (boxes: { width: number; height: number }[], width: number): number => {
  let x = 0;
  let rowHeight = 0;
  let total = 0;
  for (const box of boxes) {
    if (x > 0 && x + box.width > width) {
      total += rowHeight + GROUP_GAP_Y;
      x = 0;
      rowHeight = 0;
    }
    x += box.width + GROUP_GAP_X;
    rowHeight = Math.max(rowHeight, box.height);
  }
  return total + rowHeight;
};

/** The biggest hexagon radius at which groups of these sizes fit in width × height. */
export const fitRadius = (groupTiles: number[], width: number, height: number): number =>
  RADII.find(
    (r) =>
      flowHeight(
        groupTiles.map((tiles) => groupBox(tiles, r)),
        width,
      ) <= height,
  ) ?? RADII[RADII.length - 1];

// The smallest hexagon drawn, as a share of its slot's radius (9% of the area).
const MIN_SCALE = 0.3;

/**
 * How big to draw a workload's hexagon in its slot: its area is its share of
 * the busiest workload's lines (in its namespace), so sizes keep the ratio of log volume, down
 * to MIN_SCALE so a quiet workload can still be seen and clicked.
 */
export const hexScale = (lines: number, mostLines: number): number =>
  mostLines > 0 ? Math.min(1, Math.max(MIN_SCALE, Math.sqrt(lines / mostLines))) : 1;
