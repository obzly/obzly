import type { QueryNode } from './QueryNode';
import type { TimeRange } from './TimeRange';

// Deployment rollouts in a range.
export interface DeploysRequest {
  query?: QueryNode | null;
  timeRange?: TimeRange;
}

/** One rollout: a new ReplicaSet's pods first logged at time. */
export interface DeployMarker {
  time: string;
  namespace: string;
  workload: string;
  replicaSet: string;
  pods: number;
}

export interface DeploysResponse {
  markers: DeployMarker[];
}
