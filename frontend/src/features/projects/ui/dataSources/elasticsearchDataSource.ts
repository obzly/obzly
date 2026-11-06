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

export const elasticsearchDataSource: DataSourceDefinition = {
  id: 'elasticsearch',
  name: 'Elasticsearch',
  category: 'Databases',
  description: 'Cluster/node/index metrics (health, JVM heap, indexing rate) via the cluster API.',
  fields: [
    { key: 'endpoint', label: 'Endpoint', defaultValue: 'http://localhost:9200' },
    { key: 'username', label: 'Username (leave blank if unauthenticated)', defaultValue: '' },
    { key: 'password', label: 'Password', defaultValue: '' },
    OTLP_REMOTE_FIELD,
  ],
  build: ({ baseUrl, apiKey, fieldValues }) => {
    const endpoint = fieldValues.endpoint.trim() || 'http://localhost:9200';
    const username = fieldValues.username.trim();
    const password = fieldValues.password.trim();
    const hasAuth = !!username;

    const content = `# Obzly OpenTelemetry Collector config for Elasticsearch.

receivers:
  elasticsearch:
    endpoint: ${endpoint}
    ${hasAuth ? `username: ${username}\n    password: \${env:OBZLY_DB_PASSWORD}` : '# no authentication configured'}
    collection_interval: 30s
    nodes: ["_all"]
    indices: ["_all"]

${otlpReceiverYaml(fieldValues)}

processors:
${MEMORY_LIMITER_PROCESSOR_YAML}
${BATCH_PROCESSOR_YAML}
${RESOURCEDETECTION_PROCESSOR_YAML}
  resource:
    attributes:
      - key: service.name
        value: elasticsearch
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
      receivers: [elasticsearch, otlp]
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
      instructions: hasAuth
        ? `Run with: OBZLY_API_KEY="${apiKey}" OBZLY_DB_PASSWORD="${password}" otelcol-contrib --config=otel-collector-config.yaml`
        : `Run with: OBZLY_API_KEY="${apiKey}" otelcol-contrib --config=otel-collector-config.yaml`,
    };
  },
};
