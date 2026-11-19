/** Pointy-top hexagon cells laid out in offset rows, one per monitor. */
export interface HexCell {
  cx: number;
  cy: number;
}

export interface HexLayout {
  cells: HexCell[];
  height: number;
  columns: number;
}

/** Bigger cells for few monitors, smaller ones so hundreds stay compact. */
export const hexRadius = (count: number, compact = false): number => {
  if (compact) return count > 200 ? 4.5 : count > 60 ? 6 : 8;
  return count > 200 ? 11 : count > 60 ? 15 : 20;
};

/** A hexagon's SVG path centred on (cx, cy). */
export const hexPath = (cx: number, cy: number, r: number): string => {
  const points: string[] = [];
  for (let i = 0; i < 6; i++) {
    const angle = (Math.PI / 180) * (60 * i - 30);
    points.push(
      `${(cx + r * Math.cos(angle)).toFixed(2)},${(cy + r * Math.sin(angle)).toFixed(2)}`,
    );
  }
  return `M${points.join('L')}Z`;
};

/** The left half of a hexagon: an impacted monitor's half-filled cell. */
export const halfHexPath = (cx: number, cy: number, r: number): string => {
  const point = (degrees: number) => {
    const angle = (Math.PI / 180) * degrees;
    return `${(cx + r * Math.cos(angle)).toFixed(2)},${(cy + r * Math.sin(angle)).toFixed(2)}`;
  };
  return `M${[270, 210, 150, 90].map(point).join('L')}Z`;
};

/** count cells filling width, row by row, every other row shifted half a cell. */
export const layoutHexes = (count: number, width: number, r: number, gap = 2): HexLayout => {
  const cellWidth = Math.sqrt(3) * r;
  const rowHeight = 1.5 * r;
  const columns = Math.max(1, Math.floor((width - cellWidth / 2 - gap) / (cellWidth + gap)));
  const cells: HexCell[] = [];
  for (let i = 0; i < count; i++) {
    const row = Math.floor(i / columns);
    const column = i % columns;
    cells.push({
      cx: cellWidth / 2 + 1 + column * (cellWidth + gap) + (row % 2 ? (cellWidth + gap) / 2 : 0),
      cy: r + 1 + row * (rowHeight + gap),
    });
  }
  const rows = Math.ceil(count / columns);
  return { cells, columns, height: rows ? r * 2 + 2 + (rows - 1) * (rowHeight + gap) : 0 };
};

export interface LatencyBand {
  /** Upper bound in ms, exclusive. */
  below: number;
  label: string;
}

export const LATENCY_BANDS: LatencyBand[] = [
  { below: 300, label: '< 300 ms' },
  { below: 800, label: '300–800 ms' },
  { below: 1500, label: '800 ms–1.5 s' },
  { below: Infinity, label: '> 1.5 s' },
];

/** The band index for a p95, or -1 with no timings. */
export const latencyBand = (p95Ms: number | undefined): number =>
  p95Ms === undefined ? -1 : LATENCY_BANDS.findIndex((band) => p95Ms < band.below);

/** The top-level folder ("Payments/EU" -> "Payments"), or "" at the top level. */
export const topFolder = (folder: string): string => folder.split('/')[0]?.trim() ?? '';

/** Groups items by top-level folder, keeping their order; the top level comes last. */
export const groupByFolder = <T extends { folder: string }>(
  items: T[],
): { folder: string; items: T[] }[] => {
  const groups = new Map<string, T[]>();
  for (const item of items) {
    const key = topFolder(item.folder);
    groups.set(key, [...(groups.get(key) ?? []), item]);
  }
  return [...groups.entries()]
    .sort(([a], [b]) => (a === '' ? 1 : b === '' ? -1 : a.localeCompare(b)))
    .map(([folder, grouped]) => ({ folder, items: grouped }));
};
