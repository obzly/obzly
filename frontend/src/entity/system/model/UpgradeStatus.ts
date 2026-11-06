export type SchemaState = 'up-to-date' | 'pending' | 'ahead' | 'unknown';

export interface AppliedMigration {
  version: number;
  appliedAt: string;
}

export interface SchemaStatus {
  currentVersion: number;
  latestAvailable: number;
  appliedCount: number;
  state: SchemaState;
  /** Releases recorded with a newer schema; 1 = rollout in progress or supported rollback. */
  releasesAhead: number;
  recent: AppliedMigration[];
}

export interface RewriteStream {
  stream: string;
  legacy: number;
  total: number;
  skipped: number;
}

export interface RewriteStatus {
  completed: { name: string; completedAt: string }[];
  streams: RewriteStream[];
  remaining: number;
  workerOnThisReplica: boolean;
}

export interface StreamBacklog {
  stream: string;
  total: number;
  /** null when object storage is off. */
  pendingUpload: number | null;
  smallSegments: number;
  oldestPendingUploadAt?: string;
}

/** One signal's JetStream ingest buffer: acknowledged, not yet written. */
export interface IngestStreamStatus {
  stream: string;
  messages: number;
  bytes: number;
  maxBytes: number;
  oldestAt?: string;
  replicasLagging: number;
}

/** The weekly check of uploaded segments against object storage. */
export interface IntegrityAudit {
  finishedAt: string;
  sampled: number;
  failed: number;
  failures: {
    segmentId: string;
    stream: string;
    objectKey: string;
    problem: 'missing' | 'size' | 'row-count' | 'unreadable';
    detail?: string;
  }[];
}

export interface UpgradeStatus {
  version: string;
  serviceMode: string;
  checkedAt: string;
  schema: SchemaStatus;
  /** Absent on a control plane, which holds no signal data. */
  rewrites?: RewriteStatus;
  segments?: StreamBacklog[];
  ingestBuffer?: IngestStreamStatus[];
  integrityAudit?: IntegrityAudit;
  objectStorageEnabled: boolean;
  verdict: {
    safeToUpgrade: boolean;
    blockers: string[];
    warnings: string[];
  };
}
