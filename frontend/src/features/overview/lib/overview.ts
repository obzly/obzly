// Pure helpers behind the project Overview. Every figure is computed from what an API returned;
// anything it didn't return stays undefined, so the page shows "n/a", never 0.
import type { Session } from '../../../entity/correlation';
import type { AggregationBucket } from '../../../entity/dashboards';
import type { AssetHealth, AssetKind, InventorySummary } from '../../../entity/inventory';
import type { IngestionStatus } from '../../../entity/projects';
import type { ServiceErrorsResponse, TraceSummaryResponse } from '../../../entity/traces';
import type { ChangeTone } from '../../../shared/ui/v2';

export type OverviewRange = '1h' | '24h' | '7d' | '30d';

export const OVERVIEW_RANGES: OverviewRange[] = ['1h', '24h', '7d', '30d'];

export const RANGE_TEXT: Record<OverviewRange, string> = {
  '1h': 'last hour',
  '24h': 'last 24 hours',
  '7d': 'last 7 days',
  '30d': 'last 30 days',
};

const MINUTE = 60_000;
const HOUR = 60 * MINUTE;
const DAY = 24 * HOUR;

const RANGE_MS: Record<OverviewRange, number> = {
  '1h': HOUR,
  '24h': DAY,
  '7d': 7 * DAY,
  '30d': 30 * DAY,
};

// Whole minutes, so the logs histogram reads the per-minute rollups.
const BUCKET_MS: Record<OverviewRange, number> = {
  '1h': 5 * MINUTE,
  '24h': HOUR,
  '7d': 6 * HOUR,
  '30d': DAY,
};

export interface OverviewWindow {
  from: number;
  to: number;
  /** The same-length period before from, for change pills. */
  previousFrom: number;
  bucketMs: number;
}

/** The range ending now, rounded to the minute so refreshes within one reuse the same window. */
export const overviewWindow = (range: OverviewRange, now = Date.now()): OverviewWindow => {
  const to = Math.ceil(now / MINUTE) * MINUTE;
  const span = RANGE_MS[range];
  return { from: to - span, to, previousFrom: to - 2 * span, bucketMs: BUCKET_MS[range] };
};

export interface Change {
  text: string;
  tone: ChangeTone;
}

/** "+6.1%" against before, green when the move is good; undefined with nothing to compare with. */
export const percentChange = (
  now: number | undefined,
  before: number | undefined,
  higherIsGood: boolean,
): Change | undefined => {
  if (now === undefined || before === undefined || before === 0) return undefined;
  const change = ((now - before) / Math.abs(before)) * 100;
  if (Math.abs(change) < 0.05) return { text: '0.0%', tone: 'neutral' };
  return {
    text: `${change > 0 ? '+' : '−'}${Math.abs(change).toFixed(1)}%`,
    tone: change > 0 === higherIsGood ? 'good' : 'bad',
  };
};

/** "+0.4 pt" between two percentages; a rise is bad (error rates). */
export const pointChange = (now?: number, before?: number): Change | undefined => {
  if (now === undefined || before === undefined) return undefined;
  const change = now - before;
  if (Math.abs(change) < 0.05) return { text: '0.0 pt', tone: 'neutral' };
  return {
    text: `${change > 0 ? '+' : '−'}${Math.abs(change).toFixed(1)} pt`,
    tone: change > 0 ? 'bad' : 'good',
  };
};

export interface LogVolume {
  /** Lines per second over the range. */
  rate: number;
  previousRate?: number;
  total: number;
  /** Lines per bucket over the range, oldest first. */
  trend: number[];
}

/**
 * Log lines per second from a histogram over [previousFrom, to]. Buckets start at their timestamp
 * on multiples of bucketMs, so the bucket straddling `from` belongs to neither period: each rate
 * is over the whole buckets it covers. The previous rate is undefined when the previous period
 * had no lines at all, since the project may simply not have existed then.
 */
export const logVolume = (buckets: AggregationBucket[], window: OverviewWindow): LogVolume => {
  const size = window.bucketMs;
  const from = Math.ceil(window.from / size) * size;
  const previousFrom = Math.ceil(window.previousFrom / size) * size;
  const slots = Math.max(1, Math.ceil((window.to - from) / size));
  const trend = new Array<number>(slots).fill(0);
  let total = 0;
  let previous = 0;
  for (const bucket of buckets) {
    if (!bucket.timestamp) continue;
    const at = Date.parse(bucket.timestamp);
    if (Number.isNaN(at)) continue;
    if (at >= from) {
      total += bucket.value;
      trend[Math.min(slots - 1, Math.floor((at - from) / size))] += bucket.value;
    } else if (at >= previousFrom) {
      previous += bucket.value;
    }
  }
  return {
    rate: total / (Math.max(window.to - from, size) / 1000),
    previousRate:
      previous > 0 ? previous / (Math.max(from - previousFrom, size) / 1000) : undefined,
    total,
    trend,
  };
};

