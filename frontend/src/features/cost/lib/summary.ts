// Pure helpers behind the Cloud summary (Cost → Summary): cost and posture side by side. Amounts
// stay in their own currency; anything a Site didn't answer is left out, never counted as zero.
import type {
  CommitmentCoverage,
  CostAnomaly,
  CostBudget,
  CostRecommendation,
  ExploreResponse,
  RecommendationKind,
} from '../../../entity/cost';
import type {
  PostureAction,
  PostureSummary,
  Severity,
  StandardScore,
  TrendsResponse,
} from '../../../entity/posture';
import { type DayRange, addDays } from './ranges';

export type CostBasis = 'effective' | 'billed';

const parse = (day: string): Date => new Date(`${day}T00:00:00Z`);
const format = (date: Date): string => date.toISOString().slice(0, 10);

export const monthStart = (today: string): string => `${today.slice(0, 8)}01`;

/** The days of the month: today's number and how many it has. */
export const monthProgress = (today: string): { day: number; days: number } => {
  const date = parse(today);
  const days = new Date(Date.UTC(date.getUTCFullYear(), date.getUTCMonth() + 1, 0)).getUTCDate();
  return { day: date.getUTCDate(), days };
};

/** Last month's first day up to the same day number (its last day when shorter). */
export const sameDaysLastMonth = (today: string): DayRange => {
  const date = parse(today);
  const first = new Date(Date.UTC(date.getUTCFullYear(), date.getUTCMonth() - 1, 1));
  const lastDay = new Date(Date.UTC(date.getUTCFullYear(), date.getUTCMonth(), 0)).getUTCDate();
  const end = new Date(
    Date.UTC(first.getUTCFullYear(), first.getUTCMonth(), Math.min(date.getUTCDate(), lastDay)),
  );
  return { from: format(first), to: format(end) };
};

/** From last month's first day to today: one daily explore covers this month and the comparison. */
export const dailyRange = (today: string): DayRange => ({
  from: sameDaysLastMonth(today).from,
  to: today,
});

export interface DailyStack {
  day: string;
  /** Per provider key (aws, azure, ...), in the explore's row order. */
  amounts: Record<string, number>;
  total: number;
  estimated: boolean;
}

export interface BasisTotals {
  monthToDate: number;
  sameDaysLastMonth: number;
  /** This month's days, stacked by provider. */
  days: DailyStack[];
  providers: string[];
}

/**
 * Month to date, the same days of last month and this month's days by provider, from a daily
 * explore grouped by provider over dailyRange(today). estimatedDays marks days not final yet.
 */
export const basisTotals = (
  explore: ExploreResponse,
  currency: string,
  today: string,
  estimatedDays: ReadonlySet<string> = new Set(),
): BasisTotals | undefined => {
  const block = explore.currencies.find((item) => item.currency === currency);
  if (!block) return undefined;
  const start = monthStart(today);
  const before = sameDaysLastMonth(today);
  const rows = [...block.rows, ...(block.other ? [block.other] : [])];
  const providers = rows.map((row) => row.key || 'other');
  let monthToDate = 0;
  let previous = 0;
  const days: DailyStack[] = [];
  explore.periods.forEach((period, index) => {
    const total = block.totals[index] ?? 0;
    if (period.from >= start) {
      monthToDate += total;
      const amounts: Record<string, number> = {};
      rows.forEach((row, rowIndex) => {
        amounts[providers[rowIndex]] =
          (amounts[providers[rowIndex]] ?? 0) + (row.values[index] ?? 0);
      });
      days.push({ day: period.from, amounts, total, estimated: estimatedDays.has(period.from) });
    } else if (period.from >= before.from && period.from <= before.to) {
      previous += total;
    }
  });
  return { monthToDate, sameDaysLastMonth: previous, days, providers: [...new Set(providers)] };
};

/** The providers' forecast for the rest of the month, added to this basis's month to date. */
export const basisForecast = (
  effectiveForecast: number | undefined,
  effectiveMonthToDate: number,
  basisMonthToDate: number,
): number | undefined =>
  effectiveForecast === undefined
    ? undefined
    : basisMonthToDate + Math.max(0, effectiveForecast - effectiveMonthToDate);

