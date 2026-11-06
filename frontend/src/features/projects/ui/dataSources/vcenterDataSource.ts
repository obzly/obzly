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

export const vcenterDataSource: DataSourceDefinition = {
  id: 'vcenter',
  name: 'VMware vCenter',
  category: 'Infrastructure',
  description: 'Host/VM/datastore metrics (CPU, memory, disk, network) via the vSphere API.',
  fields: [
    { key: 'endpoint', label: 'vCenter URL', defaultValue: 'https://vcenter.example.com' },
    { key: 'username', label: 'Username', defaultValue: 'otel_monitor@vsphere.local' },
    {
      key: 'password',
      label: 'Password',
      defaultValue: '',
      placeholder: 'set this to a real password before running the collector',
    },
    OTLP_REMOTE_FIELD,
  ],
  build: ({ baseUrl, apiKey, fieldValues }) => {
    const endpoint = fieldValues.endpoint.trim() || 'https://vcenter.example.com';
    const username = fieldValues.username.trim() || 'otel_monitor@vsphere.local';
    const password = fieldValues.password.trim() || 'CHANGE_ME';

    const content = `# Obzly OpenTelemetry Collector config for VMware vCenter.
#
# The monitoring account only needs the built-in "Read-only" role granted
# at the vCenter (or Datacenter) level.

receivers:
  vcenter:
    endpoint: ${endpoint}
    username: ${username}
    password: \${env:OBZLY_DB_PASSWORD}
    collection_interval: 60s
    tls:
      insecure_skip_verify: true

${otlpReceiverYaml(fieldValues)}

processors:
${MEMORY_LIMITER_PROCESSOR_YAML}
${BATCH_PROCESSOR_YAML}
${RESOURCEDETECTION_PROCESSOR_YAML}
  resource:
    attributes:
      - key: service.name
        value: vcenter
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
      receivers: [vcenter, otlp]
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
        `Grant the monitoring account the built-in Read-only role, then run: ` +
        `OBZLY_API_KEY="${apiKey}" OBZLY_DB_PASSWORD="${password}" otelcol-contrib --config=otel-collector-config.yaml. ` +
        `Uses a self-signed-cert-tolerant TLS setting by default - remove tls.insecure_skip_verify if your vCenter has a trusted cert.`,
    };
  },
};
