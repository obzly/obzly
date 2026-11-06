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

export const mysqlDataSource: DataSourceDefinition = {
  id: 'mysql',
  name: 'MySQL',
  category: 'Databases',
  description: 'Database metrics (threads, buffer pool, replication) plus the MySQL error log.',
  fields: [
    { key: 'host', label: 'Host', defaultValue: 'localhost' },
    { key: 'port', label: 'Port', defaultValue: '3306' },
    { key: 'username', label: 'Monitoring username', defaultValue: 'otel_monitor' },
    {
      key: 'password',
      label: 'Monitoring password',
      defaultValue: '',
      placeholder: 'set this to a real password before running the collector',
    },
    {
      key: 'logPath',
      label: 'Error log path (leave blank to skip log tailing)',
      defaultValue: '/var/log/mysql/error.log',
    },
    OTLP_REMOTE_FIELD,
  ],
  build: ({ baseUrl, apiKey, fieldValues }) => {
    const host = fieldValues.host.trim() || 'localhost';
    const port = fieldValues.port.trim() || '3306';
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
        regex: '^(?P<time>\\d{4}-\\d{2}-\\d{2}T\\d{2}:\\d{2}:\\d{2}\\.\\d+Z) \\d+ \\[(?P<level>\\w+)\\]'
        timestamp:
          parse_from: attributes.time
          layout: '%Y-%m-%dT%H:%M:%S.%fZ'
      - type: severity_parser
        parse_from: attributes.level
        mapping:
          info: [Note, System]
          warn: Warning
          error: ERROR

`
      : '';

    const content = `# Obzly OpenTelemetry Collector config for MySQL.
#
# The mysql receiver's monitoring user only needs PROCESS, REPLICATION
# CLIENT, and SELECT on performance_schema:
#   CREATE USER '${username}'@'%' IDENTIFIED BY '<password>';
#   GRANT PROCESS, REPLICATION CLIENT ON *.* TO '${username}'@'%';
#   GRANT SELECT ON performance_schema.* TO '${username}'@'%';
${logPath ? '#\n# Log tailing expects the default MySQL 8+ JSON-ish error log format;\n# adjust the regex below for MySQL 5.7 or a custom log_error_verbosity.\n' : ''}
receivers:
${logReceiver}  mysql:
    endpoint: ${host}:${port}
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
        value: mysql
        action: upsert

${extensionsYaml({ fileStorageDir: logPath ? FILE_STORAGE_DIR : undefined })}

exporters:
${otlpHttpExporterYaml(baseUrl)}

service:
${serviceExtensionsYaml(!!logPath)}
  pipelines:
${logPath ? '    logs:\n      receivers: [file_log, otlp]\n      processors: [memory_limiter, resource_detection, resource, batch]\n      exporters: [otlp_http]\n' : '    logs:\n      receivers: [otlp]\n      processors: [memory_limiter, resource_detection, resource, batch]\n      exporters: [otlp_http]\n'}    metrics:
      receivers: [mysql, otlp]
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
        `OBZLY_API_KEY="${apiKey}" OBZLY_DB_PASSWORD="${password}" otelcol-contrib --config=otel-collector-config.yaml` +
        (logPath ? `. ${FILE_STORAGE_NOTE}` : ''),
    };
  },
};
