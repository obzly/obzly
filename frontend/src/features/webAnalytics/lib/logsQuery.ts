// Builds the Logs search that "Open in Logs" runs: the selected sources'
// identity attributes AND the clicked value, matched under every name the
// supported web servers log it as (the backend's schema aliases).
import type { WebAnalyticsSource } from '../../../entity/webAnalytics';

export const FIELD_ALIASES = {
  path: ['url_path', 'path', 'RequestPath', 'cs_uri_stem', 'request_path', 'request_uri'],
  status: ['http_response_status_code', 'status', 'response_code', 'DownstreamStatus', 'sc_status'],
  method: ['http_request_method', 'method', 'RequestMethod', 'cs_method', 'request_method'],
  client: ['client_address', 'remote_addr', 'downstream_remote_address', 'ClientHost', 'c_ip'],
  referrer: ['http_request_header_referer', 'referer', 'referrer', 'request_Referer'],
  userAgent: ['user_agent_original', 'user_agent', 'request_User_Agent', 'cs_user_agent'],
  host: ['server_address', 'http_request_header_host', 'host', 'authority', 'RequestHost'],
  protocol: ['network_protocol_version', 'protocol', 'RequestProtocol'],
  tls: ['tls_protocol_version', 'tls_version', 'ssl_protocol', 'TLSVersion'],
} as const;

export type LogsField = keyof typeof FIELD_ALIASES;

export type LogsTarget =
  | { kind: 'all' }
  | { kind: 'equals'; field: LogsField; value: string }
  | { kind: 'contains'; field: LogsField; value: string }
  | { kind: 'atLeast'; field: LogsField; value: number };

/** A search value, quoted when it has spaces, quotes or operators. */
export const quoteValue = (value: string): string =>
  value === '' || /[\s"():=<>!]/.test(value)
    ? `"${value.replace(/\\/g, '\\\\').replace(/"/g, '\\"')}"`
    : value;

/** One source's identity as an AND of its attributes. */
export const sourceClause = (source: WebAnalyticsSource): string =>
  source.identity.map((a) => `${a.name}=${quoteValue(a.value)}`).join(' AND ');

/** The value clause: any of the field's names, plus the text of a raw access-log line for paths and statuses. */
export const targetClause = (target: LogsTarget): string => {
  if (target.kind === 'all') {
    return `(${FIELD_ALIASES.status.map((name) => `${name}:*`).join(' OR ')} OR "HTTP/")`;
  }
  const names = FIELD_ALIASES[target.field];
  if (target.kind === 'atLeast') {
    return `(${names.map((name) => `${name}>=${target.value}`).join(' OR ')})`;
  }
  // Grouped paths (/catalog/:id) match by their fixed start.
  let value = target.value;
  let kind: 'equals' | 'contains' = target.kind;
  if (target.field === 'path' && value.includes(':id')) {
    value = value.slice(0, value.indexOf(':id'));
    kind = 'contains';
  }
  const op = kind === 'equals' ? '=' : ':';
  const parts: string[] = names.map((name) => `${name}${op}${quoteValue(value)}`);
  // Container logs keep the line as text.
  if (target.field === 'path') parts.push(quoteValue(` ${value}${kind === 'equals' ? ' ' : ''}`));
  if (target.field === 'status') parts.push(quoteValue(`" ${value} `));
  return `(${parts.join(' OR ')})`;
};

/**
 * The full search: the clicked value, scoped to the selected sources when
 * the report isn't built from all of them.
 */
export const logsQueryFor = (
  target: LogsTarget,
  selected: WebAnalyticsSource[],
  allSelected: boolean,
): string => {
  const value = targetClause(target);
  const scoped = selected.filter((s) => s.identity.length > 0);
  if (allSelected || scoped.length === 0) return value;
  const sources = scoped.map((s) => {
    const clause = sourceClause(s);
    return scoped.length > 1 && s.identity.length > 1 ? `(${clause})` : clause;
  });
  const scope = sources.length > 1 ? `(${sources.join(' OR ')})` : sources[0];
  return `${scope} AND ${value}`;
};

/** The Logs time preset closest to a Web analytics range (Logs presets stop at 72 h). */
export const logsPresetFor = (rangeMs: number): string => {
  const presets: [number, string][] = [
    [15 * 60_000, '15m'],
    [3_600_000, '1h'],
    [86_400_000, '24h'],
    [72 * 3_600_000, '72h'],
  ];
  return (presets.find(([ms]) => rangeMs <= ms) ?? presets[presets.length - 1])[1];
};
