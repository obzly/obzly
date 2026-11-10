import type { Granularity } from '../../../entity/cost';
import type { PostureFilters } from '../../../entity/posture';
import type { DayRange, RangePreset } from '../../cost';

/** "eu-west-1", or "Account-wide" for the global scope. */
export const regionLabel = (region: string) => (region === 'global' ? 'Account-wide' : region);

/** The last part of an ARN or resource ID, which names the resource. */
export const shortResource = (key: string): string => {
  const parts = key.split(/[/:]/).filter(Boolean);
  return parts.length > 1 ? `…/${parts[parts.length - 1]}` : key;
};

/** The Trends tab's state, kept as a saved view's state too. */
export interface TrendsState {
  range: RangePreset;
  custom?: DayRange;
  granularity: Granularity;
  filters: PostureFilters;
}

export const DEFAULT_TRENDS: TrendsState = { range: 'last90', granularity: 'monthly', filters: {} };
