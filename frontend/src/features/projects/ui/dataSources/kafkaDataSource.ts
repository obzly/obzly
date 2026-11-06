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

export const kafkaDataSource: DataSourceDefinition = {
  id: 'kafka',
  name: 'Apache Kafka',
  category: 'Message Queues',
  description: 'Broker/topic/consumer-group metrics (partitions, lag, throughput).',
  fields: [
    {
      key: 'brokers',
      label: 'Brokers (comma-separated host:port)',
      defaultValue: 'localhost:9092',
    },
    OTLP_REMOTE_FIELD,
  ],
  build: ({ baseUrl, apiKey, fieldValues }) => {
    const brokers = fieldValues.brokers
      .split(',')
      .map((b) => b.trim())
      .filter(Boolean);
    const brokerList = (brokers.length ? brokers : ['localhost:9092'])
      .map((b) => `      - ${b}`)
      .join('\n');

    const content = `# Obzly OpenTelemetry Collector config for Apache Kafka.
#
# Connects to the cluster as a regular client (no special ACLs beyond
# describe/read on the cluster and topics) to pull broker, topic, and
# consumer-group metrics.

receivers:
  kafka_metrics:
    brokers:
${brokerList}
    protocol_version: 2.0.0
    scrapers:
      - brokers
      - topics
      - consumers
    collection_interval: 30s

${otlpReceiverYaml(fieldValues)}

processors:
${MEMORY_LIMITER_PROCESSOR_YAML}
${BATCH_PROCESSOR_YAML}
${RESOURCEDETECTION_PROCESSOR_YAML}
  resource:
    attributes:
      - key: service.name
        value: kafka
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
      receivers: [kafka_metrics, otlp]
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
        `Run with: OBZLY_API_KEY="${apiKey}" otelcol-contrib --config=otel-collector-config.yaml. ` +
        `For broker logs, point the Linux Server or Docker source at wherever your Kafka install writes server.log.`,
    };
  },
};
