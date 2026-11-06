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

export const consulDataSource: DataSourceDefinition = {
  id: 'consul',
  name: 'Consul',
  category: 'Infrastructure',
  description:
    'Agent/cluster metrics (raft, catalog, health checks) via its native Prometheus endpoint.',
  fields: [
    { key: 'endpoint', label: 'Consul HTTP API address', defaultValue: 'localhost:8500' },
    { key: 'aclToken', label: 'ACL token (leave blank if ACLs disabled)', defaultValue: '' },
    OTLP_REMOTE_FIELD,
  ],
  build: ({ baseUrl, apiKey, fieldValues }) => {
    const endpoint = fieldValues.endpoint.trim() || 'localhost:8500';
    const aclToken = fieldValues.aclToken.trim();
    const headersBlock = aclToken
      ? `          http_headers:\n            X-Consul-Token:\n              values: ['\${env:OBZLY_CONSUL_TOKEN}']\n`
      : '';

    const content = `# Obzly OpenTelemetry Collector config for Consul.
#
# Consul exposes Prometheus-format metrics natively - requires
# telemetry.prometheus_retention_time set to a non-zero duration in its
# own config (e.g. "24h") if you haven't already enabled it.

receivers:
  prometheus:
    config:
      scrape_configs:
        - job_name: 'consul'
          metrics_path: '/v1/agent/metrics'
          params:
            format: ['prometheus']
          scrape_interval: 30s
${headersBlock}          static_configs:
            - targets: ["${endpoint}"]

${otlpReceiverYaml(fieldValues)}

processors:
${MEMORY_LIMITER_PROCESSOR_YAML}
${BATCH_PROCESSOR_YAML}
${RESOURCEDETECTION_PROCESSOR_YAML}
  resource:
    attributes:
      - key: service.name
        value: consul
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
      instructions: aclToken
        ? `Run with: OBZLY_API_KEY="${apiKey}" OBZLY_CONSUL_TOKEN="${aclToken}" otelcol-contrib --config=otel-collector-config.yaml`
        : `Run with: OBZLY_API_KEY="${apiKey}" otelcol-contrib --config=otel-collector-config.yaml`,
    };
  },
};
