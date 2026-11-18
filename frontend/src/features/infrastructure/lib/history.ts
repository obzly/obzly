// Kind counts over time from each Site's inventory history, for the Overview's sparklines and
// change pills.
import type { AssetKind, InventoryHistory } from '../../../entity/inventory';

export interface KindHistory {
  /** Each kind's assets per hour over the last 7 days, oldest first, every Site added up. */
  trend: Partial<Record<AssetKind, number[]>>;
  /** Assets by kind about 24 h ago; undefined when a Site has nothing that old to compare with. */
  previous?: Partial<Record<AssetKind, number>>;
}

/**
 * Adds the Sites' histories up. Only hours every Site has are kept, so a Site that joined later
 * or missed a build doesn't show as a dip. A Site whose history is missing (an older Site, or
 * its summary failed) leaves the change pills out, since its assets are in the current counts.
 */
export const mergeHistories = (histories: (InventoryHistory | undefined)[]): KindHistory => {
  if (histories.length === 0 || histories.some((history) => !history)) return { trend: {} };
  const known = histories as InventoryHistory[];
  const shared = known
    .map((history) => new Set(history.hours))
    .reduce((common, hours) => new Set([...common].filter((hour) => hours.has(hour))));
  const hours = [...shared].sort();
  const trend: Partial<Record<AssetKind, number[]>> = {};
  for (const history of known) {
    const index = new Map(history.hours.map((hour, i) => [hour, i]));
    for (const [kind, values] of Object.entries(history.kinds) as [AssetKind, number[]][]) {
      const merged = (trend[kind] ??= hours.map(() => 0));
      hours.forEach((hour, i) => {
        merged[i] += values[index.get(hour) ?? -1] ?? 0;
      });
    }
  }
  let previous: Partial<Record<AssetKind, number>> | undefined;
  if (known.every((history) => history.previousAt)) {
    previous = {};
    for (const history of known) {
      for (const [kind, healths] of Object.entries(history.previous ?? {}) as [
        AssetKind,
        Record<string, number>,
      ][]) {
        previous[kind] =
          (previous[kind] ?? 0) + Object.values(healths).reduce((sum, count) => sum + count, 0);
      }
    }
  }
  return { trend, previous };
};
