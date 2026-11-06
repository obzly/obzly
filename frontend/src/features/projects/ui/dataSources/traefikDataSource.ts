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

export const traefikDataSource: DataSourceDefinition = {
  id: 'traefik',
  name: 'Traefik',
  category: 'Web Servers',
  description: 'JSON access logs plus request/router metrics via its built-in Prometheus endpoint.',
  fields: [
    {
      key: 'accessLogPath',
      label: 'Access log path',
      defaultValue: '/var/log/traefik/access.log',
      helpText: 'Requires accessLog.format: json in Traefik’s static config.',
    },
    {
      key: 'metricsUrl',
      label: 'Prometheus metrics URL',
      defaultValue: 'http://localhost:8080/metrics',
      helpText: 'Requires metrics.prometheus enabled in Traefik’s static config.',
    },
    OTLP_REMOTE_FIELD,
  ],
  build: ({ baseUrl, apiKey, fieldValues }) => {
    const accessLogPath = fieldValues.accessLogPath.trim() || '/var/log/traefik/access.log';
    const metricsUrl = fieldValues.metricsUrl.trim() || 'http://localhost:8080/metrics';

    const content = `# Obzly OpenTelemetry Collector config for Traefik.
#
# Requires JSON access logs and the Prometheus metrics endpoint enabled in
# Traefik's static config:
#
#   accessLog:
#     format: json
#     filePath: "${accessLogPath}"
#   metrics:
#     prometheus: {}

receivers:
  file_log:
    include: [${accessLogPath}]
    start_at: end
    storage: file_storage
    operators:
      # Traefik's JSON access log already has structured fields - no
      # regex needed, just parse it and promote the useful ones.
      - type: json_parser
        timestamp:
          parse_from: attributes.time
          layout: '%Y-%m-%dT%H:%M:%S%z'
      - type: severity_parser
        parse_from: attributes.DownstreamStatus
        mapping:
          info: 2xx
          info2: 3xx
          warn: 4xx
          error: 5xx

  prometheus:
    config:
      scrape_configs:
        - job_name: 'traefik'
          metrics_path: '/metrics'
          scrape_interval: 30s
          static_configs:
            - targets: ["${metricsUrl.replace(/^https?:\/\//, '').replace(/\/metrics$/, '')}"]

${otlpReceiverYaml(fieldValues)}

processors:
${MEMORY_LIMITER_PROCESSOR_YAML}
${BATCH_PROCESSOR_YAML}
${RESOURCEDETECTION_PROCESSOR_YAML}
  resource:
    attributes:
      - key: service.name
        value: traefik
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
        `Enable JSON access logs and the Prometheus endpoint as shown in the config comment, then run: ` +
        `OBZLY_API_KEY="${apiKey}" otelcol-contrib --config=otel-collector-config.yaml. ` +
        FILE_STORAGE_NOTE,
    };
  },
};
