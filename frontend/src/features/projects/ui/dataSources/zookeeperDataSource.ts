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

export const zookeeperDataSource: DataSourceDefinition = {
  id: 'zookeeper',
  name: 'ZooKeeper',
  category: 'Infrastructure',
  description:
    'Ensemble metrics (watches, znodes, latency) via the four-letter-word admin protocol.',
  fields: [
    { key: 'endpoint', label: 'Endpoint', defaultValue: 'localhost:2181' },
    OTLP_REMOTE_FIELD,
  ],
  build: ({ baseUrl, apiKey, fieldValues }) => {
    const endpoint = fieldValues.endpoint.trim() || 'localhost:2181';

    const content = `# Obzly OpenTelemetry Collector config for ZooKeeper.
#
# Uses the "mntr" four-letter-word command, which must be allowed in
# zoo.cfg (it's allowed by default on modern ZooKeeper; older versions
# need 4lw.commands.whitelist=mntr or "*" set explicitly).

receivers:
  zookeeper:
    endpoint: ${endpoint}
    collection_interval: 30s

${otlpReceiverYaml(fieldValues)}

processors:
${MEMORY_LIMITER_PROCESSOR_YAML}
${BATCH_PROCESSOR_YAML}
${RESOURCEDETECTION_PROCESSOR_YAML}
  resource:
    attributes:
      - key: service.name
        value: zookeeper
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
      receivers: [zookeeper, otlp]
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
      instructions: `Run with: OBZLY_API_KEY="${apiKey}" otelcol-contrib --config=otel-collector-config.yaml`,
    };
  },
};
