// The service graph merged from spans and network data.
export type ServiceMapSource = 'spans' | 'network';

export type ServiceMapNodeType = 'service' | 'database' | 'cache' | 'messaging' | 'external';
export type ServiceMapHealth = 'ok' | 'degraded' | 'failing';

export interface ServiceMapNode {
  name: string;
  sources: ServiceMapSource[];
  // From spans only, and only with k8sattributes enrichment.
  namespace?: string;
  deployment?: string;
  node?: string;
  // Absent when unknown (network-only nodes, the "other" bucket).
  type?: ServiceMapNodeType;
  // RED over the window: a service's entry spans, or the client spans
  // calling an untraced peer. Absent for network-only nodes.
  requests?: number;
  errors?: number;
  // 0..1; absent with no requests.
  errorRate?: number;
  p95LatencyMs?: number;
  // Absent with no requests (and on "other").
  health?: ServiceMapHealth;
}

export interface ServiceMapEdge {
  src: string;
  dst: string;
  sources: ServiceMapSource[];
  // Present only on spans-sourced edges.
  callCount?: number;
  errorRate?: number;
  // Call-count-weighted average span duration, ms.
  avgLatencyMs?: number;
  // Present only on network-sourced edges.
  bytesTotal?: number;
}

export interface ServiceMapResponse {
  nodes: ServiceMapNode[];
  edges: ServiceMapEdge[];
  // The window after the backend's clamp, for rates.
  windowSeconds?: number;
}
