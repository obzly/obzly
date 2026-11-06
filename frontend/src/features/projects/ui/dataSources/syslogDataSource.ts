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

export const syslogDataSource: DataSourceDefinition = {
  id: 'syslog',
  name: 'Syslog',
  category: 'Infrastructure',
  description:
    'RFC3164/RFC5424 syslog over UDP or TCP - firewalls, network gear, appliances, anything that only speaks syslog.',
  fields: [
    { key: 'protocol', label: 'Protocol (udp or tcp)', defaultValue: 'udp' },
    { key: 'port', label: 'Listen port', defaultValue: '514' },
    { key: 'format', label: 'Syslog format (rfc3164 or rfc5424)', defaultValue: 'rfc3164' },
    OTLP_REMOTE_FIELD,
  ],
  build: ({ baseUrl, apiKey, fieldValues }) => {
    const protocol = fieldValues.protocol.trim().toLowerCase() === 'tcp' ? 'tcp' : 'udp';
    const port = fieldValues.port.trim() || '514';
    const format = fieldValues.format.trim().toLowerCase() === 'rfc5424' ? 'rfc5424' : 'rfc3164';

    const content = `# Obzly OpenTelemetry Collector config for Syslog.
#
# Listens for syslog messages the way syslogd/rsyslog would - point your
# device's/appliance's syslog forwarding at this collector's host on port
# ${port}/${protocol}. Ports below 1024 need the collector process to run
# as root (or use setcap/a reverse proxy) - consider a port like 5514 and
# forwarding 514 to it if you'd rather not run as root.

receivers:
  syslog:
    protocol: ${format}
    ${protocol}:
      listen_address: "0.0.0.0:${port}"

${otlpReceiverYaml(fieldValues)}

processors:
${MEMORY_LIMITER_PROCESSOR_YAML}
${BATCH_PROCESSOR_YAML}
${RESOURCEDETECTION_PROCESSOR_YAML}

${extensionsYaml()}

exporters:
${otlpHttpExporterYaml(baseUrl)}

service:
${serviceExtensionsYaml()}
  pipelines:
    logs:
      receivers: [syslog, otlp]
      processors: [memory_limiter, resource_detection, batch]
      exporters: [otlp_http]
    traces:
      receivers: [otlp]
      processors: [memory_limiter, resource_detection, batch]
      exporters: [otlp_http]
`;

    return {
      filename: 'otel-collector-config.yaml',
      language: 'yaml',
      content,
      instructions: `Run with: OBZLY_API_KEY="${apiKey}" otelcol-contrib --config=otel-collector-config.yaml, then point your device's syslog target at this host, port ${port}/${protocol}.`,
    };
  },
};
