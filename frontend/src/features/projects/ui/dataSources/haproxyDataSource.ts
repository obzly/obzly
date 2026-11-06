import {
  BATCH_PROCESSOR_YAML,
  FILE_STORAGE_DIR,
  FILE_STORAGE_NOTE,
  MEMORY_LIMITER_PROCESSOR_YAML,
  OTLP_REMOTE_FIELD,
  RESOURCEDETECTION_PROCESSOR_YAML,
  extensionsYaml,
  otlpHttpExporterYaml,
  otlpReceiverYaml,
  serviceExtensionsYaml,
} from './otelCollectorCommon';
import type { DataSourceDefinition } from './types';

export const haproxyDataSource: DataSourceDefinition = {
  id: 'haproxy',
  name: 'HAProxy',
  category: 'Web Servers',
  description: 'Frontend/backend metrics (sessions, queue, errors) via the stats page.',
  fields: [
    {
      key: 'statsUrl',
      label: 'Stats page URL (CSV endpoint)',
      defaultValue: 'http://localhost:8404/stats;csv',
      helpText:
        'Requires a stats page enabled, e.g. "stats enable" + "stats uri /stats" in haproxy.cfg.',
    },
    {
      key: 'logPath',
      label: 'Log path (leave blank to skip log tailing)',
      defaultValue: '/var/log/haproxy.log',
      helpText:
        'HAProxy logs to syslog by default - point this at wherever your syslog daemon writes it.',
    },
    OTLP_REMOTE_FIELD,
  ],
  build: ({ baseUrl, apiKey, fieldValues }) => {
    const statsUrl = fieldValues.statsUrl.trim() || 'http://localhost:8404/stats;csv';
    const logPath = fieldValues.logPath.trim();

    const logReceiver = logPath
      ? `  file_log:
    include: [${logPath}]
    start_at: end
    storage: file_storage
    operators:
      - type: regex_parser
        regex: '^(?P<time>\\w+\\s+\\d+ \\d{2}:\\d{2}:\\d{2}) \\S+ haproxy\\[\\d+\\]: .* (?P<status>\\d{3}) '
        timestamp:
          parse_from: attributes.time
          layout: '%b %d %H:%M:%S'
      - type: severity_parser
        parse_from: attributes.status
        mapping:
          info: 2xx
          info2: 3xx
          warn: 4xx
          error: 5xx

`
      : '';

    const content = `# Obzly OpenTelemetry Collector config for HAProxy.
#
# The stats page must be enabled in haproxy.cfg:
#   frontend stats
#       bind *:8404
#       stats enable
#       stats uri /stats
${logPath ? '#\n# HAProxy logs to syslog by default - point logPath at wherever your\n# syslog daemon (rsyslog/syslog-ng) writes the haproxy facility, or\n# switch HAProxy to log directly to a file.\n' : ''}
receivers:
${logReceiver}  haproxy:
    endpoint: "${statsUrl}"
    collection_interval: 30s

${otlpReceiverYaml(fieldValues)}

processors:
${MEMORY_LIMITER_PROCESSOR_YAML}
${BATCH_PROCESSOR_YAML}
${RESOURCEDETECTION_PROCESSOR_YAML}
  resource:
    attributes:
      - key: service.name
        value: haproxy
        action: upsert

${extensionsYaml({ fileStorageDir: logPath ? FILE_STORAGE_DIR : undefined })}

exporters:
${otlpHttpExporterYaml(baseUrl)}

service:
${serviceExtensionsYaml(!!logPath)}
  pipelines:
${logPath ? '    logs:\n      receivers: [file_log, otlp]\n      processors: [memory_limiter, resource_detection, resource, batch]\n      exporters: [otlp_http]\n' : '    logs:\n      receivers: [otlp]\n      processors: [memory_limiter, resource_detection, resource, batch]\n      exporters: [otlp_http]\n'}    metrics:
      receivers: [haproxy, otlp]
      processors: [memory_limiter, resource_detection, resource, batch]
      exporters: [otlp_http]
    traces:
      receivers: [otlp]
      processors: [memory_limiter, resource_detection, resource, batch]
      exporters: [otlp_http]
`;

    return {
      filename: 'otel-collector-config.yaml',
      language: 'yaml',
      content,
      instructions:
        `Run with: OBZLY_API_KEY="${apiKey}" otelcol-contrib --config=otel-collector-config.yaml` +
        (logPath ? `. ${FILE_STORAGE_NOTE}` : ''),
    };
  },
};
