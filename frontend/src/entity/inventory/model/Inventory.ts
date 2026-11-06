export type AssetKind =
  | 'cluster'
  | 'node'
  | 'host'
  | 'namespace'
  | 'workload'
  | 'pod'
  | 'container'
  | 'docker_container'
  | 'service'
  // From cloud connections.
  | 'load_balancer'
  | 'database'
  | 'bucket'
  | 'function'
  | 'scaling_group';

export type AssetHealth = 'healthy' | 'warning' | 'critical' | 'inactive' | 'unknown';

export type AssetState = 'new' | 'active' | 'not_reporting' | 'terminated';

export interface AssetReason {
  health: AssetHealth;
  text: string;
}

export interface AssetMetrics {
  cpuPercent?: number;
  memoryPercent?: number;
  diskPercent?: number;
  /** What a pod's or container's percentage is of. */
  cpuBasis?: 'limit' | 'node';
  memoryBasis?: 'limit' | 'node';
  restarts1h?: number;
  restarts24h?: number;
  errorShare?: number;
  desired?: number;
  available?: number;
  phase?: string;
  ready?: boolean;
}

export interface Asset {
  id: string;
  projectId: string;
  projectName: string;
  kind: AssetKind;
  key: string;
  name: string;
  /** Workload kind (Deployment...) or host type (EC2 instance...). */
  subtype: string;
  parentKey: string;
  nodeKey: string;
  cluster: string;
  namespace: string;
  node: string;
  attributes: Record<string, string>;
  sources: string[];
  firstSeen: string;
  lastSeen: string;
  state: AssetState;
  health: AssetHealth;
  reasons: AssetReason[];
  metrics: AssetMetrics;
  updatedAt: string;
}

export interface AssetList {
  assets: Asset[];
  total: number;
}

export interface AssetRef {
  id: string;
  kind: AssetKind;
  subtype: string;
  name: string;
  health: AssetHealth;
}

export interface AssetChange {
  id: string;
  projectId: string;
  projectName?: string;
  assetKey: string;
  assetName: string;
  kind: AssetKind;
  change: 'new' | 'state' | 'health';
  from: string;
  to: string;
  detail: string;
  at: string;
}

export interface AssetDetail {
  asset: Asset;
  parents: AssetRef[];
  node?: AssetRef;
  children: Asset[];
  changes: AssetChange[];
}

export interface InventorySummary {
  total: number;
  kinds: Partial<Record<AssetKind, Partial<Record<AssetHealth, number>>>>;
  health: Partial<Record<AssetHealth, number>>;
  states: Partial<Record<AssetState, number>>;
  projects: { id: string; name: string; assets: number }[];
}

/** Where an asset came from: on a control plane, one of several Sites. */
export interface SiteRef {
  /** '' on a standalone Site. */
  siteId: string;
  siteName: string;
}

export type SiteAsset = Asset & SiteRef;
export type SiteChange = AssetChange & SiteRef;
