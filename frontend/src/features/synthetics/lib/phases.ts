export const PHASES: { key: string; label: string; color: string }[] = [
  { key: 'dns', label: 'DNS', color: '#94a3b8' },
  { key: 'connect', label: 'Connect', color: '#0ea5e9' },
  { key: 'tls', label: 'TLS', color: '#8b5cf6' },
  { key: 'ttfb', label: 'Server (TTFB)', color: '#059669' },
  { key: 'transfer', label: 'Transfer', color: '#f59e0b' },
];

export const PHASE_COLORS = Object.fromEntries(PHASES.map((phase) => [phase.key, phase.color]));
export const PHASE_LABELS = Object.fromEntries(PHASES.map((phase) => [phase.key, phase.label]));
