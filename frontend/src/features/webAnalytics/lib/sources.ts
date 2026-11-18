import type { WebAnalyticsSource } from '../../../entity/webAnalytics';

/** The gaps that make a source leave some panels out. */
export const sourceGaps = (source: WebAnalyticsSource): string[] =>
  [
    source.hasReferrer && source.hasUserAgent ? '' : 'no referrer/browser',
    source.hasDuration ? '' : 'no response time',
    source.hasTls ? '' : 'no TLS',
  ].filter(Boolean);

export const sourceLabel = (sources: WebAnalyticsSource[], selected: string[]): string => {
  if (sources.length === 0) return 'No sources yet';
  const count = selected.length === 0 ? sources.length : selected.length;
  if (count === sources.length)
    return `All ${sources.length} source${sources.length === 1 ? '' : 's'}`;
  if (count === 1) return sources.find((s) => s.key === selected[0])?.name ?? '1 source';
  return `${count} of ${sources.length} sources`;
};
