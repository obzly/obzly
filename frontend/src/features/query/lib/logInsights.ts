import type { LogSampleRule } from '../../../entity/projects';
import type {
  ExplainField,
  HealthTile,
  NoisyPattern,
  PatternChange,
  PatternTrend,
  PatternTrendsResponse,
  QueryNode,
} from '../../../entity/query';

// Pure helpers for the Logs page's Patterns, Volume & noise and Health map
// tabs and the "Explain this spike" panel.

/** A change pill's kind: bad (red), warn (orange), info (blue) or neutral. */
export type InsightTone = 'bad' | 'warn' | 'info' | 'neutral';

export type PatternFilter = 'all' | 'new' | 'spiking' | 'dropped';

export type HealthColourBy = 'err' | 'warn' | 'chg';

export interface StatusPalette {
  ok: string;
  warn: string;
  bad: string;
  noData: string;
}

/** Keep choices offered for a sampling rule (percent of lines kept). */
export const KEEP_CHOICES = [0, 10, 25, 50] as const;

export const ERROR_LEVELS = ['fatal', 'critical', 'error'];
export const WARN_LEVELS = ['warn', 'warning'];

const DAYS_PER_MONTH = 30;
// Patterns within this of their earlier count read as steady.
const STEADY_BAND = 0.2;

/** 812, 18.2 k, 1.9 M */
export const formatCount = (value: number): string => {
  if (!Number.isFinite(value)) return 'n/a';
  const abs = Math.abs(value);
  if (abs >= 1e9) return `${(value / 1e9).toFixed(1)} B`;
  if (abs >= 1e6) return `${(value / 1e6).toFixed(1)} M`;
  if (abs >= 1e4) return `${Math.round(value / 1e3)} k`;
  if (abs >= 1e3) return `${(value / 1e3).toFixed(1)} k`;
  return Math.round(value).toLocaleString('en-US');
};

/** 1.4 GB, 600 MB, 12 KB; "n/a" when unknown. */
export const formatBytes = (bytes: number | null): string => {
  if (bytes === null || !Number.isFinite(bytes) || bytes < 0) return 'n/a';
  const units = ['B', 'KB', 'MB', 'GB', 'TB'];
  let value = bytes;
  let unit = 0;
  while (value >= 1024 && unit < units.length - 1) {
    value /= 1024;
    unit++;
  }
  return `${value >= 10 || unit === 0 ? Math.round(value) : value.toFixed(1)} ${units[unit]}`;
};

/** A pattern's change pill: new, ×ratio, −percent, or steady / ±percent. */
export const trendChangeBadge = (
  pattern: Pick<PatternTrend, 'change' | 'count' | 'yesterday' | 'level'>,
): { text: string; tone: InsightTone } => {
  const { change, count, yesterday } = pattern;
  if (change === 'new') return { text: 'new', tone: 'bad' };
  if (change === 'spike') {
    const ratio = yesterday > 0 ? count / yesterday : 0;
    return {
      text: `×${ratio.toFixed(ratio < 100 ? 1 : 0)}`,
      tone: ERROR_LEVELS.includes(pattern.level) ? 'bad' : 'warn',
    };
  }
  if (change === 'drop') {
    return { text: `−${Math.round((1 - count / yesterday) * 100)}%`, tone: 'info' };
  }
  if (yesterday === 0) return { text: count > 0 ? 'no baseline' : 'n/a', tone: 'neutral' };
  const delta = count / yesterday - 1;
  if (Math.abs(delta) <= STEADY_BAND) return { text: 'steady', tone: 'neutral' };
  return {
    text: `${delta > 0 ? '+' : '−'}${Math.round(Math.abs(delta) * 100)}%`,
    tone: 'neutral',
  };
};

export const matchesPatternFilter = (pattern: PatternTrend, filter: PatternFilter): boolean =>
  filter === 'all' ||
  (filter === 'new' && pattern.change === 'new') ||
  (filter === 'spiking' && pattern.change === 'spike') ||
  (filter === 'dropped' && pattern.change === 'drop');

/** Changed patterns first (new, spiking, dropping), then by lines. */
export const sortTrends = (patterns: PatternTrend[]): PatternTrend[] => {
  const rank = { new: 0, spike: 1, drop: 2, '': 3 } as const;
  return [...patterns].sort(
    (a, b) => rank[a.change] - rank[b.change] || b.count - a.count || a.hash.localeCompare(b.hash),
  );
};

