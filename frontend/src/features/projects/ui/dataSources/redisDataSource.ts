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
import type { DataSourceBuildContext, DataSourceConfigResult, DataSourceDefinition } from './types';

const FIELDS = [
  { key: 'host', label: 'Host', defaultValue: 'localhost' },
  { key: 'port', label: 'Port', defaultValue: '6379' },
  {
    key: 'password',
    label: 'Password (leave blank if none)',
    defaultValue: '',
  },
  OTLP_REMOTE_FIELD,
];

// Redis and Valkey are wire-compatible, so both entries share this builder.
function buildRedisConfig(
  serviceName: string,
  displayName: string,
  { baseUrl, apiKey, fieldValues }: DataSourceBuildContext,
): DataSourceConfigResult {
  const host = fieldValues.host.trim() || 'localhost';
  const port = fieldValues.port.trim() || '6379';
  const password = fieldValues.password.trim();

  const content = `# Obzly OpenTelemetry Collector config for ${displayName}.

receivers:
  redis:
    endpoint: ${host}:${port}
    ${password ? 'password: ${env:OBZLY_DB_PASSWORD}' : '# no password configured'}
    collection_interval: 30s
    tls:
      insecure: true

${otlpReceiverYaml(fieldValues)}

processors:
${MEMORY_LIMITER_PROCESSOR_YAML}
${BATCH_PROCESSOR_YAML}
${RESOURCEDETECTION_PROCESSOR_YAML}
  resource:
    attributes:
      - key: service.name
        value: ${serviceName}
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
      receivers: [redis, otlp]
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
    instructions: password
      ? `Run with: OBZLY_API_KEY="${apiKey}" OBZLY_DB_PASSWORD="${password}" otelcol-contrib --config=otel-collector-config.yaml`
      : `Run with: OBZLY_API_KEY="${apiKey}" otelcol-contrib --config=otel-collector-config.yaml`,
  };
}

export const redisDataSource: DataSourceDefinition = {
  id: 'redis',
  name: 'Redis',
  category: 'Databases',
  description: 'Server metrics (memory, connected clients, ops/sec, keyspace hits) via INFO.',
  fields: FIELDS,
  build: (ctx) => buildRedisConfig('redis', 'Redis', ctx),
};

export const valkeyDataSource: DataSourceDefinition = {
  id: 'valkey',
  name: 'Valkey',
  category: 'Databases',
  description: 'Server metrics (memory, connected clients, ops/sec, keyspace hits) via INFO.',
  fields: FIELDS,
  build: (ctx) => buildRedisConfig('valkey', 'Valkey', ctx),
};
