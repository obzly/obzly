import {
  BATCH_PROCESSOR_YAML,
  MEMORY_LIMITER_PROCESSOR_YAML,
  OTLP_REMOTE_FIELD,
  RESOURCEDETECTION_PROCESSOR_YAML,
  WINDOWS_FILE_STORAGE_DIR,
  extensionsYaml,
  otlpHttpExporterYaml,
  otlpReceiverYaml,
  serviceExtensionsYaml,
} from './otelCollectorCommon';
import type { DataSourceDefinition } from './types';

export const iisDataSource: DataSourceDefinition = {
  id: 'iis',
  name: 'IIS',
  category: 'Web Servers',
  description: 'Request/connection metrics plus W3C access logs, for Windows-hosted IIS sites.',
  fields: [
    {
      key: 'accessLogPath',
      label: 'W3C access log path',
      defaultValue: 'C:\\inetpub\\logs\\LogFiles\\W3SVC1\\*.log',
    },
    OTLP_REMOTE_FIELD,
  ],
  build: ({ baseUrl, apiKey, fieldValues }) => {
    const accessLogPath =
      fieldValues.accessLogPath.trim() || 'C:\\inetpub\\logs\\LogFiles\\W3SVC1\\*.log';

    const content = `# Obzly OpenTelemetry Collector config for IIS.
#
# The iisreceiver reads Windows performance counters IIS already exposes -
# no extra IIS configuration needed. Access logs use the default W3C
# extended format; if you've customized which fields are logged, adjust
# the header_delimiter/fields below to match.

receivers:
  iis:
    collection_interval: 30s

  file_log:
    include: ["${accessLogPath.replace(/\\/g, '\\\\')}"]
    start_at: end
    storage: file_storage
    operators:
      # W3C logs start with #Fields: header lines and are space-delimited
      # after that - this parser skips comment lines automatically.
      - type: csv_parser
        header_delimiter: ' '
        delimiter: ' '
        # Field names avoid hyphens on purpose (date, sc_status, etc, not
        # sc-status) - a hyphen in a later operator's field reference would
        # be parsed as subtraction, not part of the name.
        header: 'date time s_ip cs_method cs_uri_stem cs_uri_query s_port cs_username c_ip cs_user_agent sc_status sc_substatus sc_win32_status time_taken'
      - type: filter
        expr: 'body matches "^#"'
        drop_ratio: 1.0
      - type: severity_parser
        parse_from: attributes.sc_status
        mapping:
          info: 2xx
          info2: 3xx
          warn: 4xx
          error: 5xx

${otlpReceiverYaml(fieldValues)}

processors:
${MEMORY_LIMITER_PROCESSOR_YAML}
${BATCH_PROCESSOR_YAML}
${RESOURCEDETECTION_PROCESSOR_YAML}
  resource:
    attributes:
      - key: service.name
        value: iis
        action: upsert

${extensionsYaml({ fileStorageDir: WINDOWS_FILE_STORAGE_DIR })}

exporters:
${otlpHttpExporterYaml(baseUrl)}

service:
${serviceExtensionsYaml(true)}
  pipelines:
    logs:
      receivers: [file_log, otlp]
      processors: [memory_limiter, resource_detection, resource, batch]
      exporters: [otlp_http]
    metrics:
      receivers: [iis, otlp]
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
      instructions: `Install the collector as a Windows service, set OBZLY_API_KEY=${apiKey} in its environment, and point it at this config file.`,
    };
  },
};
