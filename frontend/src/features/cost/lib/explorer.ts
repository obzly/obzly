// The Explorer's state, kept as a saved view's state too, and the filter helpers behind it.
import type {
  CostDimension,
  CostFilters,
  CostMetric,
  ExploreResponse,
  Granularity,
} from '../../../entity/cost';
import type { DayRange, RangePreset } from './ranges';

export interface ExplorerState {
  range: RangePreset;
  /** Only for a custom range. */
  custom?: DayRange;
  granularity: Granularity;
  groupBy: CostDimension;
  metric: CostMetric;
  filters: CostFilters;
}

export const DEFAULT_EXPLORER: ExplorerState = {
  range: 'last6m',
  granularity: 'monthly',
  groupBy: 'service',
  metric: 'effective',
  filters: {},
};

export type BaseDimension = Exclude<CostDimension, `tag:${string}`>;

export const BASE_DIMENSIONS: BaseDimension[] = [
  'provider',
  'account',
  'region',
  'service',
  'resource',
  'charge',
];

export const DIMENSION_LABEL: Record<BaseDimension, string> = {
  provider: 'Provider',
  account: 'Account',
  region: 'Region',
  service: 'Service',
  resource: 'Resource',
  charge: 'Charge type',
};

const PROVIDER_LABEL: Record<string, string> = { aws: 'AWS', azure: 'Azure' };

export const isTag = (dimension: CostDimension): dimension is `tag:${string}` =>
  dimension.startsWith('tag:');

export const tagKey = (dimension: `tag:${string}`): string => dimension.slice(4);

export const dimensionLabel = (dimension: CostDimension): string =>
  isTag(dimension) ? `Tag: ${tagKey(dimension)}` : DIMENSION_LABEL[dimension];

/** How a value reads: provider codes by name, empty as "(none)". */
export const valueLabel = (dimension: CostDimension, value: string, label?: string): string => {
  if (!value) return isTag(dimension) ? '(untagged)' : '(none)';
  if (dimension === 'provider') return PROVIDER_LABEL[value] ?? value;
  return label ? `${label} (${value})` : value;
};

/** What a click on a row opens next: from the broad to the single resource. */
const DRILL_NEXT: Partial<Record<BaseDimension, BaseDimension>> = {
  provider: 'account',
  account: 'service',
  region: 'service',
  charge: 'service',
  service: 'resource',
};

export const drillNext = (dimension: CostDimension): CostDimension | undefined =>
  isTag(dimension) ? 'service' : DRILL_NEXT[dimension];

export const filterValues = (filters: CostFilters, dimension: CostDimension): string[] =>
  (isTag(dimension) ? filters.tags?.[tagKey(dimension)] : filters[dimension]) ?? [];

/** filters with dimension set to values; no values removes the filter. */
export const setFilterValues = (
  filters: CostFilters,
  dimension: CostDimension,
  values: string[],
): CostFilters => {
  if (isTag(dimension)) {
    const tags = { ...filters.tags };
    if (values.length) tags[tagKey(dimension)] = values;
    else delete tags[tagKey(dimension)];
    const next: CostFilters = { ...filters, tags };
    if (Object.keys(tags).length === 0) delete next.tags;
    return next;
  }
  const next: CostFilters = { ...filters, [dimension]: values };
  if (!values.length) delete next[dimension];
  return next;
};

/** Every dimension with a filter set, tags last. */
export const activeDimensions = (filters: CostFilters): CostDimension[] => [
  ...BASE_DIMENSIONS.filter((dimension) => (filters[dimension]?.length ?? 0) > 0),
  ...Object.entries(filters.tags ?? {})
    .filter(([, values]) => values.length > 0)
    .map(([key]) => `tag:${key}` as const),
];

/** "Provider: Azure · Region: westeurope" for the filters other than dimension. */
export const scopeLine = (filters: CostFilters, except?: CostDimension): string =>
  activeDimensions(filters)
    .filter((dimension) => dimension !== except)
    .map(
      (dimension) =>
        `${dimensionLabel(dimension)}: ${filterValues(filters, dimension)
          .map((value) => valueLabel(dimension, value))
          .join(', ')}`,
    )
    .join(' · ');

/** One currency's pivot as CSV rows: a header, a row per group, the other groups and a total. */
export const exploreCsvRows = (
  response: ExploreResponse,
  currency: string,
  groupBy: CostDimension,
): (string | number)[][] => {
  const block = response.currencies.find((candidate) => candidate.currency === currency);
  if (!block) return [];
  const header = [
    dimensionLabel(groupBy),
    ...response.periods.map((period) => period.key),
    'Total',
  ];
  const rows = [...block.rows, ...(block.other ? [block.other] : [])].map((row) => [
    row.key ? valueLabel(groupBy, row.key, row.label) : (row.label ?? 'Other'),
    ...row.values.map(round),
    round(row.total),
  ]);
  return [
    [...header, 'Currency'],
    ...rows.map((row) => [...row, currency]),
    ['Total', ...block.totals.map(round), round(block.total), currency],
  ];
};

const round = (value: number): number => Math.round(value * 100) / 100;
