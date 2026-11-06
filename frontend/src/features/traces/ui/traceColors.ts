// Hashed, not random, so a service keeps its color across renders and views.
const PALETTE = [
  '#2563eb', // blue
  '#059669', // emerald
  '#d97706', // amber
  '#7c3aed', // violet
  '#db2777', // pink
  '#0891b2', // cyan
  '#65a30d', // lime
  '#dc2626', // red
  '#4f46e5', // indigo
  '#ea580c', // orange
];

export function colorForService(serviceName: string): string {
  let hash = 0;
  for (let i = 0; i < serviceName.length; i++) {
    hash = (hash * 31 + serviceName.charCodeAt(i)) >>> 0;
  }
  return PALETTE[hash % PALETTE.length];
}

export function formatDurationMs(durationMs: number): string {
  if (durationMs < 1) return `${(durationMs * 1000).toFixed(0)}µs`;
  if (durationMs < 1000) return `${durationMs.toFixed(durationMs < 10 ? 2 : 0)}ms`;
  return `${(durationMs / 1000).toFixed(2)}s`;
}
