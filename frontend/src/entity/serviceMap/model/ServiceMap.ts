// The service graph merged from spans and network data.
export type ServiceMapSource = 'spans' | 'network';

export interface ServiceMapNode {
  name: string;
  sources: ServiceMapSource[];
  // From spans only, and only with k8sattributes enrichment.
  namespace?: string;
  deployment?: string;
  node?: string;
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
}
