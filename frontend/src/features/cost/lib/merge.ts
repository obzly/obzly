// A control plane asks every Site and adds the answers up per currency; a Site that fails is
// reported beside the result, never counted as zero.
import type {
  Coverage,
  CurrencyBlock,
  CurrencySummary,
  DailyPoint,
  ExploreResponse,
  ExploreRow,
  FacetValue,
  FacetsResponse,
  SourceStatus,
} from '../../../entity/cost';

export interface CostSite {
  siteId: string;
  siteName: string;
}

export interface SiteOutcome<T> {
  site: CostSite;
  value?: T;
  error?: string;
}

export interface SiteSource extends SourceStatus, CostSite {}

export interface MergedSummary {
  today: string;
  currencies: CurrencySummary[];
  sources: SiteSource[];
  openAnomalies: number;
}

/** full only when every Site in scope had all of it; '' (no connection) doesn't count. */
export const mergeCoverage = (values: Coverage[]): Coverage => {
  const known = values.filter((value) => value !== '');
  if (known.length === 0) return '';
  if (known.every((value) => value === 'full')) return 'full';
  if (known.every((value) => value === 'none')) return 'none';
  return 'partial';
};

const addInto = (target: number[], values: number[]) =>
  values.forEach((value, index) => {
    target[index] = (target[index] ?? 0) + value;
  });

const succeeded = <T>(outcomes: SiteOutcome<T>[]): T[] =>
  outcomes.flatMap((outcome) => (outcome.value === undefined ? [] : [outcome.value]));

export const mergeExplore = (outcomes: SiteOutcome<ExploreResponse>[]): ExploreResponse => {
  const responses = succeeded(outcomes);
  if (responses.length === 0) return { periods: [], currencies: [] };
  const periods = responses[0].periods.map((period, index) => ({
    ...period,
    coverage: mergeCoverage(responses.map((response) => response.periods[index]?.coverage ?? '')),
  }));
  const width = periods.length;
  interface Block {
    rows: Map<string, ExploreRow>;
    other?: ExploreRow;
    totals: number[];
    total: number;
  }
  const blocks = new Map<string, Block>();
  for (const response of responses) {
    for (const block of response.currencies) {
      const merged: Block = blocks.get(block.currency) ?? {
        rows: new Map<string, ExploreRow>(),
        totals: new Array<number>(width).fill(0),
        total: 0,
      };
      blocks.set(block.currency, merged);
      for (const row of block.rows) {
        const existing = merged.rows.get(row.key);
        if (existing) {
          addInto(existing.values, row.values);
          existing.total += row.total;
          existing.label ||= row.label;
        } else {
          merged.rows.set(row.key, { ...row, values: [...row.values] });
        }
      }
      if (block.other) {
        merged.other ??= {
          key: '',
          label: 'Other',
          values: new Array<number>(width).fill(0),
          total: 0,
        };
        addInto(merged.other.values, block.other.values);
        merged.other.total += block.other.total;
        if (responses.length === 1) merged.other.label = block.other.label;
      }
      addInto(merged.totals, block.totals);
      merged.total += block.total;
    }
  }
  const currencies: CurrencyBlock[] = [...blocks.entries()]
    .map(([currency, block]) => ({
      currency,
      rows: [...block.rows.values()].sort((a, b) => b.total - a.total),
      other: block.other,
      totals: block.totals,
      total: block.total,
    }))
    .sort((a, b) => b.total - a.total);
  return { periods, currencies };
};

export const mergeFacets = (outcomes: SiteOutcome<FacetsResponse>[]): FacetsResponse => {
  const dimensions: Record<string, Map<string, FacetValue>> = {};
  for (const response of succeeded(outcomes)) {
    for (const [dimension, values] of Object.entries(response.dimensions)) {
      const merged = (dimensions[dimension] ??= new Map());
      for (const value of values) {
        const existing = merged.get(value.value);
        if (existing) {
          for (const [currency, amount] of Object.entries(value.amounts)) {
            existing.amounts[currency] = (existing.amounts[currency] ?? 0) + amount;
          }
          existing.label ||= value.label;
        } else {
          merged.set(value.value, { ...value, amounts: { ...value.amounts } });
        }
      }
    }
  }
  return {
    dimensions: Object.fromEntries(
      Object.entries(dimensions).map(([dimension, values]) => [
        dimension,
        [...values.values()].sort((a, b) => facetSize(b) - facetSize(a)),
      ]),
    ),
  };
};

/** A value's largest amount in any currency, to rank values without adding currencies. */
export const facetSize = (value: FacetValue): number =>
  Math.max(0, ...Object.values(value.amounts).map(Math.abs));

const mergeDaily = (lists: DailyPoint[][]): DailyPoint[] => {
  const days = new Map<string, DailyPoint>();
  for (const point of lists.flat()) {
    const existing = days.get(point.day);
    if (existing) {
      existing.amount += point.amount;
      existing.estimated = existing.estimated || point.estimated;
    } else {
      days.set(point.day, { ...point });
    }
  }
  return [...days.values()].sort((a, b) => a.day.localeCompare(b.day));
};

export const mergeSummary = (
  outcomes: SiteOutcome<{
    today: string;
    currencies: CurrencySummary[];
    sources: SourceStatus[];
    openAnomalies: number;
  }>[],
): MergedSummary => {
  const ok = outcomes.filter((outcome) => outcome.value);
  const byCurrency = new Map<string, CurrencySummary[]>();
  for (const outcome of ok) {
    for (const summary of outcome.value!.currencies) {
      byCurrency.set(summary.currency, [...(byCurrency.get(summary.currency) ?? []), summary]);
    }
  }
  const currencies = [...byCurrency.entries()].map(([currency, list]) => {
    const sum = (pick: (summary: CurrencySummary) => number) =>
      list.reduce((total, summary) => total + pick(summary), 0);
    return {
      currency,
      monthToDate: sum((summary) => summary.monthToDate),
      lastMonth: sum((summary) => summary.lastMonth),
      lastMonthSameDays: sum((summary) => summary.lastMonthSameDays),
      // A forecast missing on one Site would understate the total, so there's none then.
      forecast: list.every((summary) => summary.forecast !== undefined)
        ? sum((summary) => summary.forecast ?? 0)
        : undefined,
      daily: mergeDaily(list.map((summary) => summary.daily)),
    };
  });
  return {
    today:
      ok
        .map((outcome) => outcome.value!.today)
        .sort()
        .pop() ?? new Date().toISOString().slice(0, 10),
    currencies: currencies.sort((a, b) => b.monthToDate - a.monthToDate),
    sources: ok.flatMap((outcome) =>
      outcome.value!.sources.map((source) => ({ ...source, ...outcome.site })),
    ),
    openAnomalies: ok.reduce((total, outcome) => total + outcome.value!.openAnomalies, 0),
  };
};
