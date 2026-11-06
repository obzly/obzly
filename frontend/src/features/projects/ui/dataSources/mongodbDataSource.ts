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

export const mongodbDataSource: DataSourceDefinition = {
  id: 'mongodb',
  name: 'MongoDB',
  category: 'Databases',
  description: 'Database metrics (operations, connections, cache) plus the mongod log file.',
  fields: [
    { key: 'host', label: 'Host', defaultValue: 'localhost' },
    { key: 'port', label: 'Port', defaultValue: '27017' },
    { key: 'username', label: 'Monitoring username', defaultValue: 'otel_monitor' },
    {
      key: 'password',
      label: 'Monitoring password',
      defaultValue: '',
      placeholder: 'set this to a real password before running the collector',
    },
    {
      key: 'logPath',
      label: 'mongod log path (leave blank to skip log tailing)',
      defaultValue: '/var/log/mongodb/mongod.log',
    },
    OTLP_REMOTE_FIELD,
  ],
  build: ({ baseUrl, apiKey, fieldValues }) => {
    const host = fieldValues.host.trim() || 'localhost';
    const port = fieldValues.port.trim() || '27017';
    const username = fieldValues.username.trim() || 'otel_monitor';
    const password = fieldValues.password.trim() || 'CHANGE_ME';
    const logPath = fieldValues.logPath.trim();

    const logReceiver = logPath
      ? `  file_log:
    include: [${logPath}]
    start_at: end
    storage: file_storage
    operators:
      # mongod's default log is one JSON object per line since MongoDB 4.4+.
      - type: json_parser
        timestamp:
          parse_from: attributes.t.$date
          layout: '%Y-%m-%dT%H:%M:%S.%L%z'
      - type: severity_parser
        parse_from: attributes.s
        mapping:
          info: [I, D1, D2, D3, D4, D5]
          warn: W
          error: [E, F]

`
      : '';

    const content = `# Obzly OpenTelemetry Collector config for MongoDB.
#
# The monitoring user only needs the built-in clusterMonitor role:
#   db.getSiblingDB("admin").createUser({
#     user: "${username}", pwd: "<password>", roles: ["clusterMonitor"]
#   })
${logPath ? '#\n# Log tailing expects the default JSON log format (MongoDB 4.4+). For\n# older versions using the plain-text format, swap json_parser for a\n# regex_parser matching that format instead.\n' : ''}
receivers:
${logReceiver}  mongodb:
    hosts:
      - endpoint: ${host}:${port}
    username: ${username}
    password: \${env:OBZLY_DB_PASSWORD}
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
        value: mongodb
        action: upsert

${extensionsYaml({ fileStorageDir: logPath ? FILE_STORAGE_DIR : undefined })}

exporters:
${otlpHttpExporterYaml(baseUrl)}

service:
${serviceExtensionsYaml(!!logPath)}
  pipelines:
${logPath ? '    logs:\n      receivers: [file_log, otlp]\n      processors: [memory_limiter, resource_detection, resource, batch]\n      exporters: [otlp_http]\n' : '    logs:\n      receivers: [otlp]\n      processors: [memory_limiter, resource_detection, resource, batch]\n      exporters: [otlp_http]\n'}    metrics:
      receivers: [mongodb, otlp]
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
