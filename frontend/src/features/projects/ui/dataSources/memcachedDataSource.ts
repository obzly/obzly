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

export const memcachedDataSource: DataSourceDefinition = {
  id: 'memcached',
  name: 'Memcached',
  category: 'Databases',
  description: 'Server metrics (connections, hit rate, evictions, bytes) via the stats protocol.',
  fields: [
    { key: 'endpoint', label: 'Endpoint', defaultValue: 'localhost:11211' },
    OTLP_REMOTE_FIELD,
  ],
  build: ({ baseUrl, apiKey, fieldValues }) => {
    const endpoint = fieldValues.endpoint.trim() || 'localhost:11211';

    const content = `# Obzly OpenTelemetry Collector config for Memcached.

receivers:
  memcached:
    endpoint: ${endpoint}
    transport: tcp
    collection_interval: 30s

${otlpReceiverYaml(fieldValues)}

processors:
${MEMORY_LIMITER_PROCESSOR_YAML}
${BATCH_PROCESSOR_YAML}
${RESOURCEDETECTION_PROCESSOR_YAML}
  resource:
    attributes:
      - key: service.name
        value: memcached
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
      receivers: [memcached, otlp]
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