/** When a pattern was first seen, as far as the 7-day lookback tells. */
export const firstSeenText = (
  pattern: PatternTrend,
  buckets: string[],
  formatTime: (iso: string) => string,
): string => {
  if (pattern.previousWeek > 0) return 'over 7 d ago';
  if (pattern.change === 'new') {
    const first = pattern.series.findIndex((count) => count > 0);
    return first >= 0 && buckets[first] ? formatTime(buckets[first]) : 'in range';
  }
  if (pattern.yesterday > 0) return 'in the last 7 d';
  return pattern.count > 0 ? 'in range' : 'n/a';
};

/**
 * The stacked "what makes up the volume" chart: the `top` biggest patterns
 * per bucket plus Other (the rest, listed or not).
 */
export const stackPatterns = (
  response: PatternTrendsResponse,
  top = 6,
): { label: string; hash: string | null; values: number[] }[] => {
  const biggest = [...response.patterns]
    .filter((p) => p.count > 0)
    .sort((a, b) => b.count - a.count)
    .slice(0, top);
  const shown = new Set(biggest.map((p) => p.hash));
  const other = response.other.map((count, i) =>
    response.patterns
      .filter((p) => !shown.has(p.hash))
      .reduce((sum, p) => sum + (p.series[i] ?? 0), count),
  );
  return [
    ...biggest.map((p) => ({ label: p.pattern || p.hash, hash: p.hash, values: p.series })),
    { label: 'Other', hash: null, values: other },
  ];
};

/** "new" when the value had no lines in the baseline, else ×(share ratio). */
export const explainLift = (field: ExplainField): string => {
  if (field.baseline === 0 || field.baselineShare === 0) return 'new';
  const ratio = field.currentShare / field.baselineShare;
  return `×${ratio.toFixed(ratio < 100 ? 1 : 0)}`;
};

/** A strong lift (new, or more than doubled) is called out. */
export const isStrongLift = (field: ExplainField): boolean =>
  field.baselineShare === 0 || field.currentShare / field.baselineShare > 2;

const percent = (share: number): string => `${Math.round(share * 100)}%`;

/** One sentence summing up the explanation from its biggest field and pattern changes. */
export const explainHeadline = (
  fields: ExplainField[],
  patterns: PatternChange[],
): { headline: string; detail: string } | null => {
  const top = fields[0];
  if (!top) return null;
  const headline = `Most of the extra lines carry ${top.field} = ${top.value}.`;
  const newOnes = patterns.filter((p) => p.change === 'new').length;
  const parts = [
    top.baseline === 0
      ? `${top.value} appears only in this range: ${percent(top.currentShare)} of its lines.`
      : `${percent(top.currentShare)} of the lines in the range against ${percent(top.baselineShare)} before.`,
  ];
  if (newOnes > 0) {
    parts.push(`${newOnes} new message pattern${newOnes === 1 ? '' : 's'} appear with it.`);
  }
  return { headline, detail: parts.join(' ') };
};

/** A tile's colour value: error share, warn + error share or change against yesterday (%). */
export const healthMetric = (tile: HealthTile, colourBy: HealthColourBy): number | null => {
  if (tile.total === 0) return null;
  if (colourBy === 'err') return (tile.errors / tile.total) * 100;
  if (colourBy === 'warn') return ((tile.errors + tile.warnings) / tile.total) * 100;
  if (tile.previous === 0) return null;
  return (tile.total / tile.previous - 1) * 100;
};

/** Where each scale reaches warn and bad. */
export const healthScale = (colourBy: HealthColourBy): { warnAt: number; badAt: number } =>
  colourBy === 'chg' ? { warnAt: 100, badAt: 300 } : { warnAt: 4, badAt: 10 };

/** A tile's colour from ok through warn to bad (status colours); noData when unknown. */
export const healthColor = (
  value: number | null,
  colourBy: HealthColourBy,
  status: StatusPalette,
): string => {
  if (value === null || !Number.isFinite(value)) return status.noData;
  const { warnAt, badAt } = healthScale(colourBy);
  const v = Math.abs(value);
  if (v <= warnAt) return mixHex(status.ok, status.warn, v / warnAt);
  return mixHex(status.warn, status.bad, Math.min((v - warnAt) / (badAt - warnAt), 1));
};

/** Linear blend of two #rrggbb colours (t from 0 to 1). */
export const mixHex = (from: string, to: string, t: number): string => {
  const parse = (hex: string) => {
    const n = parseInt(hex.replace('#', '').slice(0, 6), 16);
    return [(n >> 16) & 255, (n >> 8) & 255, n & 255];
  };
  const a = parse(from);
  const b = parse(to);
  const k = Math.min(Math.max(t, 0), 1);
  return `#${a
    .map((c, i) =>
      Math.round(c + (b[i] - c) * k)
        .toString(16)
        .padStart(2, '0'),
    )
    .join('')}`;
};

