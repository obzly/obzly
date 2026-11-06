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

export const httpCheckDataSource: DataSourceDefinition = {
  id: 'http-check',
  name: 'HTTP Uptime Check',
  category: 'Custom',
  description:
    'Black-box endpoint monitoring: status code and response time for any URL, no agent needed on the target.',
  fields: [
    { key: 'targetUrl', label: 'URL to check', defaultValue: 'https://example.com/health' },
    OTLP_REMOTE_FIELD,
  ],
  build: ({ baseUrl, apiKey, fieldValues }) => {
    const targetUrl = fieldValues.targetUrl.trim() || 'https://example.com/health';

    const content = `# Obzly OpenTelemetry Collector config for HTTP uptime checks.
#
# Periodically requests the URL below and reports status code and
# response time as metrics - useful for endpoints you can't (or don't
# want to) install an agent on directly.

receivers:
  http_check:
    targets:
      - endpoint: "${targetUrl}"
        method: GET
    collection_interval: 30s

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
    metrics:
      receivers: [http_check, otlp]
      processors: [memory_limiter, resource_detection, batch]
      exporters: [otlp_http]
    logs:
      receivers: [otlp]
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
      instructions: `Run with: OBZLY_API_KEY="${apiKey}" otelcol-contrib --config=otel-collector-config.yaml. Add more entries under targets for additional URLs.`,
    };
  },
};
