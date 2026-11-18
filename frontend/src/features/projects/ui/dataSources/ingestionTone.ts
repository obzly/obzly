import type { IngestionHealth } from '../../../../entity/projects';
import type { PillTone } from '../ProjectPageShell';

// Good is green, bad is red: stopped is the bad state, quiet a warning.
export const HEALTH_TONE: Record<IngestionHealth, PillTone> = {
  sending: 'ok',
  quiet: 'warn',
  stopped: 'bad',
};

export const HEALTH_COLOR: Record<IngestionHealth, string> = {
  sending: 'var(--v2-green)',
  quiet: 'var(--v2-yellow)',
  stopped: 'var(--v2-red)',
};
