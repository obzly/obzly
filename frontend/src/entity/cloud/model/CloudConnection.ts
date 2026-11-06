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

export interface CloudScan {
  /** inventory: a full discovery (or Test access); health: a re-read of what it found. */
  kind?: 'inventory' | 'health';
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
  enabled: boolean;
  status: { scan?: CloudScan; health?: CloudScan; reportedAt?: string };
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
