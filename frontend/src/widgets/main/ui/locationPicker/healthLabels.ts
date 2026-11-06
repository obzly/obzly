import type { SiteHealth } from '../../../../entity/sites';

export const HEALTH_LABEL: Record<SiteHealth, string> = {
  up: 'Up',
  degraded: 'Degraded',
  down: 'Down',
  disabled: 'Disabled',
};
