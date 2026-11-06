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

export const envoyDataSource: DataSourceDefinition = {
  id: 'envoy',
  name: 'Envoy Proxy',
  category: 'Web Servers',
  description: 'JSON access logs plus listener/cluster metrics via the admin stats endpoint.',
  fields: [
    {
      key: 'accessLogPath',
      label: 'Access log path',
      defaultValue: '/var/log/envoy/access.log',
      helpText: 'Requires a file access log with a JSON log_format configured on the listener.',
    },
    {
      key: 'adminStatsUrl',
      label: 'Admin stats endpoint (Prometheus format)',
      defaultValue: 'http://localhost:9901/stats/prometheus',
    },
    OTLP_REMOTE_FIELD,
  ],
  build: ({ baseUrl, apiKey, fieldValues }) => {
    const accessLogPath = fieldValues.accessLogPath.trim() || '/var/log/envoy/access.log';
    const adminStatsUrl =
      fieldValues.adminStatsUrl.trim() || 'http://localhost:9901/stats/prometheus';
    const target = adminStatsUrl.replace(/^https?:\/\//, '').replace(/\/stats\/prometheus$/, '');

    const content = `# Obzly OpenTelemetry Collector config for Envoy Proxy.
#
# Requires a JSON-formatted file access log configured on the listener,
# e.g.:
#   access_log:
#     - name: envoy.access_loggers.file
#       typed_config:
#         "@type": type.googleapis.com/envoy.extensions.access_loggers.file.v3.FileAccessLog
#         path: "${accessLogPath}"
#         log_format:
#           json_format:
#             time: "%START_TIME%"
#             method: "%REQ(:METHOD)%"
#             path: "%REQ(:PATH)%"
#             status: "%RESPONSE_CODE%"
#             duration_ms: "%DURATION%"

receivers:
  file_log:
    include: [${accessLogPath}]
    start_at: end
    storage: file_storage
    operators:
      - type: json_parser
        timestamp:
          parse_from: attributes.time
          layout: '%Y-%m-%dT%H:%M:%S.%fZ'
      - type: severity_parser
        parse_from: attributes.status
        mapping:
          info: 2xx
          info2: 3xx
          warn: 4xx
          error: 5xx

  prometheus:
    config:
      scrape_configs:
        - job_name: 'envoy'
          metrics_path: '/stats/prometheus'
          scrape_interval: 30s
          static_configs:
            - targets: ["${target}"]

${otlpReceiverYaml(fieldValues)}

processors:
${MEMORY_LIMITER_PROCESSOR_YAML}
${BATCH_PROCESSOR_YAML}
${RESOURCEDETECTION_PROCESSOR_YAML}
  resource:
    attributes:
      - key: service.name
        value: envoy
        action: upsert

${extensionsYaml({ fileStorageDir: FILE_STORAGE_DIR })}

exporters:
${otlpHttpExporterYaml(baseUrl)}

service:
${serviceExtensionsYaml(true)}
  pipelines:
    logs:
      receivers: [file_log, otlp]
      processors: [memory_limiter, resource_detection, resource, batch]
      exporters: [otlp_http]
    metrics:
      receivers: [prometheus, otlp]
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
        `Configure the JSON access log as shown in the config comment (Envoy's admin stats endpoint ` +
        `is on by default), then run: OBZLY_API_KEY="${apiKey}" otelcol-contrib --config=otel-collector-config.yaml. ` +
        FILE_STORAGE_NOTE,
    };
  },
};
