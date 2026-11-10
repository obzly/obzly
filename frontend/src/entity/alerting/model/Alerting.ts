import type { QueryNode } from '../../query';

// ai_cost rules are kept by AI budgets only; cloud_cost rules by cost budgets and anomaly alerts.
export type RuleSource = 'metrics' | 'logs' | 'log_patterns' | 'traces' | 'ai_cost' | 'cloud_cost';
export type Comparison = '>' | '>=' | '<' | '<=' | '==' | '!=';
export type ChannelKind = 'webhook' | 'slack' | 'pagerduty' | 'email';

export interface RuleQuery {
  promql?: string;
  logs?: QueryNode;
  groupBy?: string;
  metric?: string;
  serviceName?: string;
  operationName?: string;
  rootSpansOnly?: boolean;
  /** log_patterns: the window before the rule's window it compares with. */
  baselineSeconds?: number;
  /** log_patterns: fewest logs for a pattern to count. */
  minCount?: number;
  /** ai_cost: the AI budget the rule watches. */
  budgetId?: string;
}

export interface AlertRule {
  id: string;
  projectId: string;
  name: string;
  enabled: boolean;
  source: RuleSource;
  query: RuleQuery;
  comparison: Comparison;
  threshold: number;
  windowSeconds: number;
  intervalSeconds: number;
  forSeconds: number;
  repeatSeconds: number;
  labels: Record<string, string>;
  summary: string;
  lastEvalAt?: string;
  lastError: string;
  channelIds: string[];
  /** Set on rules a feature keeps (e.g. "synthetics:<monitor>:down"); read-only here. */
  managedBy?: string;
}

export type RuleRequest = Omit<
  AlertRule,
  'id' | 'projectId' | 'lastEvalAt' | 'lastError' | 'enabled' | 'managedBy'
> & { enabled?: boolean };

export interface PreviewSeries {
  labels: Record<string, string>;
  value: number;
  firing: boolean;
}

export interface AlertView {
  ruleId: string;
  ruleName: string;
  fingerprint: string;
  labels: Record<string, string>;
  state: 'pending' | 'firing' | 'resolved';
  value: number;
  activeAt: string;
  firedAt?: string;
  resolvedAt?: string;
  lastNotifiedAt?: string;
}

export interface ChannelConfig {
  url?: string;
  headers?: Record<string, string>;
  routingKey?: string;
  to?: string[];
}

export interface NotificationChannel {
  id: string;
  name: string;
  kind: ChannelKind;
  // Non-secret description of where it sends.
  target: string;
  /** central: sent by the control plane; local: this Site's own. */
  source: 'central' | 'local';
}

/** Where a control plane's channel can be used. */
export interface ChannelScope {
  allSites: boolean;
  sites?: string[];
  projects?: { siteId: string; projectId: string }[];
}

export interface SettingsChannel {
  id: string;
  name: string;
  kind: ChannelKind;
  target: string;
  source: 'central' | 'local';
  scope?: ChannelScope;
  allProjects: boolean;
  projectIds: string[];
  usedBy: number;
  updatedAt: string;
}

export interface SettingsChannelRequest {
  name: string;
  kind: ChannelKind;
  // null keeps the stored config on update
  config: ChannelConfig | null;
  scope?: ChannelScope;
  allProjects?: boolean;
  projectIds?: string[];
}

export interface SMTPSettings {
  configured: boolean;
  source?: 'central' | 'local';
  host: string;
  port: number;
  username: string;
  passwordSet: boolean;
  from: string;
  tls: 'starttls' | 'tls';
  updatedAt?: string;
  /** Read-only here: set on the control plane. */
  managed: boolean;
}

export interface SMTPRequest {
  host: string;
  port: number;
  username: string;
  /** Empty keeps the stored password. */
  password: string;
  from: string;
  tls: 'starttls' | 'tls';
}

export interface SiteSync {
  siteId: string;
  /** Last served to the Site. */
  version: string;
  fetchedAt: string | null;
  /** What the Site reports it applied; current compares this with what it would get now. */
  appliedVersion: string;
  appliedAt: string | null;
  current: boolean;
  /** The pinned key settings are sealed to. */
  keyFingerprint?: string;
  /** The Site now serves another key; nothing is sealed to it until an admin accepts it. */
  pendingKeyFingerprint?: string;
  keyCheckedAt: string | null;
}

export interface SettingsSync {
  mode: 'control-plane' | 'site' | 'standalone';
  sites?: SiteSync[];
  applied?: { version: string; issuedAt: string; fetchedAt: string };
  /** On a Site: its own key, to compare with what its control plane shows. */
  keyFingerprint?: string;
}

export interface AdoptResult {
  channels: string[];
  smtp: boolean;
  smtpSkipped: boolean;
}

export interface Matcher {
  name: string;
  value: string;
  regex?: boolean;
}

export interface Silence {
  id: string;
  projectId: string;
  matchers: Matcher[];
  startsAt: string;
  endsAt: string;
  comment: string;
  /** Set on a silence a Synthetics maintenance window keeps; read-only. */
  managedBy?: string;
}

export interface SilenceRequest {
  matchers: Matcher[];
  startsAt?: string;
  endsAt: string;
  comment: string;
}
