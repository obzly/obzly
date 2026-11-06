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

export const jvmDataSource: DataSourceDefinition = {
  id: 'jvm',
  name: 'JVM (Java application)',
  category: 'Application Runtimes',
  description: 'Heap/GC/thread metrics from any JVM app, via JMX - no code changes needed.',
  fields: [
    {
      key: 'jmxUrl',
      label: 'JMX service URL',
      defaultValue: 'service:jmx:rmi:///jndi/rmi://localhost:9010/jmxrmi',
      helpText:
        'Start your JVM with -Dcom.sun.management.jmxremote.port=9010 -Dcom.sun.management.jmxremote.authenticate=false -Dcom.sun.management.jmxremote.ssl=false (add auth for production).',
    },
    OTLP_REMOTE_FIELD,
  ],
  build: ({ baseUrl, apiKey, fieldValues }) => {
    const jmxUrl =
      fieldValues.jmxUrl.trim() || 'service:jmx:rmi:///jndi/rmi://localhost:9010/jmxrmi';

    const content = `# Obzly OpenTelemetry Collector config for a JVM application (via JMX).
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

${extensionsYaml()}

exporters:
${otlpHttpExporterYaml(baseUrl)}

service:
${serviceExtensionsYaml()}
  pipelines:
    logs:
      receivers: [otlp]
      processors: [memory_limiter, resource_detection, batch]
      exporters: [otlp_http]
    metrics:
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
      files: [
        { filename: 'otel-collector-config.yaml', language: 'yaml', content },
        {
          filename: 'jmx-scraper.properties',
          language: 'ini',
          content: jmxScraperProperties({ jmxUrl, targetSystem: 'jvm', serviceName: 'jvm-app' }),
        },
      ],
      instructions: jmxScraperInstructions(apiKey),
    };
  },
};
