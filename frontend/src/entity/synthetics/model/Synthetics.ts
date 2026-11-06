export type CheckType = 'http' | 'multi' | 'tls' | 'tcp' | 'dns' | 'icmp';
export type RunStatus = 'up' | 'warn' | 'down';
export type StepStatus = RunStatus | 'skipped';
export type ExtractSource = 'json' | 'header' | 'status' | 'regex' | 'body';
export type AssertionKind = 'status' | 'response_time_ms' | 'header' | 'json' | 'body';
export type AssertionOp = 'equals' | 'in' | 'below' | 'contains' | 'matches' | 'exists';
export type Severity = 'fail' | 'warn';

export interface Assertion {
  kind: AssertionKind;
  op: AssertionOp;
  name?: string;
  path?: string;
  value?: string;
  values?: number[];
  number?: number;
  severity?: Severity;
}

export interface HTTPRequest {
  method: string;
  url: string;
  headers?: Record<string, string>;
  body?: string;
  followRedirects?: boolean;
}

export interface TLSOptions {
  verify?: boolean;
  warnDays?: number;
  failDays?: number;
  minProtocol?: string;
  verifyHostname?: boolean;
}

export interface DNSOptions {
  recordType?: string;
  resolver?: string;
  expectAnyOf?: string[];
}

export interface ICMPOptions {
  count?: number;
  maxLossRatio?: number;
  rttWarnMs?: number;
}

/** A value a step keeps for later steps as {{name}}; path is the JSON path, header name or regex. */
export interface Extract {
  name: string;
  from: ExtractSource;
  path?: string;
  secret?: boolean;
}

/** One request of a multi-step check. */
export interface Step {
  name: string;
  request: HTTPRequest;
  assertions?: Assertion[];
  extract?: Extract[];
  /** Runs even after an earlier step failed (clean-up). */
  continueOnFailure?: boolean;
  timeoutSeconds?: number;
}

export interface Definition {
  type: CheckType;
  target?: string;
  request?: HTTPRequest;
  assertions?: Assertion[];
  tls?: TLSOptions;
  dns?: DNSOptions;
  icmp?: ICMPOptions;
  timeoutSeconds: number;
  /** Multi-step only. */
  steps?: Step[];
  variables?: Record<string, string>;
  /** Cookies carry between steps unless false. */
  cookies?: boolean;
}

export interface DestinationRef {
  id: string;
  name: string;
  siteId: string;
  projectId: string;
}

export interface SecretState {
  name: string;
  stored: boolean;
}

export type AlertNotify = 'down' | 'degraded';

/** Channels from Obzly settings; none means the monitor sends no alerts. */
export interface CheckAlerts {
  channelIds: string[];
  notify: AlertNotify | '';
  certDays: number;
  repeatSeconds: number;
}

export interface CheckView {
  id: string;
  name: string;
  type: CheckType;
  enabled: boolean;
  /** A path such as "Payments/EU"; empty at the top level. */
  folder: string;
  parentIds: string[];
  alerts: CheckAlerts;
  intervalSeconds: number;
  downLocations: number;
  locations: string[];
  destination: DestinationRef;
  definition: Definition;
  secrets: SecretState[];
  version: number;
  createdAt: string;
  updatedAt: string;
}

export interface CheckRequest {
  name: string;
  enabled: boolean;
  intervalSeconds: number;
  downLocations: number;
  locations: string[];
  destinationId: string;
  definition: Definition;
  folder?: string;
  parentIds?: string[];
  alerts?: CheckAlerts;
  secrets?: Record<string, string>;
  version?: number;
}

export interface Probe {
  id: string;
  name: string;
  location: string;
  description: string;
  version: string;
  lastSeenAt: string | null;
  createdAt: string;
  online: boolean;
  checks: number;
  /** The group's instances heard from in the last 24 h. */
  members: ProbeMember[];
}