/** A rate as a value and unit: per second when there's at least one a second, else per minute. */
export const formatRate = (perSecond: number): { value: string; unit: string } =>
  perSecond >= 1
    ? { value: compactNumber(perSecond), unit: '/s' }
    : { value: compactNumber(perSecond * 60), unit: '/min' };

export const compactNumber = (value: number): string => {
  if (value >= 1_000_000) return `${(value / 1_000_000).toFixed(value >= 10_000_000 ? 0 : 1)} M`;
  if (value >= 10_000) return `${(value / 1000).toFixed(value >= 100_000 ? 0 : 1)} k`;
  if (value >= 100) return Math.round(value).toLocaleString('en-US');
  if (value >= 10) return value.toFixed(0);
  return value.toFixed(value === 0 ? 0 : 1);
};

/** Error share in percent; undefined without requests. */
export const errorRate = (errors?: number | null, count?: number | null): number | undefined =>
  count && errors !== null && errors !== undefined ? (errors / count) * 100 : undefined;

export const formatMs = (ms?: number | null): { value: string; unit: string } | undefined => {
  if (ms === undefined || ms === null) return undefined;
  if (ms >= 10_000) return { value: (ms / 1000).toFixed(0), unit: 's' };
  if (ms >= 1000) return { value: (ms / 1000).toFixed(1), unit: 's' };
  return { value: ms >= 10 ? ms.toFixed(0) : ms.toFixed(1), unit: 'ms' };
};

export interface RequestBar {
  at: number;
  ok: number;
  errors: number;
  /** Incident sessions that opened in this bucket. */
  incidents: number;
}

/**
 * Requests and errors per trend bucket. countTrend has every bucket; errorRateTrend only buckets
 * with requests, in order, so each rate maps back onto the next non-empty bucket.
 */
export const requestBars = (
  summary: TraceSummaryResponse,
  from: number,
  sessions: Session[] = [],
): RequestBar[] => {
  const step = summary.stepMs;
  if (!step) return [];
  const first = Math.floor(from / step);
  const rates = [...summary.errorRateTrend];
  const bars = summary.countTrend.map((count, index) => {
    const rate = count > 0 ? (rates.shift() ?? 0) : 0;
    const errors = Math.round((rate * count) / 100);
    return { at: (first + index) * step, ok: count - errors, errors, incidents: 0 };
  });
  for (const session of sessions) {
    const slot = Math.floor(Date.parse(session.windowStart) / step) - first;
    if (slot >= 0 && slot < bars.length) bars[slot].incidents += 1;
  }
  return bars;
};

/** The busiest bucket, as requests per minute. */
export const peakBar = (
  bars: RequestBar[],
  stepMs: number,
): { at: number; perMinute: number } | undefined => {
  let best: RequestBar | undefined;
  for (const bar of bars) {
    if (bar.ok + bar.errors > 0 && (!best || bar.ok + bar.errors > best.ok + best.errors)) {
      best = bar;
    }
  }
  return best && { at: best.at, perMinute: ((best.ok + best.errors) * MINUTE) / stepMs };
};

export interface ServiceErrorRow {
  name: string;
  requests: number;
  errors: number;
  /** Percent. */
  rate: number;
  change?: Change;
}

/** Services from the per-service errors call, with their error rate's change in points. */
export const serviceErrorRows = (response: ServiceErrorsResponse, top = 6): ServiceErrorRow[] =>
  response.services
    .filter((service) => service.requests > 0)
    .slice(0, top)
    .map((service) => {
      const rate = (service.errors / service.requests) * 100;
      const previous =
        service.previousRequests > 0
          ? (service.previousErrors / service.previousRequests) * 100
          : undefined;
      return {
        name: service.serviceName,
        requests: service.requests,
        errors: service.errors,
        rate,
        change: pointChange(rate, previous),
      };
    });

export type IncidentGroup = 'ongoing' | 'recent' | 'earlier';

/** A session still collecting signals: its last one is this recent. */
export const ONGOING_MS = 15 * MINUTE;

