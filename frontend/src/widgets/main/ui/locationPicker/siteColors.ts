import type { Site } from '../../../../entity/sites';
import { siteGroupKey } from '../../../../shared/site';

// Identity colours only: no green, amber, red or grey, which mean health.
const PALETTE = ['#378ADD', '#7F77DD', '#D4537E', '#0EA5B7', '#B14FC5', '#5B6BD8'];

/**
 * Derived from the ID so it's stable for every user; IDs sharing a Site name
 * get distinct colours while the palette lasts.
 */
export function assignSiteColors(sites: Site[]): Record<string, string> {
  const colors: Record<string, string> = {};
  const usedByGroup = new Map<string, Set<number>>();

  for (const site of [...sites].sort((a, b) => a.id.localeCompare(b.id))) {
    const group = siteGroupKey(site);
    const used = usedByGroup.get(group) ?? new Set<number>();
    let index = hash(site.id) % PALETTE.length;
    for (let tries = 0; tries < PALETTE.length && used.has(index); tries++) {
      index = (index + 1) % PALETTE.length;
    }
    used.add(index);
    usedByGroup.set(group, used);
    colors[site.id] = PALETTE[index];
  }
  return colors;
}

function hash(value: string): number {
  let h = 2166136261;
  for (let i = 0; i < value.length; i++) {
    h ^= value.charCodeAt(i);
    h = Math.imul(h, 16777619);
  }
  return h >>> 0;
}