export interface ServiceRow {
  key: string;
  label: string;
  total: number;
  /** Percent change against the same days last month; undefined when it had none. */
  change?: number;
}

/** Top services this month with their change against the same days of last month. */
export const serviceRows = (
  monthToDate: ExploreResponse,
  before: ExploreResponse,
  currency: string,
  top = 6,
): ServiceRow[] => {
  const now = monthToDate.currencies.find((item) => item.currency === currency);
  if (!now) return [];
  const previous = new Map(
    (before.currencies.find((item) => item.currency === currency)?.rows ?? []).map((row) => [
      row.key,
      row.total,
    ]),
  );
  return now.rows.slice(0, top).map((row) => {
    const was = previous.get(row.key);
    return {
      key: row.key,
      label: row.label || row.key || 'No service',
      total: row.total,
      change: was ? ((row.total - was) / Math.abs(was)) * 100 : undefined,
    };
  });
};

export interface OpenSavings {
  /** Monthly, known amounts only. */
  total: number;
  count: number;
  /** Open items whose savings aren't known yet. */
  unknown: number;
  topKind?: RecommendationKind;
}

export const openSavings = (items: CostRecommendation[], currency: string): OpenSavings => {
  const open = items.filter(
    (item) => (item.status === 'open' || item.status === 'planned') && item.currency === currency,
  );
  const byKind = new Map<RecommendationKind, number>();
  let total = 0;
  let unknown = 0;
  for (const item of open) {
    if (item.monthlySavings === null) {
      unknown += 1;
      continue;
    }
    total += item.monthlySavings;
    byKind.set(item.kind, (byKind.get(item.kind) ?? 0) + item.monthlySavings);
  }
  const topKind = [...byKind.entries()].sort((a, b) => b[1] - a[1])[0]?.[0];
  return { total, count: open.length, unknown, topKind };
};

export interface RealizedSavings {
  realized: number;
  expected: number;
  /** Done items with a measured saving. */
  measured: number;
  /** Done items still waiting for their measurement. */
  pending: number;
}

export const realizedSavings = (items: CostRecommendation[], currency: string): RealizedSavings => {
  const done = items.filter((item) => item.status === 'done' && item.currency === currency);
  const result: RealizedSavings = { realized: 0, expected: 0, measured: 0, pending: 0 };
  for (const item of done) {
    if (item.realizedMonthly === null || item.realizedMonthly === undefined) {
      result.pending += 1;
      continue;
    }
    result.measured += 1;
    result.realized += item.realizedMonthly;
    result.expected += item.expectedMonthly ?? item.monthlySavings ?? 0;
  }
  return result;
};

/** The unfiltered budgets in a currency (each a project's whole spend), added up. */
export const totalBudget = (
  budgets: CostBudget[],
  currency: string,
): { amount: number; count: number; thresholdPercent?: number } | undefined => {
  const whole = budgets.filter(
    (budget) =>
      budget.currency === currency &&
      Object.values(budget.filters).every((value) =>
        Array.isArray(value) ? value.length === 0 : !value || Object.keys(value).length === 0,
      ),
  );
  if (whole.length === 0) return undefined;
  const thresholds = new Set(whole.map((budget) => budget.thresholdPercent));
  return {
    amount: whole.reduce((sum, budget) => sum + budget.amount, 0),
    count: whole.length,
    thresholdPercent: thresholds.size === 1 ? [...thresholds][0] : undefined,
  };
};

/** The open anomaly this month that cost the most over what was expected. */
export const topAnomaly = (
  anomalies: CostAnomaly[],
  currency: string,
  today: string,
): CostAnomaly | undefined =>
  anomalies
    .filter(
      (anomaly) =>
        anomaly.currency === currency &&
        anomaly.status === 'open' &&
        anomaly.day >= monthStart(today),
    )
    .sort((a, b) => b.actual - b.expected - (a.actual - a.expected))[0];

