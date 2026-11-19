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
  /** How many assets sit right under this one: only with childCounts. */
  childCount?: number;
}

export interface AssetList {
  /** The page from offset, at most limit. */
  assets: Asset[];
  /** How many matched. */
  total: number;
  /** Only with ancestors: the parents, up to the top, of assets that aren't among them. */
  ancestors?: Asset[];
}

export type AssetSort =
  | 'health'
  | 'kind'
  | 'name'
  | 'cpu'
  | 'memory'
  | 'restarts'
  | 'age'
  | 'lastSeen'
  /** Worst health, then leaves (pods) before the parents repeating their reason. */
  | 'attention';

/** GET /inventory/assets parameters. */
export interface AssetQuery {
  projectId?: string;
  kind?: AssetKind[];
  health?: AssetHealth[];
  state?: AssetState[];
  cluster?: string;
  namespace?: string;
  /** Only the assets right under this key (with projectId). */
  parent?: string;
  /** Only the assets without a parent in the inventory. */
  roots?: boolean;
  /** Name, node, attribute values and project name. */
  q?: string;
  sort?: AssetSort;
  /** asc: worst health, newest, least first; desc reverses. */
  order?: 'asc' | 'desc';
  offset?: number;
  /** Default 2000, max 5000. */
  limit?: number;
  childCounts?: boolean;
  ancestors?: boolean;
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
  /** The asset's id while it is still in the inventory (Sites from v1.2.2). */
  assetId?: string;
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
  projects: {
    id: string;
    name: string;
    assets: number;
    /** Assets not terminated, by health. */
    live?: Partial<Record<AssetHealth, number>>;
  }[];
  /** Assets not terminated, by health (Sites from v1.2.2, like regions and clusters). */
  live?: Partial<Record<AssetHealth, number>>;
  /** Live assets reporting a cloud region, pods and containers aside; most assets first. */
  regions?: RegionCount[];
  /** Names of the cluster assets. */
  clusters?: string[];
  /** Only with history: true (and from Sites that keep it). */
  history?: InventoryHistory;
}

export interface RegionCount {
  /** cloud_provider, '' when not reported. */
  provider: string;
  region: string;
  assets: number;
  warning: number;
  critical: number;
  /** Names of the projects with assets there. */
  projects: string[];
}

/** Hourly counts the inventory builder keeps (30 days; the summary returns 7). */
export interface InventoryHistory {
  /** Hours with counts, oldest first (ISO). */
  hours: string[];
  /** Each kind's assets (every health) at each of hours. */
  kinds: Partial<Record<AssetKind, number[]>>;
  /** The newest hour at least 24 h old; absent when none is recent enough to compare with. */
  previousAt?: string;
  /** Assets by kind, then health, at previousAt. */
  previous?: Partial<Record<AssetKind, Partial<Record<AssetHealth, number>>>>;
}

/** Where an asset came from: on a control plane, one of several Sites. */
export interface SiteRef {
  /** '' on a standalone Site. */
  siteId: string;
  siteName: string;
}

export type SiteAsset = Asset & SiteRef;
export type SiteChange = AssetChange & SiteRef;
