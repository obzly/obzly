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

export const postgresqlDataSource: DataSourceDefinition = {
  id: 'postgresql',
  name: 'PostgreSQL',
  category: 'Databases',
  description:
    'Database metrics (connections, commits, locks, replication) plus the Postgres log file.',
  fields: [
    { key: 'host', label: 'Host', defaultValue: 'localhost' },
    { key: 'port', label: 'Port', defaultValue: '5432' },
    { key: 'username', label: 'Monitoring username', defaultValue: 'otel_monitor' },
    {
      key: 'password',
      label: 'Monitoring password',
      defaultValue: '',
      placeholder: 'set this to a real password before running the collector',
    },
    {
      key: 'logPath',
      label: 'Log file path (leave blank to skip log tailing)',
      defaultValue: '/var/log/postgresql/postgresql*.log',
    },
    OTLP_REMOTE_FIELD,
  ],
  build: ({ baseUrl, apiKey, fieldValues }) => {
    const host = fieldValues.host.trim() || 'localhost';
    const port = fieldValues.port.trim() || '5432';
    const username = fieldValues.username.trim() || 'otel_monitor';
    const password = fieldValues.password.trim() || 'CHANGE_ME';
    const logPath = fieldValues.logPath.trim();

    const logReceiver = logPath
      ? `  file_log:
    include: [${logPath}]
    start_at: end
    storage: file_storage
    operators:
      - type: regex_parser
        regex: '^(?P<time>\\d{4}-\\d{2}-\\d{2} \\d{2}:\\d{2}:\\d{2}\\.\\d+ \\w+)  \\[\\d+\\] (?P<level>\\w+):'
        timestamp:
          parse_from: attributes.time
          layout: '%Y-%m-%d %H:%M:%S.%f %Z'
      - type: severity_parser
        parse_from: attributes.level
        mapping:
          info: [LOG, STATEMENT, DETAIL, HINT]
          warn: WARNING
          error: [ERROR, FATAL, PANIC]

`
      : '';

    const content = `# Obzly OpenTelemetry Collector config for PostgreSQL.
#
# The postgresql receiver needs a monitoring role that can read
# pg_stat_* views (it never needs write access):
#   CREATE USER ${username} WITH PASSWORD '<password>';
#   GRANT pg_monitor TO ${username};
${logPath ? '#\n# Log tailing expects the default Postgres log line prefix; adjust the\n# regex below if you customized log_line_prefix.\n' : ''}
receivers:
${logReceiver}  postgresql:
    endpoint: ${host}:${port}
    transport: tcp
    username: ${username}
    password: \${env:OBZLY_DB_PASSWORD}
    databases: []  # empty = discover and monitor every database
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
        value: postgresql
        action: upsert

${extensionsYaml({ fileStorageDir: logPath ? FILE_STORAGE_DIR : undefined })}

exporters:
${otlpHttpExporterYaml(baseUrl)}

service:
${serviceExtensionsYaml(!!logPath)}
  pipelines:
${logPath ? '    logs:\n      receivers: [file_log, otlp]\n      processors: [memory_limiter, resource_detection, resource, batch]\n      exporters: [otlp_http]\n' : '    logs:\n      receivers: [otlp]\n      processors: [memory_limiter, resource_detection, resource, batch]\n      exporters: [otlp_http]\n'}    metrics:
      receivers: [postgresql, otlp]
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
        `Create the monitoring role shown in the config comment, then run: ` +
        `OBZLY_API_KEY="${apiKey}" OBZLY_DB_PASSWORD="${password}" otelcol-contrib --config=otel-collector-config.yaml` +
        (logPath ? `. ${FILE_STORAGE_NOTE}` : ''),
    };
  },
};
