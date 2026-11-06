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

export const apacheDataSource: DataSourceDefinition = {
  id: 'apache',
  name: 'Apache HTTP Server',
  category: 'Web Servers',
  description: 'Access/error logs plus worker/request metrics, via mod_status.',
  fields: [
    {
      key: 'accessLogPath',
      label: 'Access log path',
      defaultValue: '/var/log/apache2/access.log',
    },
    {
      key: 'errorLogPath',
      label: 'Error log path',
      defaultValue: '/var/log/apache2/error.log',
    },
    {
      key: 'statusUrl',
      label: 'mod_status URL (for worker/request metrics)',
      defaultValue: 'http://localhost/server-status?auto',
      helpText:
        'Requires mod_status enabled with an ExtendedStatus/server-status location; leave as-is and skip enabling it if you only want logs.',
    },
    OTLP_REMOTE_FIELD,
  ],
  build: ({ baseUrl, apiKey, fieldValues }) => {
    const accessLogPath = fieldValues.accessLogPath.trim() || '/var/log/apache2/access.log';
    const errorLogPath = fieldValues.errorLogPath.trim() || '/var/log/apache2/error.log';
    const statusUrl = fieldValues.statusUrl.trim() || 'http://localhost/server-status?auto';

    const content = `# Obzly OpenTelemetry Collector config for Apache HTTP Server.
#
# Tails the default "combined" access log and the error log, and
# (optionally) scrapes worker/request metrics from mod_status - enable it
# if you haven't already:
#
#   <Location "/server-status">
#       SetHandler server-status
#       Require local
#   </Location>

receivers:
  file_log/access:
    include: [${accessLogPath}]
    start_at: end
    storage: file_storage
    operators:
      - type: regex_parser
        regex: '^(?P<remote_addr>\\S+) \\S+ (?P<remote_user>\\S+) \\[(?P<time>[^\\]]+)\\] "(?P<method>\\S+) (?P<path>\\S+) (?P<protocol>[^"]+)" (?P<status>\\d+) (?P<bytes_sent>\\S+)'
        timestamp:
          parse_from: attributes.time
          layout: '%d/%b/%Y:%H:%M:%S %z'
      - type: severity_parser
        parse_from: attributes.status
        mapping:
          info: 2xx
          info2: 3xx
          warn: 4xx
          error: 5xx

  file_log/error:
    include: [${errorLogPath}]
    start_at: end
    storage: file_storage
    operators:
      - type: regex_parser
        regex: '^\\[(?P<time>[^\\]]+)\\] \\[[^:]+:(?P<level>\\w+)\\]'
        timestamp:
          parse_from: attributes.time
          layout: '%a %b %d %H:%M:%S.%f %Y'
      - type: severity_parser
        parse_from: attributes.level
        mapping:
          info: [info, notice]
          warn: warn
          error: [error, crit, alert, emerg]

  apache:
    endpoint: "${statusUrl}"
    collection_interval: 30s

${otlpReceiverYaml(fieldValues)}

processors:
${MEMORY_LIMITER_PROCESSOR_YAML}
${BATCH_PROCESSOR_YAML}
${RESOURCEDETECTION_PROCESSOR_YAML}
  resource:
    attributes:
      - key: service.name
        value: apache
        action: upsert

${extensionsYaml({ fileStorageDir: FILE_STORAGE_DIR })}

exporters:
${otlpHttpExporterYaml(baseUrl)}

service:
${serviceExtensionsYaml(true)}
  pipelines:
    logs:
      receivers: [file_log/access, file_log/error, otlp]
      processors: [memory_limiter, resource_detection, resource, batch]
      exporters: [otlp_http]
    metrics:
      receivers: [apache, otlp]
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
        `Run with: OBZLY_API_KEY="${apiKey}" otelcol-contrib --config=otel-collector-config.yaml. ` +
        `Skip enabling mod_status if you only want logs; the metrics pipeline just won't collect anything. ` +
        FILE_STORAGE_NOTE,
    };
  },
};
