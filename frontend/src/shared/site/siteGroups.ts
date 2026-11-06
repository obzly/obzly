import type { Site } from '../../entity/sites';

/** Sites sharing a name (trimmed, case-insensitive) show as one entry in the picker. */
export interface SiteGroup {
  key: string;
  name: string;
  sites: Site[];
}

export function siteGroupKey(site: Site): string {
  return site.name.trim().toLowerCase();
}

export function groupSites(sites: Site[]): SiteGroup[] {
  const groups = new Map<string, SiteGroup>();
  for (const site of sites) {
    const key = siteGroupKey(site);
    const group = groups.get(key);
    if (group) group.sites.push(site);
    else groups.set(key, { key, name: site.name.trim(), sites: [site] });
  }
  return [...groups.values()];
}
