import type { Site, SiteEnvironment } from '../../entity/sites';

export const ENVIRONMENT_OPTIONS: { value: SiteEnvironment; label: string }[] = [
  { value: '', label: 'Not set' },
  { value: 'production', label: 'Production' },
  { value: 'staging', label: 'Staging' },
  { value: 'development', label: 'Development' },
];

/** Tailwind classes per environment; '' stays neutral. */
export const ENVIRONMENT_STYLE: Record<
  Exclude<SiteEnvironment, ''>,
  { tab: string; strip: string; badge: string; label: string }
> = {
  production: {
    tab: 'border-red-300 bg-red-50 text-red-800 dark:border-red-800 dark:bg-red-950 dark:text-red-200',
    strip: 'bg-red-500',
    badge: 'bg-red-50 text-red-800 dark:bg-red-950 dark:text-red-200',
    label: 'Production',
  },
  staging: {
    tab: 'border-amber-300 bg-amber-50 text-amber-800 dark:border-amber-800 dark:bg-amber-950 dark:text-amber-200',
    strip: 'bg-amber-400',
    badge: 'bg-amber-50 text-amber-800 dark:bg-amber-950 dark:text-amber-200',
    label: 'Staging',
  },
  development: {
    tab: 'border-sky-300 bg-sky-50 text-sky-800 dark:border-sky-800 dark:bg-sky-950 dark:text-sky-200',
    strip: 'bg-sky-500',
    badge: 'bg-sky-50 text-sky-800 dark:bg-sky-950 dark:text-sky-200',
    label: 'Development',
  },
};

const STRICTNESS: SiteEnvironment[] = ['production', 'staging', 'development', ''];

/** The strictest of a group's environments, so it never looks safer than its most sensitive Site. */
export function strictestEnvironment(sites: Site[]): SiteEnvironment {
  return STRICTNESS.find((env) => sites.some((site) => site.environment === env)) ?? '';
}

/** What the picker calls a Site ID: its friendly name when one is set. */
export function siteLabel(site: Pick<Site, 'id' | 'displayName'>): string {
  return site.displayName?.trim() || site.id;
}
