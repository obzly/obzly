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

export const rabbitmqDataSource: DataSourceDefinition = {
  id: 'rabbitmq',
  name: 'RabbitMQ',
  category: 'Message Queues',
  description: 'Queue/node metrics (message rates, consumers, memory) via the management API.',
  fields: [
    { key: 'endpoint', label: 'Management API endpoint', defaultValue: 'http://localhost:15672' },
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
    const endpoint = fieldValues.endpoint.trim() || 'http://localhost:15672';
    const username = fieldValues.username.trim() || 'otel_monitor';
    const password = fieldValues.password.trim() || 'CHANGE_ME';

    const content = `# Obzly OpenTelemetry Collector config for RabbitMQ.
#
# Requires the management plugin enabled (rabbitmq-plugins enable
# rabbitmq_management) and a user with the "monitoring" tag:
#   rabbitmqctl add_user ${username} <password>
#   rabbitmqctl set_user_tags ${username} monitoring

receivers:
  rabbitmq:
    endpoint: ${endpoint}
    username: ${username}
    password: \${env:OBZLY_DB_PASSWORD}
    collection_interval: 30s

${otlpReceiverYaml(fieldValues)}

processors:
${MEMORY_LIMITER_PROCESSOR_YAML}
${BATCH_PROCESSOR_YAML}
${RESOURCEDETECTION_PROCESSOR_YAML}
  resource:
    attributes:
      - key: service.name
        value: rabbitmq
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
      receivers: [rabbitmq, otlp]
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
        `Create the monitoring user shown in the config comment, then run: ` +
        `OBZLY_API_KEY="${apiKey}" OBZLY_DB_PASSWORD="${password}" otelcol-contrib --config=otel-collector-config.yaml`,
    };
  },
};
