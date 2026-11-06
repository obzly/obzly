import {
  BATCH_PROCESSOR_YAML,
  MEMORY_LIMITER_PROCESSOR_YAML,
  OTLP_REMOTE_FIELD,
  RESOURCEDETECTION_PROCESSOR_YAML,
  extensionsYaml,
  otlpHttpExporterYaml,
  otlpReceiverYaml,
  serviceExtensionsYaml,
} from './otelCollectorCommon';
import type { DataSourceDefinition } from './types';

export const etcdDataSource: DataSourceDefinition = {
  id: 'etcd',
  name: 'etcd',
  category: 'Infrastructure',
  description:
    'Cluster metrics (leader changes, proposal latency, DB size) via its native Prometheus endpoint.',
  fields: [
    { key: 'endpoint', label: 'etcd client address', defaultValue: 'localhost:2379' },
    OTLP_REMOTE_FIELD,
  ],
  build: ({ baseUrl, apiKey, fieldValues }) => {
    const endpoint = fieldValues.endpoint.trim() || 'localhost:2379';

    const content = `# Obzly OpenTelemetry Collector config for etcd.
#
# etcd exposes Prometheus-format metrics natively on its client port, no
# extra configuration needed on the etcd side.

receivers:
  prometheus:
    config:
      scrape_configs:
        - job_name: 'etcd'
          metrics_path: '/metrics'
          scrape_interval: 30s
          static_configs:
            - targets: ["${endpoint}"]

${otlpReceiverYaml(fieldValues)}

processors:
${MEMORY_LIMITER_PROCESSOR_YAML}
${BATCH_PROCESSOR_YAML}
${RESOURCEDETECTION_PROCESSOR_YAML}
  resource:
    attributes:
      - key: service.name
        value: etcd
        action: upsert

${extensionsYaml()}

exporters:
${otlpHttpExporterYaml(baseUrl)}

service:
${serviceExtensionsYaml()}
  pipelines:
    logs:
      receivers: [otlp]
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
      instructions: `Run with: OBZLY_API_KEY="${apiKey}" otelcol-contrib --config=otel-collector-config.yaml`,
    };
  },
};
