import type { CheckView } from '../../../entity/synthetics';

export interface MapNode {
  check: CheckView;
  /** 0 for monitors without parents, else one below its deepest parent. */
  layer: number;
  x: number;
  y: number;
}

export interface MapLayout {
  nodes: MapNode[];
  links: { source: string; target: string }[];
  width: number;
  height: number;
}

const COLUMN = 220;
const ROW = 130;

/** Monitors with a parent or a child, parents above their children; the rest are left out. */
export const dependencyLayout = (checks: CheckView[]): MapLayout => {
  const byId = new Map(checks.map((check) => [check.id, check]));
  const parentsOf = (check: CheckView) =>
    (check.parentIds ?? []).filter((id) => byId.has(id) && id !== check.id);
  const involved = new Set<string>();
  for (const check of checks) {
    const parents = parentsOf(check);
    if (parents.length > 0) involved.add(check.id);
    parents.forEach((id) => involved.add(id));
  }

  const layers = new Map<string, number>();
  const layerOf = (id: string, visiting: Set<string>): number => {
    const known = layers.get(id);
    if (known !== undefined) return known;
    // The API refuses loops; this only guards against a stale list.
    if (visiting.has(id)) return 0;
    visiting.add(id);
    const parents = parentsOf(byId.get(id)!);
    const layer =
      parents.length === 0 ? 0 : Math.max(...parents.map((p) => layerOf(p, visiting))) + 1;
    visiting.delete(id);
    layers.set(id, layer);
    return layer;
  };
  const rows = new Map<number, CheckView[]>();
  for (const id of involved) {
    const layer = layerOf(id, new Set());
    rows.set(layer, [...(rows.get(layer) ?? []), byId.get(id)!]);
  }

  const widest = Math.max(1, ...[...rows.values()].map((row) => row.length));
  const nodes: MapNode[] = [];
  for (const [layer, row] of [...rows].sort(([a], [b]) => a - b)) {
    row.sort(
      (a, b) => (a.folder ?? '').localeCompare(b.folder ?? '') || a.name.localeCompare(b.name),
    );
    const offset = ((widest - row.length) * COLUMN) / 2;
    row.forEach((check, index) =>
      nodes.push({ check, layer, x: offset + index * COLUMN, y: layer * ROW }),
    );
  }
  const links = nodes.flatMap((node) =>
    parentsOf(node.check).map((parent) => ({ source: parent, target: node.check.id })),
  );
  return { nodes, links, width: widest * COLUMN, height: (rows.size || 1) * ROW };
};
