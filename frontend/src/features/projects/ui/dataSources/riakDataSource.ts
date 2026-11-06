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

export const riakDataSource: DataSourceDefinition = {
  id: 'riak',
  name: 'Riak',
  category: 'Databases',
  description: 'Node/vnode metrics (latency, gets/puts, ring status) via the HTTP stats endpoint.',
  fields: [
    { key: 'endpoint', label: 'Endpoint', defaultValue: 'http://localhost:8098' },
    { key: 'username', label: 'Username', defaultValue: 'otel_monitor' },
    {
      key: 'password',
      label: 'Password',
      defaultValue: '',
      placeholder: 'set this to a real password before running the collector',
    },
    OTLP_REMOTE_FIELD,
  ],
  build: ({ baseUrl, apiKey, fieldValues }) => {
    const endpoint = fieldValues.endpoint.trim() || 'http://localhost:8098';
    const username = fieldValues.username.trim() || 'otel_monitor';
    const password = fieldValues.password.trim() || 'CHANGE_ME';

    const content = `# Obzly OpenTelemetry Collector config for Riak.

receivers:
  riak:
    endpoint: ${endpoint}
    username: ${username}
    password: \${env:OBZLY_RIAK_PASSWORD}
    collection_interval: 30s

${otlpReceiverYaml(fieldValues)}

processors:
${MEMORY_LIMITER_PROCESSOR_YAML}
${BATCH_PROCESSOR_YAML}
${RESOURCEDETECTION_PROCESSOR_YAML}
  resource:
    attributes:
      - key: service.name
        value: riak
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
      receivers: [riak, otlp]
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
        `Run with: OBZLY_API_KEY="${apiKey}" OBZLY_RIAK_PASSWORD="${password}" ` +
        `otelcol-contrib --config=otel-collector-config.yaml`,
    };
  },
};