export const anomalyRise = (anomaly: CostAnomaly): string =>
  anomaly.expected > 0
    ? `+${Math.round(((anomaly.actual - anomaly.expected) / anomaly.expected) * 100)}%`
    : 'new spend';

/** Commitment coverage across connections and types, weighted by what stays on demand. */
export const commitmentCoverage = (rows: CommitmentCoverage[]): number | undefined => {
  if (rows.length === 0) return undefined;
  // coveredPercent of eligible spend; onDemandMonthly is the uncovered rest, so eligible spend is
  // onDemand / (1 - covered). Rows without it count equally.
  const weighted = rows.filter(
    (row) => row.onDemandMonthly !== null && row.onDemandMonthly > 0 && row.coveredPercent < 100,
  );
  if (weighted.length === rows.length) {
    let covered = 0;
    let eligible = 0;
    for (const row of weighted) {
      const total = row.onDemandMonthly! / (1 - row.coveredPercent / 100);
      eligible += total;
      covered += total - row.onDemandMonthly!;
    }
    return eligible > 0 ? (covered / eligible) * 100 : undefined;
  }
  return rows.reduce((sum, row) => sum + row.coveredPercent, 0) / rows.length;
};

export interface StandardRow extends StandardScore {
  /** Points since a week ago, from the score trend; undefined without a point that old. */
  weekChange?: number;
}

export const standardRows = (summary: PostureSummary, today: string): StandardRow[] => {
  const weekAgo = addDays(today, -7);
  return summary.standards
    .filter((standard) => standard.evaluated > 0)
    .map((standard) => {
      const trend = standard.trend ?? [];
      const then = [...trend].reverse().find((point) => point.day <= weekAgo);
      return {
        ...standard,
        weekChange: then ? Math.round((standard.percent - then.percent) * 10) / 10 : undefined,
      };
    });
};

export const SUMMARY_SEVERITIES: Severity[] = ['critical', 'high', 'medium', 'low'];

/** Open findings per severity now and the change since the first trend period. */
export const severityChanges = (
  summary: PostureSummary,
  trends: TrendsResponse | undefined,
): { severity: Severity; count: number; change?: number }[] => {
  const first = trends?.periods[0];
  return SUMMARY_SEVERITIES.map((severity) => {
    const count = summary.open[severity] ?? 0;
    const before = first?.open[severity];
    return { severity, count, change: before === undefined ? undefined : count - before };
  });
};

export type NextAction =
  | { kind: 'cost'; key: string; item: CostRecommendation }
  | { kind: 'posture'; key: string; item: PostureAction };

const SEVERITY_RANK: Record<Severity, number> = {
  critical: 0,
  high: 1,
  medium: 2,
  low: 3,
  informational: 4,
};

/**
 * The best next steps: open cost recommendations by monthly savings (unknown last) and open
 * posture actions by their Site's rank, alternating so neither crowds out the other.
 */
export const nextActions = (
  recommendations: CostRecommendation[],
  actions: PostureAction[],
  currency: string | undefined,
  limit = 5,
): NextAction[] => {
  const cost: NextAction[] = recommendations
    .filter((item) => item.status === 'open' && (!currency || item.currency === currency))
    .sort((a, b) => (b.monthlySavings ?? -1) - (a.monthlySavings ?? -1))
    .map((item) => ({ kind: 'cost', key: `cost:${item.projectId}:${item.key}`, item }));
  const posture: NextAction[] = actions
    .filter((item) => item.status === 'open' && !item.dismissed)
    .sort(
      (a, b) =>
        b.rank - a.rank ||
        SEVERITY_RANK[a.severity ?? 'informational'] - SEVERITY_RANK[b.severity ?? 'informational'],
    )
    .map((item) => ({ kind: 'posture', key: `posture:${item.projectId}:${item.key}`, item }));
  const result: NextAction[] = [];
  for (
    let index = 0;
    result.length < limit && (index < cost.length || index < posture.length);
    index++
  ) {
    if (index < cost.length) result.push(cost[index]);
    if (result.length < limit && index < posture.length) result.push(posture[index]);
  }
  return result;
};
