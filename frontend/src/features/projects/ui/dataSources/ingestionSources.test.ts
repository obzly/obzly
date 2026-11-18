import { describe, expect, it } from 'vitest';

import type { IngestionHealth, IngestionSource } from '../../../../entity/projects';
import { filterSources, healthCounts, needingAttention } from './ingestionSources';

const source = (
  name: string,
  health: IngestionHealth,
  lastSeen: string,
  perMinute = 0,
  signal: 'logs' | 'traces' | 'metrics' = 'logs',
): IngestionSource => ({
  id: name,
  name,
  identifiedBy: 'service',
  location: { cluster: 'contoso-prd' },
  health,
  firstSeen: '2026-11-01T00:00:00Z',
  lastSeen,
  signals: [
    { signal, health, firstSeen: '2026-11-01T00:00:00Z', lastSeen, recentCount: 1, perMinute },
  ],
});

const sources = [
  source('checkout', 'sending', '2026-11-18T10:00:00Z', 50),
  source('cart', 'sending', '2026-11-18T10:00:00Z', 900, 'traces'),
  source('legacy-batch', 'stopped', '2026-11-16T10:00:00Z'),
  source('billing', 'quiet', '2026-11-18T09:40:00Z'),
  source('old-cron', 'stopped', '2026-11-17T10:00:00Z'),
];

describe('ingestion sources', () => {
  it('counts sources per health', () => {
    expect(healthCounts(sources)).toEqual({ sending: 2, quiet: 1, stopped: 2 });
  });

  it('lists stopped first, then quiet, most recently lost first', () => {
    expect(needingAttention(sources).map((s) => s.name)).toEqual([
      'old-cron',
      'legacy-batch',
      'billing',
    ]);
  });

  it('filters by health, signal and text in the name or location', () => {
    const none = () => undefined;
    expect(filterSources(sources, 'stopped', 'all', '', none).map((s) => s.name)).toEqual([
      'legacy-batch',
      'old-cron',
    ]);
    expect(filterSources(sources, 'all', 'traces', '', none).map((s) => s.name)).toEqual(['cart']);
    expect(filterSources(sources, 'all', 'all', 'CONTOSO', none)).toHaveLength(5);
    expect(filterSources(sources, 'sending', 'all', 'check', none).map((s) => s.name)).toEqual([
      'checkout',
    ]);
  });
});