export interface ProbeMember {
  name: string;
  version: string;
  checks: number;
  firstSeen: string;
  lastSeenAt: string;
  /** Heard from within the last minute, so it runs its share. */
  live: boolean;
}

export interface ProbeTokenResponse {
  probe: Probe;
  token: string;
}

export interface Destination {
  id: string;
  name: string;
  siteId: string;
  projectId: string;
  otlpUrl: string;
  apiKeyHint: string;
  createdAt: string;
}

export interface DestinationRequest {
  name: string;
  siteId: string;
  projectId: string;
  otlpUrl: string;
  apiKey: string;
}

export interface ImportError {
  line?: number;
  monitor?: string;
  /** The maintenance window the problem is in. */
  window?: string;
  message: string;
}

export interface ImportPreview {
  create: string[];
  update: { name: string; changes: string[] }[];
  unchanged: string[];
  delete: string[];
  secrets: SecretState[];
  errors: ImportError[];
  applied: boolean;
  /** Maintenance windows (only changed when the file has a maintenance list). */
  maintenance?: {
    create: string[];
    update: { name: string; changes: string[] }[];
    unchanged: string[];
    delete: string[];
  };
}

export interface TLSInfo {
  subject: string;
  issuer: string;
  sans?: string[];
  notAfter: string;
  daysLeft: number;
  protocol: string;
  cipher: string;
  verified: boolean;
  verifyError?: string;
  selfSigned?: boolean;
}

export interface AssertionResult {
  description: string;
  severity: Severity;
  passed: boolean;
  actual?: string;
}

export interface RunResult {
  checkId?: string;
  check: string;
  type: CheckType;
  target: string;
  location: string;
  probe: string;
  startedAt: string;
  status: RunStatus;
  error?: string;
  unsupported?: boolean;
  durationMs: number;
  phasesMs?: Record<string, number>;
  httpStatus?: number;
  responseBytes?: number;
  redirects?: number;
  headers?: Record<string, string>;
  bodySnippet?: string;
  resolvedIps?: string[];
  tls?: TLSInfo;
  dnsAnswers?: string[];
  icmp?: { sent: number; received: number; lossRatio: number; rttAvgMs: number; jitterMs: number };
  assertions?: AssertionResult[];
  fromControlPlane?: boolean;
  /** A multi-step run's steps in order, skipped ones too. */
  steps?: StepResult[];
}

export interface StepResult {
  /** Counts from 1. */
  index: number;
  name: string;
  status: StepStatus;
  method: string;
  url: string;
  httpStatus?: number;
  durationMs: number;
  phasesMs?: Record<string, number>;
  responseBytes?: number;
  redirects?: number;
  error?: string;
  /** Kept for steps that didn't pass only. */
  bodySnippet?: string;
  assertions?: AssertionResult[];
  /** A secret value is listed by name only. */
  extracted?: { name: string; value?: string; secret?: boolean }[];
  /** Ran after an earlier step failed (continue on failure). */
  afterFailure?: boolean;
}

export type TestTaskStatus = 'queued' | 'done' | 'no_probe' | 'expired' | 'not_found';

export interface TestRun {
  id: string;
  tasks: {
    id: string;
    location: string;
    probe: string;
    status: TestTaskStatus;
    result?: RunResult;
  }[];
}

export interface TestRequest {
  checkId?: string;
  check: CheckRequest;
  locations?: string[];
  fromControlPlane?: boolean;
}

export interface PromQLPoint {
  timestamp: string;
  value: number;
}

export interface PromQLSeries {
  labels: Record<string, string>;
  points: PromQLPoint[];
}

export interface PromQLResponse {
  resultType: string;
  series: PromQLSeries[];
  scalar?: number;
  stepSeconds?: number;
  /** More series matched than the limit; the rest were dropped. */
  truncated?: boolean;
}

export interface PromQLRequest {
  query: string;
  start?: string;
  end?: string;
  stepSeconds?: number;
  instant?: boolean;
  /** Series limit (older Sites ignore it and stop at 100). */
  maxSeries?: number;
}
