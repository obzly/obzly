import type {
  IngestionHealth,
  IngestionSignal,
  IngestionSource,
} from '../../../../entity/projects';
import { locationText } from './ingestionStatusFormat';

export type HealthFilter = 'all' | IngestionHealth;

export type HealthCounts = Record<IngestionHealth, number>;

// Worst first: what needs attention leads every list.
const HEALTH_RANK: Record<IngestionHealth, number> = { stopped: 0, quiet: 1, sending: 2 };

export const healthCounts = (sources: IngestionSource[]): HealthCounts => {
  const counts: HealthCounts = { sending: 0, quiet: 0, stopped: 0 };
  for (const source of sources) counts[source.health]++;
  return counts;
};

/** Records per minute over every signal of a source. */
export const totalRate = (source: IngestionSource): number =>
  source.signals.reduce((sum, signal) => sum + signal.perMinute, 0);

/** Stopped, then quiet, each by most recent data; then sending, busiest first. */
export const byAttention = (a: IngestionSource, b: IngestionSource): number => {
  const rank = HEALTH_RANK[a.health] - HEALTH_RANK[b.health];
  if (rank !== 0) return rank;
  if (a.health === 'sending') return totalRate(b) - totalRate(a);
  return new Date(b.lastSeen).getTime() - new Date(a.lastSeen).getTime();
};

/** Sources that aren't sending, most recently lost first. */
export const needingAttention = (sources: IngestionSource[]): IngestionSource[] =>
  sources.filter((source) => source.health !== 'sending').sort(byAttention);

export const filterSources = (
  sources: IngestionSource[],
  health: HealthFilter,
  signal: IngestionSignal | 'all',
  search: string,
  typeName: (source: IngestionSource) => string | undefined,
): IngestionSource[] => {
  const needle = search.trim().toLowerCase();
  return sources.filter((source) => {
    if (health !== 'all' && source.health !== health) return false;
    if (signal !== 'all' && !source.signals.some((s) => s.signal === signal)) return false;
    if (!needle) return true;
    return [source.name, typeName(source), locationText(source), ...Object.values(source.location)]
      .filter((text): text is string => !!text)
      .some((text) => text.toLowerCase().includes(needle));
  });
};
