// Cloud posture on a Site (docs/CLOUD_COST_POSTURE.md "Posture").
import type { Granularity } from '../../cost';

export type Severity = 'critical' | 'high' | 'medium' | 'low' | 'informational';
export type PostureSource = 'native' | 'baseline';
export type FindingStatusFilter = 'open' | 'suppressed' | 'resolved' | 'all';
export type CoverageStatus = 'native' | 'fallback' | 'baseline' | 'not_covered' | 'stale';
export type RunResult = 'ok' | 'not_enabled' | 'needs_permission' | 'error';

export interface StandardRef {
  standard: string;
  control: string;
}

export interface ScorePoint {
  day: string;
  percent: number;
}

export interface StandardScore {
  standard: string;
  name: string;
  passed: number;
  failed: number;
  evaluated: number;
  percent: number;
  sources: PostureSource[];
  trend?: ScorePoint[];
}

export interface CoverageCounts {
  native: number;
  fallback: number;
  baseline: number;
  notCovered: number;
  stale: number;
}

export interface PostureSummary {
  standards: StandardScore[];
  open: Partial<Record<Severity, number>>;
  suppressed: number;
  resolved30d: number;
  coverage: CoverageCounts;
  lastScanAt?: string;
}

export interface PostureFilters {
  status?: FindingStatusFilter;
  severity?: string[];
  standard?: string[];
  provider?: string[];
  account?: string[];
  region?: string[];
  source?: string[];
  connection?: string[];
  control?: string[];
  resource?: string[];
  search?: string;
}

export interface Finding {
  id: string;
  projectId: string;
  connectionId: string;
  connectionName: string;
  provider: 'aws' | 'azure';
  account: string;
  region: string;
  source: PostureSource;
  control: string;
  check?: string;
  title: string;
  severity: Severity;
  resourceKey: string;
  resourceType?: string;
  standards: StandardRef[];
  evidence: Record<string, string>;
  remediationUrl?: string;
  status: 'open' | 'resolved';
  firstSeen: string;
  lastSeen: string;
  resolvedAt?: string;
  suppressed: boolean;
  suppressReason?: string;
  suppressedUntil?: string;
  suppressedBy?: string;
}

export interface FindingEvent {
  kind: 'opened' | 'resolved' | 'reopened' | 'suppressed' | 'unsuppressed';
  severity: Severity;
  at: string;
  by?: string;
  note?: string;
}

export interface Remediation {
  cli: string;
  iac: string;
  console: string;
}

export interface FindingDetail extends Finding {
  events: FindingEvent[];
  standardNames: Record<string, string>;
  remediation?: Remediation;
}

export interface FindingsRequest {
  filters: PostureFilters;
  sort?: 'severity' | 'firstSeen' | 'lastSeen' | 'title';
  desc?: boolean;
  offset?: number;
  limit?: number;
}

export interface FindingsResponse {
  total: number;
  findings: Finding[];
}

export interface PostureFacetValue {
  value: string;
  label?: string;
  count: number;
}

export interface PostureFacets {
  dimensions: Record<string, PostureFacetValue[]>;
}

export interface FindingUpdate {
  suppressed: boolean;
  reason: string;
  /** RFC 3339; empty never ends. */
  until?: string;
}

export interface CoverageRun {
  standard: string;
  source: PostureSource;
  result: RunResult;
  reason?: string;
  passed: number;
  failed: number;
}

export interface CoverageRow {
  connectionId: string;
  connectionName: string;
  provider: 'aws' | 'azure';
  account: string;
  region: string;
  status: CoverageStatus;
  reason?: string;
  scannedAt: string;
  runs: CoverageRun[];
}

export interface TrendsRequest {
  from: string;
  to: string;
  granularity: Granularity;
  filters: PostureFilters;
}

export interface TrendPeriod {
  key: string;
  from: string;
  to: string;
  open: Partial<Record<Severity, number>>;
  opened: number;
  resolved: number;
  scores: Record<string, number>;
}

export interface TrendsResponse {
  periods: TrendPeriod[];
}

export interface PostureAlertSettings {
  channelIds: string[];
  minSeverity: Exclude<Severity, 'informational'>;
}

// Posture recommendations: ranked actions, guardrails and coverage fixes; Obzly changes nothing.
export type ActionKind = 'coverage' | 'fix' | 'guardrail';
export type ActionStatus = 'open' | 'done' | 'accepted_risk';

export interface ActionFinding {
  id: string;
  control: string;
  title: string;
  severity: Severity;
  resourceKey: string;
  status: 'open' | 'resolved';
  suppressed: boolean;
}

export interface ActionCostLink {
  key: string;
  action: string;
  currency: string;
  monthlySavings: number | null;
}

export interface PostureAction {
  key: string;
  projectId: string;
  kind: ActionKind;
  title: string;
  detail?: string;
  connectionName?: string;
  provider?: string;
  account?: string;
  region?: string;
  severity?: Severity;
  effort: 'low' | 'medium' | 'high';
  rank: number;
  findings: ActionFinding[];
  prevents?: string[];
  scopes?: string[];
  steps?: string[];
  costLinks?: ActionCostLink[];
  status: ActionStatus;
  planned: boolean;
  dismissed: boolean;
  dismissReason?: string;
}

export interface SimulatedScore {
  standard: string;
  name: string;
  evaluated: number;
  current: number;
  withPlan: number;
  withAll: number;
}

export interface ActionsResponse {
  actions: PostureAction[];
  standards: SimulatedScore[];
}

export interface ActionUpdate {
  projectId: string;
  key: string;
  planned?: boolean;
  dismissed?: boolean;
  reason?: string;
}
