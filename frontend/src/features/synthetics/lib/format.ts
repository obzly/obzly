export const formatMs = (ms?: number): string => {
  if (ms === undefined || Number.isNaN(ms)) return '—';
  if (ms >= 10_000) return `${(ms / 1000).toFixed(1)} s`;
  if (ms >= 1000) return `${(ms / 1000).toFixed(2)} s`;
  return `${Math.round(ms)} ms`;
};

export const formatPercent = (ratio?: number, digits = 1): string =>
  ratio === undefined || Number.isNaN(ratio) ? '—' : `${(ratio * 100).toFixed(digits)}%`;

export const formatInterval = (seconds: number): string => {
  if (seconds % 3600 === 0) return `${seconds / 3600} h`;
  if (seconds >= 120 && seconds % 60 === 0) return `${seconds / 60} min`;
  return `${seconds} s`;
};

export const targetOf = (definition: {
  request?: { method: string; url: string };
  target?: string;
  steps?: { request: { method: string; url: string } }[];
}): string => {
  if (definition.request) return `${definition.request.method} ${definition.request.url}`;
  const steps = definition.steps ?? [];
  if (steps.length) {
    const first = steps[0].request;
    return `${steps.length} step${steps.length > 1 ? 's' : ''} · ${first.method} ${first.url}`;
  }
  return definition.target ?? '';
};
