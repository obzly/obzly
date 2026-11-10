// Identifiers only, never keys.
export type CloudProvider = 'aws' | 'azure';
export type CloudScope = 'account' | 'organization' | 'subscription' | 'management_group';
/** probe: the probe's own identity; role: an AWS role with an external ID; workload: an Azure
 * identity trusting the probe's service account; federated: the control plane's token. */
export type CloudAccess = 'probe' | 'role' | 'workload' | 'federated';
export type CloudUse = 'inventory' | 'cost' | 'posture';
export type UseState = 'ok' | 'needs_permission' | 'error';

export interface UseResult {
  state: UseState;
  message?: string;
  missing?: string[];
}

/** auto: the FOCUS export once it has data, the billing API until then; export or api only. */
export type CostSource = 'auto' | 'export' | 'api';
/** account: this account or subscription; billing: the management account or billing scope, for
 * every member; via: another connection reads this one's cost. */
export type CostScope = 'account' | 'billing' | 'via';

export interface CostExport {
  /** AWS: the S3 bucket and its region. */
  bucket?: string;
  region?: string;
  /** Azure: the storage account and container. */
  storageAccount?: string;
  container?: string;
  prefix?: string;
}

export interface CostSettings {
  source: CostSource;
  scope: CostScope;
  viaConnectionId?: string;
  export?: CostExport;
  refreshHours?: number;
  /** Set by the control plane from its tracked tags. */
  trackedTags?: string[];
}

export interface CostScanInfo {
  source: 'export' | 'api' | '';
  finalThrough?: string;
  from?: string;
  to?: string;
  rows: number;
  lastFile?: string;
  lastFileAt?: string;
  requests?: number;
}

/** auto: native findings where the service is on, the Obzly baseline elsewhere. */
export type PostureSourceChoice = 'auto' | 'native' | 'baseline';

export interface PostureSettings {
  source: PostureSourceChoice;
  refreshHours?: number;
}

export interface PostureScanInfo {
  findings: number;
  /** account/region scopes each source covered; notCovered: neither did. */
  native?: string[];
  baseline?: string[];
  notCovered?: string[];
}

export interface CloudCostSettings {
  trackedTags: string[];
}

export interface CloudScan {
  /** inventory: a full discovery (or Test access); health: a re-read of what it found; cost: a
   * cost refresh. */
  kind?: 'inventory' | 'health' | 'cost' | 'posture';
  uses: Record<string, UseResult>;
  resources: number;
  types?: Record<string, number>;
  regions?: string[];
  errors?: string[];
  probe?: string;
  startedAt: string;
  durationSeconds: number;
  /** Calls the cloud answered "slow down", and the seconds spent waiting as it asked. */
  throttled?: number;
  stoodDownSeconds?: number;
  /** Stopped at its time limit; what it found was still sent. */
  partial?: boolean;
  cost?: CostScanInfo;
  posture?: PostureScanInfo;
}

export interface CloudConnection {
  id: string;
  name: string;
  provider: CloudProvider;
  scope: CloudScope;
  scopeId: string;
  access: CloudAccess;
  identity: string;
  externalId: string;
  tenantId: string;
  probe: { id: string; name: string; location: string };
  destination: { id: string; name: string; siteId: string; projectId: string };
  scanTypes: CloudUse[];
  regions: string[];
  resourceTypes: string[];
  /** Seconds between full discoveries, and between health scans of what they found. */
  intervalSeconds: number;
  healthIntervalSeconds: number;
  cost?: CostSettings;
  posture?: PostureSettings;
  enabled: boolean;
  status: {
    scan?: CloudScan;
    health?: CloudScan;
    cost?: CloudScan;
    posture?: CloudScan;
    reportedAt?: string;
  };
  version: number;
  createdAt: string;
  updatedAt: string;
}

export interface CloudConnectionRequest {
  name: string;
  provider: CloudProvider;
  scope: CloudScope;
  scopeId: string;
  access: CloudAccess;
  identity: string;
  externalId: string;
  tenantId: string;
  probeId: string;
  destinationId: string;
  scanTypes: CloudUse[];
  regions: string[];
  resourceTypes: string[];
  intervalSeconds: number;
  healthIntervalSeconds: number;
  cost?: CostSettings;
  posture?: PostureSettings;
  enabled?: boolean;
  version?: number;
}

export interface CloudSetup {
  /** The control plane's URL: the OIDC issuer federated connections trust. */
  issuer: string;
  externalId: string;
  audiences: Record<CloudProvider, string>;
}

export interface CloudTest {
  id: string;
  status: 'queued' | 'done';
  probe: string;
  result?: CloudScan;
}
