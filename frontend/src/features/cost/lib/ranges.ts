// Ranges are whole UTC days, as the Site stores them; "today" is the Site's (summary.today).
import type { Granularity } from '../../../entity/cost';

export type RangePreset =
  'mtd' | 'last30' | 'last90' | 'lastMonth' | 'last6m' | 'last12m' | 'ytd' | 'lastYear' | 'custom';

export interface DayRange {
  from: string;
  to: string;
}

export const RANGE_LABEL: Record<RangePreset, string> = {
  mtd: 'Month to date',
  last30: 'Last 30 days',
  last90: 'Last 90 days',
  lastMonth: 'Last month',
  last6m: 'Last 6 months',
  last12m: 'Last 12 months',
  ytd: 'Year to date',
  lastYear: 'Last year',
  custom: 'Custom',
};

export const GRANULARITY_LABEL: Record<Granularity, string> = {
  daily: 'Daily',
  monthly: 'Monthly',
  quarterly: 'Quarterly',
  yearly: 'Yearly',
};

/** The Site answers daily periods for ranges of up to this many days. */
export const MAX_DAILY_DAYS = 92;

const parse = (day: string): Date => new Date(`${day}T00:00:00Z`);
const format = (date: Date): string => date.toISOString().slice(0, 10);
const utc = (year: number, month: number, day: number): string =>
  format(new Date(Date.UTC(year, month, day)));

export const addDays = (day: string, days: number): string => {
  const date = parse(day);
  date.setUTCDate(date.getUTCDate() + days);
  return format(date);
};

/** Days in from..to, both included. */
export const spanDays = (range: DayRange): number =>
  Math.round((parse(range.to).getTime() - parse(range.from).getTime()) / 86_400_000) + 1;

export const resolveRange = (preset: RangePreset, today: string, custom?: DayRange): DayRange => {
  const now = parse(today);
  const year = now.getUTCFullYear();
  const month = now.getUTCMonth();
  switch (preset) {
    case 'mtd':
      return { from: utc(year, month, 1), to: today };
    case 'last30':
      return { from: addDays(today, -29), to: today };
    case 'last90':
      return { from: addDays(today, -89), to: today };
    case 'lastMonth':
      return { from: utc(year, month - 1, 1), to: utc(year, month, 0) };
    case 'last6m':
      return { from: utc(year, month - 5, 1), to: today };
    case 'last12m':
      return { from: utc(year, month - 11, 1), to: today };
    case 'ytd':
      return { from: utc(year, 0, 1), to: today };
    case 'lastYear':
      return { from: utc(year - 1, 0, 1), to: utc(year - 1, 11, 31) };
    case 'custom':
      return custom ?? { from: utc(year, month, 1), to: today };
  }
};

export const allowedGranularities = (range: DayRange): Granularity[] =>
  spanDays(range) <= MAX_DAILY_DAYS
    ? ['daily', 'monthly', 'quarterly', 'yearly']
    : ['monthly', 'quarterly', 'yearly'];

/** Daily for up to a quarter, monthly beyond; a chosen one is kept while it still applies. */
export const fitGranularity = (range: DayRange, wanted?: Granularity): Granularity => {
  const allowed = allowedGranularities(range);
  if (wanted && allowed.includes(wanted)) return wanted;
  return spanDays(range) <= 45 ? 'daily' : 'monthly';
};

export const currentMonth = (today: string): string => today.slice(0, 7);