export interface IncidentSummary {
  total: number;
  counts: Record<IncidentGroup, number>;
  /** Median minutes from first to last signal; undefined without sessions. */
  medianMinutes?: number;
  /** The service with the most sessions. */
  topService?: string;
}

/**
 * Incident sessions have no acknowledge/resolve state, so they're grouped by how recently their
 * last signal arrived: ongoing (still collecting), the last 24 hours, or earlier.
 */
export const summarizeIncidents = (sessions: Session[], now = Date.now()): IncidentSummary => {
  const counts: Record<IncidentGroup, number> = { ongoing: 0, recent: 0, earlier: 0 };
  const durations: number[] = [];
  const services = new Map<string, number>();
  for (const session of sessions) {
    const end = Date.parse(session.windowEnd);
    const age = now - end;
    counts[age <= ONGOING_MS ? 'ongoing' : age <= DAY ? 'recent' : 'earlier'] += 1;
    durations.push((end - Date.parse(session.windowStart)) / MINUTE);
    if (session.serviceName) {
      services.set(session.serviceName, (services.get(session.serviceName) ?? 0) + 1);
    }
  }
  durations.sort((a, b) => a - b);
  const middle = Math.floor(durations.length / 2);
  const medianMinutes = durations.length
    ? durations.length % 2
      ? durations[middle]
      : (durations[middle - 1] + durations[middle]) / 2
    : undefined;
  const topService = [...services.entries()].sort(
    (a, b) => b[1] - a[1] || a[0].localeCompare(b[0]),
  )[0]?.[0];
  return { total: sessions.length, counts, medianMinutes, topService };
};

/** Sessions per bucket over the window, for the incidents tile's trend. */
export const sessionsPerBucket = (sessions: Session[], window: OverviewWindow): number[] => {
  const slots = Math.max(1, Math.round((window.to - window.from) / window.bucketMs));
  const counts = new Array<number>(slots).fill(0);
  for (const session of sessions) {
    const slot = Math.floor((Date.parse(session.windowStart) - window.from) / window.bucketMs);
    if (slot >= 0 && slot < slots) counts[slot] += 1;
  }
  return counts;
};

export const formatMinutes = (minutes?: number): string => {
  if (minutes === undefined) return 'n/a';
  if (minutes < 1) return 'under a minute';
  if (minutes < 90) return `${Math.round(minutes)} min`;
  return `${(minutes / 60).toFixed(1)} h`;
};

export type IngestionTone = 'ok' | 'warn' | 'bad' | 'none';

/** The header pill: are this project's sources still sending? */
export const ingestionPill = (status: IngestionStatus): { text: string; tone: IngestionTone } => {
  const total = status.sources.length;
  if (total === 0) return { text: 'No data received yet', tone: 'none' };
  const sending = status.sources.filter((source) => source.health === 'sending').length;
  const stopped = status.sources.filter((source) => source.health === 'stopped').length;
  const more = status.truncated ? '+' : '';
  if (sending === total) {
    return {
      text: total === 1 ? 'Source sending' : `All ${total}${more} sources sending`,
      tone: 'ok',
    };
  }
  if (sending === 0) {
    return {
      text: stopped === total ? 'All sources stopped' : 'No source sending now',
      tone: 'bad',
    };
  }
  return { text: `${sending} of ${total}${more} sources sending`, tone: 'warn' };
};

export interface InfraRow {
  kind: AssetKind;
  total: number;
  healthy: number;
  warning: number;
  critical: number;
  /** Inactive or unknown. */
  other: number;
}

// The kinds the panel shows first, in hierarchy order; the rest follow by size.
const INFRA_ORDER: AssetKind[] = [
  'cluster',
  'node',
  'host',
  'workload',
  'pod',
  'docker_container',
  'load_balancer',
  'database',
  'function',
  'bucket',
  'scaling_group',
  'service',
  'namespace',
  'container',
];

export const infraRows = (summary: InventorySummary, top = 5): InfraRow[] =>
  INFRA_ORDER.flatMap((kind) => {
    const byHealth: Partial<Record<AssetHealth, number>> = summary.kinds[kind] ?? {};
    const healthy = byHealth.healthy ?? 0;
    const warning = byHealth.warning ?? 0;
    const critical = byHealth.critical ?? 0;
    const other = (byHealth.inactive ?? 0) + (byHealth.unknown ?? 0);
    const total = healthy + warning + critical + other;
    return total > 0 ? [{ kind, total, healthy, warning, critical, other }] : [];
  }).slice(0, top);
