import type { QueryNode } from '../../query';

// ai_cost rules are kept by AI budgets only; cloud_cost rules by cost budgets and anomaly alerts.
export type RuleSource =
  'metrics' | 'logs' | 'log_patterns' | 'logs_sql' | 'traces' | 'ai_cost' | 'cloud_cost';
export type Comparison = '>' | '>=' | '<' | '<=' | '==' | '!=';
export type ChannelKind = 'webhook' | 'slack' | 'pagerduty' | 'email';
export type Severity = 'critical' | 'high' | 'medium' | 'low';

export interface RuleQuery {
  promql?: string;
  /** logs_sql: one SELECT over the logs table; each row is a series (label columns and a number). */
  sql?: string;
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
  createdAt?: string;
  severity: Severity;
  /** Runbook text, .Rule.Description in message templates. */
  description: string;
}

export type RuleRequest = Omit<
  AlertRule,
  | 'id'
  | 'projectId'
  | 'lastEvalAt'
  | 'lastError'
  | 'enabled'
  | 'managedBy'
  | 'createdAt'
  | 'severity'
  | 'description'
> & {
  enabled?: boolean;
  /** Empty: from the severity label, else medium. */
  severity?: Severity;
  description?: string;
};

/** One rule's counts, one per day of RuleActivityResponse.days. */
export interface RuleActivity {
  ruleId: string;
  /** Series that went from pending to firing. */
  fired: number[];
  /** Deliveries to its channels: firing, reminders and resolved (silenced ones aren't sent). */
  notifications: number[];
  firedTotal: number;
  notificationsTotal: number;
}

export interface RuleActivityResponse {
  /** UTC days (YYYY-MM-DD), oldest first; the last is today so far. */
  days: string[];
  /** When the Site started counting; earlier days are unknown. Null when it can't tell. */
  trackedSince: string | null;
  rules: RuleActivity[];
}

/** The Alerts page header's counts. */
export interface AlertsSummary {
  firing: number;
  pending: number;
  rules: number;
  /** Current or upcoming, as the Silences tab lists them. */
  silences: number;
}

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
  useEmoji?: boolean;
  templateOverride?: boolean;
  /** The last send-time render failure of its template (the built-in one was sent). */
  templateError?: string;
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
  useEmoji: boolean;
  templateOverride: boolean;
  templateSubject: string;
  templateBody: string;
  /** The last send-time render failure of its template, on the deployment that sends. */
  templateError: string;
  templateErrorAt?: string | null;
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
  /** The AI assistant (OAuth app or access token) that created it through MCP. */
  mcpClient?: string;
}

export interface SilenceRequest {
  matchers: Matcher[];
  startsAt?: string;
  endsAt: string;
  comment: string;
}

/** A channel kind's editable parts: email subject and body; Slack text, PagerDuty summary or a
 * webhook's custom JSON in body (an empty webhook body sends the built-in JSON). */
export interface MessageTemplate {
  subject: string;
  body: string;
}

export interface MessageTemplateView extends MessageTemplate {
  kind: ChannelKind;
  /** Nothing stored: the built-in template applies. */
  isDefault: boolean;
  defaultSubject: string;
  defaultBody: string;
  updatedAt: string | null;
  lastError: string;
  lastErrorAt: string | null;
}

export interface MessageTemplatesView {
  /** Read-only here: a Site enrolled in a control plane gets them from it. */
  managed: boolean;
  templates: MessageTemplateView[];
}

export interface TemplateError {
  field: 'subject' | 'body';
  /** 0 when unknown. */
  line: number;
  message: string;
}

export interface TemplatePreviewRequest extends MessageTemplate {
  kind: ChannelKind;
  useEmoji?: boolean;
}

export interface TemplatePreviewSample {
  key: string;
  label: string;
  status: 'firing' | 'resolved' | 'test';
  severity: Severity;
  emoji: string;
  subject?: string;
  text?: string;
  /** A webhook's JSON, indented. */
  body?: string;
  /** Slack's side bar. */
  color?: string;
  error?: string;
}

export interface TemplatePreviewResponse {
  errors: TemplateError[];
  samples: TemplatePreviewSample[];
}

export interface ChannelTemplateRequest extends MessageTemplate {
  useOrgDefault: boolean;
  useEmoji: boolean;
}

export interface IncidentNotificationSettings {
  projectId: string;
  notifyOpen: boolean;
  notifyEnd: boolean;
  minSignals: number;
  minServices: number;
  severity: Severity;
  channelIds: string[];
  updatedAt?: string | null;
}

export type IncidentNotificationRequest = Omit<
  IncidentNotificationSettings,
  'projectId' | 'updatedAt'
>;
