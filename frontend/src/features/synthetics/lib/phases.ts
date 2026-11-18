export const PHASES: { key: string; label: string }[] = [
  { key: 'dns', label: 'DNS' },
  { key: 'connect', label: 'Connect' },
  { key: 'tls', label: 'TLS' },
  { key: 'ttfb', label: 'Server (TTFB)' },
  { key: 'transfer', label: 'Transfer' },
];

export const PHASE_LABELS = Object.fromEntries(PHASES.map((phase) => [phase.key, phase.label]));

/** A phase's colour from the theme's series colours, the same in charts, bars and legends. */
export const phaseColor = (palette: string[], key: string): string => {
  const index = Math.max(
    0,
    PHASES.findIndex((phase) => phase.key === key),
  );
  return palette[index % palette.length];
};
