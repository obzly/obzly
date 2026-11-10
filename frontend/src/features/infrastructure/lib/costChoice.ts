// The cost use's choices in the cloud connection wizard and what each would cost.
import type {
  CloudConnection,
  CloudProvider,
  CostScope,
  CostSettings,
  CostSource,
} from '../../../entity/cloud';

/** The wizard's cost fields; resourceGroup and accounts only shape the template and estimate. */
export interface CostDraft {
  source: CostSource;
  scope: CostScope;
  viaConnectionId: string;
  bucket: string;
  region: string;
  storageAccount: string;
  container: string;
  prefix: string;
  resourceGroup: string;
  refreshHours: number;
  accounts: number;
}

export const COST_REFRESH_HOURS = [6, 8, 12, 24];

/** AWS Cost Explorer API, per request (aws.amazon.com/aws-cost-management/pricing). */
export const CE_REQUEST_USD = 0.01;

/**
 * Recommendations read Cost Explorer once a week whatever the source: purchase recommendations
 * for 4 types × 2 terms × 3 payments × 3 look-backs (RDS has no 3-year no-upfront), plus the
 * commitments' use and coverage.
 */
export const ADVICE_WEEKLY_REQUESTS = 75;

export const adviceMonthlyUsd = () => (ADVICE_WEEKLY_REQUESTS * 30 * CE_REQUEST_USD) / 7;

export const costDraftFrom = (settings?: CostSettings): CostDraft => ({
  source: settings?.source ?? 'auto',
  scope: settings?.scope ?? 'account',
  viaConnectionId: settings?.viaConnectionId ?? '',
  bucket: settings?.export?.bucket ?? '',
  region: settings?.export?.region ?? '',
  storageAccount: settings?.export?.storageAccount ?? '',
  container: settings?.export?.container ?? '',
  prefix: settings?.export?.prefix ?? '',
  resourceGroup: '',
  refreshHours: settings?.refreshHours ?? 12,
  accounts: 1,
});

export const costSettingsFrom = (draft: CostDraft, provider: CloudProvider): CostSettings => {
  if (draft.scope === 'via') {
    return { source: 'auto', scope: 'via', viaConnectionId: draft.viaConnectionId };
  }
  const settings: CostSettings = {
    source: draft.source,
    scope: draft.scope,
    refreshHours: draft.refreshHours,
  };
  if (draft.source !== 'api') {
    settings.export =
      provider === 'aws'
        ? { bucket: draft.bucket.trim(), region: draft.region.trim(), prefix: draft.prefix.trim() }
        : {
            storageAccount: draft.storageAccount.trim(),
            container: draft.container.trim(),
            prefix: draft.prefix.trim(),
          };
  }
  return settings;
};

/** Whether the cost step has what the chosen source needs. */
export const costDraftReady = (draft: CostDraft, provider: CloudProvider): boolean => {
  if (draft.scope === 'via') return !!draft.viaConnectionId;
  if (draft.source !== 'export') return true;
  return provider === 'aws'
    ? !!draft.bucket.trim()
    : !!draft.storageAccount.trim() && !!draft.container.trim();
};

/** Connections another one may read its cost through: same provider, billing for members. */
export const costViaOptions = (
  connections: CloudConnection[],
  provider: CloudProvider,
  self?: string,
): CloudConnection[] =>
  connections.filter(
    (connection) =>
      connection.id !== self &&
      connection.provider === provider &&
      connection.scanTypes.includes('cost') &&
      connection.cost?.scope === 'billing',
  );

export interface CostEstimate {
  /** US dollars a month for the billing API calls; 0 when they're free. */
  apiMonthly: number;
  apiRequests: number;
  /** One-off calls for the first 90 days. */
  backfillRequests: number;
}

/**
 * Cost Explorer is billed per request: each refresh reads the last days for every account in
 * scope (one request each; a second page is rare) plus one forecast. Azure's Cost Management
 * query API is free but throttled per tenant.
 */
export const estimateCost = (
  provider: CloudProvider,
  accounts: number,
  refreshHours: number,
): CostEstimate => {
  const refreshes = (30 * 24) / refreshHours;
  const perRefresh = Math.max(1, accounts) + 1;
  const apiRequests = Math.round(refreshes * perRefresh);
  return {
    apiMonthly: provider === 'aws' ? apiRequests * CE_REQUEST_USD : 0,
    apiRequests,
    backfillRequests: Math.max(1, accounts) * 2,
  };
};

export const formatUsd = (value: number): string =>
  value === 0 ? 'Free' : `$${value < 10 ? value.toFixed(2) : Math.round(value).toString()}`;