/** A noisy pattern's suggestion by level: error lines are always kept. */
export const samplingSuggestion = (
  pattern: Pick<NoisyPattern, 'level'>,
): { label: string; keep: number | null; tone: InsightTone } => {
  const level = pattern.level.toLowerCase();
  if (ERROR_LEVELS.includes(level)) return { label: 'keep (errors)', keep: null, tone: 'neutral' };
  if (level === 'debug' || level === 'trace') return { label: 'drop', keep: 0, tone: 'info' };
  if (WARN_LEVELS.includes(level)) return { label: 'sample 25%', keep: 25, tone: 'warn' };
  return { label: 'sample 10%', keep: 10, tone: 'info' };
};

/** Lines kept per day and stored bytes saved per month when keeping keepPercent. */
export const samplingPreview = (
  perDay: number,
  keepPercent: number,
  bytesPerLine: number,
): { keptPerDay: number; savedBytesPerMonth: number | null } => {
  const keptPerDay = Math.round((perDay * keepPercent) / 100);
  const savedLines = (perDay - keptPerDay) * DAYS_PER_MONTH;
  return { keptPerDay, savedBytesPerMonth: bytesPerLine > 0 ? savedLines * bytesPerLine : null };
};

/**
 * The rules with rule added: before every other, since the first match wins
 * and a pattern rule is narrower than a service or level one. A rule for the
 * same service, level and pattern is replaced.
 */
export const withSampleRule = (rules: LogSampleRule[], rule: LogSampleRule): LogSampleRule[] => [
  rule,
  ...rules.filter(
    (r) =>
      !(r.service === rule.service && r.level === rule.level && (r.pattern ?? '') === rule.pattern),
  ),
];

/** The keep rate (0-1) a pattern's lines get now: the first matching rule's, else 1. */
export const currentSampleRate = (
  rules: LogSampleRule[],
  service: string,
  level: string,
  hash: string,
): number => {
  const match = rules.find(
    (r) =>
      (!r.service || r.service === service) &&
      (!r.level || r.level.toLowerCase() === level.toLowerCase()) &&
      (!r.pattern || r.pattern === hash),
  );
  return match ? match.rate : 1;
};

/** ANDs nodes (nulls skipped); null when none. */
export const andNodes = (...nodes: (QueryNode | null | undefined)[]): QueryNode | null => {
  const present = nodes.filter((n): n is QueryNode => !!n);
  if (present.length === 0) return null;
  if (present.length === 1) return present[0];
  return { type: 'logical', logic: { operator: 'and', children: present } };
};

export const levelsNode = (levels: string[]): QueryNode => ({
  type: 'condition',
  condition: { field: 'level', operator: 'in', value: levels },
});

const placeholder = (name: string): boolean => name.startsWith('(');

/** A Health map tile's filter: conditions and the matching search clauses. */
export const healthTileFilter = (
  groupBy: 'k8s' | 'host' | 'service',
  group: string,
  tile: string,
): { node: QueryNode | null; clauses: string[] } => {
  const parts: { field: string; value: string; contains?: boolean }[] = [];
  if (groupBy === 'k8s') {
    if (!placeholder(group)) {
      parts.push({ field: 'k8s_namespace_name', value: group });
      if (!placeholder(tile)) parts.push({ field: 'k8s_pod_name', value: tile, contains: true });
    } else if (!placeholder(tile)) {
      parts.push({ field: 'service_name', value: tile });
    }
  } else {
    if (groupBy === 'host' && !placeholder(group)) parts.push({ field: 'host_name', value: group });
    if (!placeholder(tile)) parts.push({ field: 'service_name', value: tile });
  }
  return {
    node: andNodes(
      ...parts.map((p): QueryNode => ({
        type: 'condition',
        condition: { field: p.field, operator: p.contains ? 'contains' : 'equals', value: p.value },
      })),
    ),
    clauses: parts.map((p) => `${p.field}${p.contains ? ':' : '='}${quoteSearchValue(p.value)}`),
  };
};

/** A search-bar value, quoted when it holds spaces or syntax characters. */
export const quoteSearchValue = (value: string): string =>
  value === '' || /[\s"():=!<>]/.test(value)
    ? `"${value.replace(/\\/g, '\\\\').replace(/"/g, '\\"')}"`
    : value;
