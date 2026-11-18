import type {
  IngestionHealth,
  IngestionSignalStatus,
  IngestionSource,
} from '../../../../entity/projects';

export const HEALTH_LABEL: Record<IngestionHealth, string> = {
  sending: 'Sending',
  quiet: 'Quiet',
  stopped: 'Stopped',
};

const SIGNAL_LABEL: Record<IngestionSignalStatus['signal'], string> = {
  logs: 'Logs',
  traces: 'Traces',
  metrics: 'Metrics',
};

export const formatAgo = (iso: string, now: number): string => {
  const seconds = Math.max(0, Math.round((now - new Date(iso).getTime()) / 1000));
  if (seconds < 60) return `${seconds} s ago`;
  const minutes = Math.round(seconds / 60);
  if (minutes < 60) return `${minutes} min ago`;
  const hours = Math.round(minutes / 60);
  if (hours < 48) return `${hours} h ago`;
  return `${Math.round(hours / 24)} days ago`;
};

export const formatRate = (perMinute: number): string => {
  if (perMinute >= 1_000_000) return `${(perMinute / 1_000_000).toFixed(1)}M/min`;
  if (perMinute >= 10_000) return `${Math.round(perMinute / 1000)}k/min`;
  if (perMinute >= 1000) return `${(perMinute / 1000).toFixed(1)}k/min`;
  return `${perMinute >= 10 ? Math.round(perMinute) : perMinute}/min`;
};

export const locationText = (source: IngestionSource): string => {
  const { host, cluster, namespace, workload, serviceName, serviceNamespace } = source.location;
  const parts: string[] = [];
  if (workload) {
    parts.push([cluster, namespace, workload].filter(Boolean).join(' / '));
  } else if (cluster && source.identifiedBy === 'k8s_cluster') {
    parts.push(`cluster ${cluster}`);
  }
  if (serviceName && serviceName !== source.name) {
    parts.push(serviceNamespace ? `${serviceNamespace}/${serviceName}` : serviceName);
  } else if (serviceNamespace && source.identifiedBy === 'service') {
    parts.push(`namespace ${serviceNamespace}`);
  }
  if (host && host !== source.name) parts.push(host);
  return parts.join(' · ');
};

export const signalChip = (signal: IngestionSignalStatus): string => {
  const label = SIGNAL_LABEL[signal.signal];
  if (signal.health !== 'sending') return `${label} · ${HEALTH_LABEL[signal.health].toLowerCase()}`;
  return signal.recentCount > 0 ? `${label} ${formatRate(signal.perMinute)}` : label;
};
