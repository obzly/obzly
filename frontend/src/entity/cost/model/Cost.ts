// Cloud cost on a Site (docs/CLOUD_COST_POSTURE.md "Cost"). Amounts are in their own currency;
// currencies are never added together.

export type Granularity = 'daily' | 'monthly' | 'quarterly' | 'yearly';
export type CostMetric = 'effective' | 'billed';
/** A dimension, or a tracked tag as `tag:<key>`. */
export type CostDimension =
  'provider' | 'account' | 'region' | 'service' | 'resource' | 'charge' | `tag:${string}`;
/** full: every connection in scope delivered the whole period; '' when none is in scope. */
export type Coverage = 'full' | 'partial' | 'none' | '';

export interface CostFilters {
  provider?: string[];
  account?: string[];
  region?: string[];
  service?: string[];
  resource?: string[];
  charge?: string[];
  tags?: Record<string, string[]>;
}

export interface ExploreRequest {
  from: string;
  to: string;
  granularity: Granularity;
  groupBy: CostDimension;
  metric: CostMetric;
  filters: CostFilters;
  projectId?: string;
}

export interface CostPeriod {
  key: string;
  from: string;
  to: string;
  coverage: Coverage;
}

export interface ExploreRow {
  key: string;
  label?: string;
  values: number[];
  total: number;
}

export interface CurrencyBlock {
  currency: string;
  rows: ExploreRow[];
  other?: ExploreRow;
  totals: number[];
  total: number;
}

export interface ExploreResponse {
  periods: CostPeriod[];
  currencies: CurrencyBlock[];
}

export interface FacetValue {
  value: string;
  label?: string;
  /** Effective cost per currency, never added together. */
  amounts: Record<string, number>;
}

export interface FacetsResponse {
  dimensions: Record<string, FacetValue[]>;
}

export interface DailyPoint {
  day: string;
  amount: number;
  estimated?: boolean;
}

export interface CurrencySummary {
  currency: string;
  monthToDate: number;
  lastMonth: number;
  lastMonthSameDays: number;
  forecast?: number;
  daily: DailyPoint[];
}

export interface SourceStatus {
  projectId: string;
  connectionId: string;
  connectionName: string;
  provider: 'aws' | 'azure';
  source: 'export' | 'api';
  from?: string;
  to?: string;
  finalThrough?: string;
  coveredFrom?: string;
  coveredTo?: string;
  rows: number;
  deliveredAt: string;
  forecastAt?: string;
}

export interface CostSummary {
  today: string;
  currencies: CurrencySummary[];
  sources: SourceStatus[];
  openAnomalies: number;
}

export interface ResourceCost {
  currency: string;
  days: DailyPoint[];
  total: number;
  byCharge: Record<string, number>;
}

export interface ResourceCostResponse {
  keys: string[];
  currencies: ResourceCost[];
  covered: boolean;
}

export interface CostAnomaly {
  id: string;
  projectId: string;
  connectionId: string;
  connectionName: string;
  day: string;
  subAccount: string;
  subAccountName?: string;
  /** '' is the whole account. */
  service: string;
  currency: string;
  expected: number;
  actual: number;
  score: number;
  status: 'open' | 'acknowledged';
  note: string;
  detectedAt: string;
}

export interface CostBudget {
  id: string;
  projectId: string;
  name: string;
  filters: CostFilters;
  currency: string;
  amount: number;
  thresholdPercent: number;
  channelIds: string[];
  spent: number;
  projected: number;
  percent: number;
  status: 'ok' | 'near' | 'over';
  /** forecast: the provider's (an unfiltered budget); run-rate: the last days' average. */
  method: 'forecast' | 'run-rate';
}

export interface CostBudgetRequest {
  name: string;
  filters: CostFilters;
  currency: string;
  amount: number;
  thresholdPercent: number;
  channelIds: string[];
}

export interface CostAlertSettings {
  anomalyChannelIds: string[];
}

// Recommendations (docs/CLOUD_COST_POSTURE.md "Recommendations"). Obzly never buys or changes
// anything; amounts are per month in the item's own currency.

