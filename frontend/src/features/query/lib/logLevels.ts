import type { QueryNode } from '../../../entity/query';

/**
 * Severity order and colors shared by the facet, histogram and results table,
 * most severe first. Unrecognized raw levels are stored as-is, so callers
 * append any others they see, colored OTHER_LEVEL_COLOR.
 */
// Levels are stored lowercase (older data is read lowercase too).
export const KNOWN_LOG_LEVELS = [
  'fatal',
  'critical',
  'error',
  'warn',
  'info',
  'debug',
  'trace',
] as const;

const LEVEL_COLORS: Record<string, string> = {
  fatal: '#991b1b', // red-800
  critical: '#dc2626', // red-600
  error: '#ef4444', // red-500
  warn: '#f59e0b', // amber-500
  info: '#3b82f6', // blue-500
  debug: '#9ca3af', // gray-400
  trace: '#a855f7', // purple-500
};

export const OTHER_LEVEL_COLOR = '#6b7280'; // gray-500

export function levelColor(level: string): string {
  return LEVEL_COLORS[level.toLowerCase()] ?? OTHER_LEVEL_COLOR;
}

/** Known levels first (most severe first), then any others alphabetically. */
export function sortLevels(levels: Iterable<string>): string[] {
  const known = KNOWN_LOG_LEVELS as readonly string[];
  return [...new Set(levels)].sort((a, b) => {
    const ia = known.indexOf(a.toLowerCase());
    const ib = known.indexOf(b.toLowerCase());
    if (ia !== -1 || ib !== -1) {
      return (ia === -1 ? known.length : ia) - (ib === -1 ? known.length : ib);
    }
    return a.localeCompare(b);
  });
}

/**
 * ANDs "level not in <hidden>" onto a base query. Returns the same reference
 * when nothing is hidden, so memoized consumers don't refetch.
 */
export function withHiddenLevels(
  base: QueryNode | null,
  hiddenLevels: ReadonlySet<string>,
): QueryNode | null {
  if (hiddenLevels.size === 0) return base;

  const levelFilter: QueryNode = {
    type: 'condition',
    condition: { field: 'level', operator: 'not_in', value: [...hiddenLevels].sort() },
  };
  if (!base) return levelFilter;

  return { type: 'logical', logic: { operator: 'and', children: [base, levelFilter] } };
}

// Facet value colors: distinct hues readable in both themes, no red (reserved
// for errors). Assigned by row position since a per-value hash let neighbors
// collide.
const VALUE_PALETTE = [
  '#3b82f6', // blue-500
  '#f59e0b', // amber-500
  '#10b981', // emerald-500
  '#8b5cf6', // violet-500
  '#ec4899', // pink-500
  '#06b6d4', // cyan-500
  '#f97316', // orange-500
  '#84cc16', // lime-500
];

export function valueColor(index: number): string {
  return VALUE_PALETTE[index % VALUE_PALETTE.length];
}
