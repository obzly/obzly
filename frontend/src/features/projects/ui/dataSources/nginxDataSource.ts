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

export const nginxDataSource: DataSourceDefinition = {
  id: 'nginx',
  name: 'Nginx',
  category: 'Web Servers',
  description: 'Access/error logs plus request metrics, via stub_status.',
  fields: [
    {
      key: 'accessLogPath',
      label: 'Access log path',
      defaultValue: '/var/log/nginx/access.log',
    },
    {
      key: 'errorLogPath',
      label: 'Error log path',
      defaultValue: '/var/log/nginx/error.log',
    },
    {
      key: 'stubStatusUrl',
      label: 'stub_status URL (for request-rate/connection metrics)',
      defaultValue: 'http://localhost:8080/nginx_status',
      helpText:
        'Requires an "nginx_status" location block with stub_status on; leave as-is and skip enabling it if you only want logs.',
    },
    OTLP_REMOTE_FIELD,
  ],
  build: ({ baseUrl, apiKey, fieldValues }) => {
    const accessLogPath = fieldValues.accessLogPath.trim() || '/var/log/nginx/access.log';
    const errorLogPath = fieldValues.errorLogPath.trim() || '/var/log/nginx/error.log';
    const stubStatusUrl = fieldValues.stubStatusUrl.trim() || 'http://localhost:8080/nginx_status';

    const content = `# Obzly OpenTelemetry Collector config for Nginx.
#
# Tails Nginx's default combined-format access log and error log, and
# (optionally) scrapes request-rate/connection metrics from stub_status -
# see https://nginx.org/en/docs/http/ngx_http_stub_status_module.html to
# enable it if you haven't already:
#
#   location /nginx_status {
#       stub_status;
#       allow 127.0.0.1;
#       deny all;
#   }

receivers:
  file_log/access:
    include: [${accessLogPath}]
    start_at: end
    storage: file_storage
    operators:
      # Nginx's default "combined" log_format. If you've customized
      # log_format, adjust this regex to match.
      - type: regex_parser
        regex: '^(?P<remote_addr>\\S+) \\S+ (?P<remote_user>\\S+) \\[(?P<time>[^\\]]+)\\] "(?P<method>\\S+) (?P<path>\\S+) (?P<protocol>[^"]+)" (?P<status>\\d+) (?P<body_bytes_sent>\\d+) "(?P<referer>[^"]*)" "(?P<user_agent>[^"]*)"'
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
        regex: '^(?P<time>\\d{4}/\\d{2}/\\d{2} \\d{2}:\\d{2}:\\d{2}) \\[(?P<level>\\w+)\\]'
        timestamp:
          parse_from: attributes.time
          layout: '%Y/%m/%d %H:%M:%S'
      - type: severity_parser
        parse_from: attributes.level
        mapping:
          info: info
          warn: warn
          error: [error, crit, alert, emerg]

  nginx:
    endpoint: "${stubStatusUrl}"
    collection_interval: 30s

${otlpReceiverYaml(fieldValues)}

processors:
${MEMORY_LIMITER_PROCESSOR_YAML}
${BATCH_PROCESSOR_YAML}
${RESOURCEDETECTION_PROCESSOR_YAML}
  resource:
    attributes:
      - key: service.name
        value: nginx
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
      receivers: [nginx, otlp]
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
        `Run with: OBZLY_API_KEY="${apiKey}" otelcol-contrib --config=otel-collector-config.yaml ` +
        `(or as a systemd service/container - same binary as the Linux Server and Docker sources here). ` +
        `Skip enabling stub_status if you only want logs; the metrics pipeline just won't collect anything. ` +
        FILE_STORAGE_NOTE,
    };
  },
};
