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

export const sqlserverDataSource: DataSourceDefinition = {
  id: 'sqlserver',
  name: 'Microsoft SQL Server',
  category: 'Databases',
  description:
    'Database metrics (batch requests, page life expectancy, locks) via a Windows perf counter or direct connection.',
  fields: [
    { key: 'server', label: 'Server (host or instance name)', defaultValue: 'localhost' },
    { key: 'port', label: 'Port', defaultValue: '1433' },
    { key: 'username', label: 'Monitoring username', defaultValue: 'otel_monitor' },
    {
      key: 'password',
      label: 'Monitoring password',
      defaultValue: '',
      placeholder: 'set this to a real password before running the collector',
    },
    OTLP_REMOTE_FIELD,
  ],
  build: ({ baseUrl, apiKey, fieldValues }) => {
    const server = fieldValues.server.trim() || 'localhost';
    const port = fieldValues.port.trim() || '1433';
    const username = fieldValues.username.trim() || 'otel_monitor';
    const password = fieldValues.password.trim() || 'CHANGE_ME';

    const content = `# Obzly OpenTelemetry Collector config for Microsoft SQL Server.
#
# The monitoring login only needs VIEW SERVER STATE:
#   CREATE LOGIN ${username} WITH PASSWORD = '<password>';
#   GRANT VIEW SERVER STATE TO ${username};
#
# Run this collector on Windows for the fullest metric set (it falls back
# to a direct SQL connection on Linux, which covers the same core metrics
# but not every Windows perf counter).

receivers:
  sqlserver:
    server: ${server}
    port: ${port}
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
        value: sqlserver
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
      receivers: [sqlserver, otlp]
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
        `Create the monitoring login shown in the config comment, then run: ` +
        `OBZLY_API_KEY="${apiKey}" OBZLY_DB_PASSWORD="${password}" otelcol-contrib --config=otel-collector-config.yaml`,
    };
  },
};