export type RecommendationKind = 'rightsizing' | 'idle' | 'modernize' | 'storage';
export type RecommendationStatus = 'open' | 'planned' | 'done' | 'dismissed';
export type AdviceSectionName = 'rightsizing' | 'checks' | 'commitments' | 'planner';
export type AdviceState = 'ok' | 'not_enabled' | 'needs_permission' | 'error';
export type CommitmentType =
  'compute_sp' | 'ec2_instance_sp' | 'ec2_ri' | 'rds_ri' | 'azure_sp' | 'azure_reservation';

export interface AdviceSection {
  projectId: string;
  connectionId: string;
  connectionName: string;
  section: AdviceSectionName;
  state: AdviceState;
  reason?: string;
  refreshedAt: string;
}

export interface CostRecommendation {
  key: string;
  projectId: string;
  connectionId?: string;
  connectionName: string;
  provider: string;
  /** compute_optimizer, advisor or obzly. */
  source: string;
  kind: RecommendationKind;
  account: string;
  region: string;
  resourceKey: string;
  resourceName: string;
  resourceType: string;
  action: string;
  current?: string;
  target?: string;
  currency: string;
  /** null: not known yet (no resource-level cost), never zero. */
  monthlySavings: number | null;
  savingsBasis: 'provider' | 'resource_cost' | 'list_price' | '';
  effort: 'low' | 'medium' | 'high';
  risk: 'low' | 'medium' | 'high';
  evidence: Record<string, string>;
  refreshedAt?: string;
  status: RecommendationStatus;
  owner?: string;
  dueDate?: string;
  reason?: string;
  doneAt?: string;
  doneBy?: string;
  expectedMonthly?: number | null;
  realizedMonthly?: number | null;
  measuredAt?: string;
}

export interface RecommendationsResponse {
  items: CostRecommendation[];
  sections: AdviceSection[];
}

export interface RecommendationUpdate {
  projectId: string;
  key: string;
  status: RecommendationStatus;
  owner?: string;
  dueDate?: string;
  reason?: string;
}

export interface Commitment {
  projectId: string;
  connectionId: string;
  connectionName: string;
  provider: string;
  id: string;
  type: CommitmentType;
  account?: string;
  region?: string;
  description: string;
  term?: string;
  payment?: string;
  startsAt?: string;
  endsAt?: string;
  endsInDays?: number;
  currency?: string;
  monthlyCost: number | null;
  utilization: number | null;
  unusedMonthly: number | null;
  unused: boolean;
  endingSoon: boolean;
}

export interface CommitmentCoverage {
  connectionId: string;
  connectionName: string;
  type: CommitmentType;
  currency?: string;
  coveredPercent: number;
  onDemandMonthly: number | null;
}

export interface CommitmentsResponse {
  commitments: Commitment[];
  coverage: CommitmentCoverage[];
  sections: AdviceSection[];
}

export interface PurchaseLine {
  description: string;
  account?: string;
  region?: string;
  quantity?: string;
  monthlySavings: number;
  upfront: number;
}

export interface PurchaseOption {
  term: '1y' | '3y';
  payment: 'no_upfront' | 'partial_upfront' | 'all_upfront' | 'monthly' | 'upfront';
  offered: boolean;
  reason?: string;
  commitment?: string;
  hourlyCommitment: number;
  upfront: number;
  monthlyRecurring: number;
  monthlySavings: number;
  savingsPercent: number;
  breakEvenMonths: number;
  averageUtilization: number;
  coveragePercent: number;
  lines: PurchaseLine[];
  labels: ('lowest_risk' | 'most_savings')[];
}

export interface UsagePoint {
  day: string;
  onDemand: number;
  committed: number;
}

export interface Plan {
  connectionId: string;
  connectionName: string;
  provider: string;
  type: CommitmentType;
  lookbackDays: number;
  currency: string;
  onDemandMonthly: number;
  rightsizingMonthly: number;
  factor: number;
  refreshedAt: string;
  options: PurchaseOption[];
  usage: UsagePoint[];
}

export interface PlannerResponse {
  plans: Plan[];
  sections: AdviceSection[];
}
