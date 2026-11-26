import type {
  IngestionHealth,
  IngestionRefusal,
  IngestionSignalStatus,
  IngestionSource,
} from '../../../../entity/projects';

export const HEALTH_LABEL: Record<IngestionHealth, string> = {
  sending: 'Sending',
  quiet: 'Quiet',
  stopped: 'Stopped',
};

// Refusal reasons are the backend's error codes (logs_core); unknown ones fall back to the code.
const REFUSAL_LABEL: Record<string, string> = {
  RATE_LIMIT_EXCEEDED: "Over the project's logs per second limit",
  INGEST_BUFFER_FULL: 'Ingest buffer full',
  INGEST_UNAVAILABLE: 'Ingest briefly unavailable',
  LOG_TOO_LARGE: 'Log larger than the size limit',
  INVALID_LOG_LEVEL: 'Unknown log level',
  MESSAGE_EMPTY: 'Empty message',
  FUTURE_TIMESTAMP: 'Timestamp in the future',
  BATCH_TOO_LARGE: 'Request over the size limit',
  PROJECT_QUOTA_EXCEEDED: 'Project quota reached',
  API_KEY_INVALID: 'Invalid API key',
  API_KEY_REQUIRED: 'Missing API key',
  IP_NOT_ALLOWED: 'Sender IP not allowed',
  DOMAIN_NOT_ALLOWED: 'Origin domain not allowed',
  OTHER: 'Other validation errors',
};

export const refusalLabel = (reason: string): string => REFUSAL_LABEL[reason] ?? reason;

/** Lines the senders dropped (not told to retry), the ones that are lost. */
export const droppedCount = (refused: IngestionRefusal[]): number =>
  refused.filter((r) => !r.retried).reduce((sum, r) => sum + r.count, 0);

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

const RECORD_NOUN: Record<IngestionSignalStatus['signal'], string> = {
  logs: 'log lines',
  traces: 'spans',
  metrics: 'data points',
};

export const recordNoun = (signal: IngestionSignalStatus['signal']): string => RECORD_NOUN[signal];

export const signalChip = (signal: IngestionSignalStatus): string => {
  const label = SIGNAL_LABEL[signal.signal];
  if (signal.health !== 'sending') return `${label} · ${HEALTH_LABEL[signal.health].toLowerCase()}`;
  return signal.recentCount > 0 ? `${label} ${formatRate(signal.perMinute)}` : label;
};
