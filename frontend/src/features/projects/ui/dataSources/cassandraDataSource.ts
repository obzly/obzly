import { jmxScraperInstructions, jmxScraperProperties } from './jmxScraper';
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

export const cassandraDataSource: DataSourceDefinition = {
  id: 'cassandra',
  name: 'Cassandra',
  category: 'Databases',
  description: 'Read/write latency, compaction, and storage metrics via JMX.',
  fields: [
    {
      key: 'jmxUrl',
      label: 'JMX service URL',
      defaultValue: 'service:jmx:rmi:///jndi/rmi://localhost:7199/jmxrmi',
      helpText: 'Cassandra exposes JMX on port 7199 by default.',
    },
    OTLP_REMOTE_FIELD,
  ],
  build: ({ baseUrl, apiKey, fieldValues }) => {
    const jmxUrl =
      fieldValues.jmxUrl.trim() || 'service:jmx:rmi:///jndi/rmi://localhost:7199/jmxrmi';

    const content = `# Obzly OpenTelemetry Collector config for Cassandra (via JMX).
#
# Metrics come from the OpenTelemetry JMX Scraper (jmx-scraper.properties,
# generated alongside this file), which sends them to the otlp receiver
# below. otelcol-contrib no longer has a jmx receiver.

receivers:
${otlpReceiverYaml(fieldValues)}

processors:
${MEMORY_LIMITER_PROCESSOR_YAML}
${BATCH_PROCESSOR_YAML}
${RESOURCEDETECTION_PROCESSOR_YAML}
  resource:
    attributes:
      - key: service.name
        value: cassandra
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
      receivers: [otlp]
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
      files: [
        { filename: 'otel-collector-config.yaml', language: 'yaml', content },
        {
          filename: 'jmx-scraper.properties',
          language: 'ini',
          content: jmxScraperProperties({
            jmxUrl,
            targetSystem: 'cassandra',
            // Since 1.61.0 "auto" picks the unstable instrumentation
            // definitions for cassandra; legacy keeps the metric names.
            targetSource: 'legacy',
            serviceName: 'cassandra',
          }),
        },
      ],
      instructions: jmxScraperInstructions(apiKey),
    };
  },
};
