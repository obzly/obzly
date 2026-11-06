import dayjs from 'dayjs';

// Shared by the sessions list and detail view so a session reads the same in both.
export const formatWindow = (windowStart: string, windowEnd: string): string => {
  const start = dayjs(windowStart);
  const end = dayjs(windowEnd);
  const sameDay = start.isSame(end, 'day');
  const startFormat = 'YYYY-MM-DD HH:mm:ss';
  const endFormat = sameDay ? 'HH:mm:ss' : 'YYYY-MM-DD HH:mm:ss';
  return `${start.format(startFormat)} – ${end.format(endFormat)}`;
};

const TRIGGER_LABELS: Record<string, string> = {
  error_log: 'Error log',
  span_error_status: 'Span error',
  trace_id_link: 'Trace link',
  metric_threshold: 'Metric threshold',
};

// trigger_type is a plain string on the wire; unknown values show as-is.
export const triggerTypeLabel = (triggerType: string): string =>
  TRIGGER_LABELS[triggerType] ?? triggerType;

// trace_id_link is a structural correlation, not an anomaly, hence blue.
const TRIGGER_COLORS: Record<string, string> = {
  error_log: 'red',
  span_error_status: 'orange',
  trace_id_link: 'blue',
  metric_threshold: 'default',
};

export const triggerTypeColor = (triggerType: string): string =>
  TRIGGER_COLORS[triggerType] ?? 'default';
