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

export const sparkDataSource: DataSourceDefinition = {
  id: 'spark',
  name: 'Apache Spark',
  category: 'Application Runtimes',
  description: 'Application/executor/stage metrics via the Spark REST API.',
  fields: [
    {
      key: 'endpoint',
      label: 'Spark master/history server endpoint',
      defaultValue: 'http://localhost:4040',
    },
    { key: 'applicationNames', label: 'Application name(s), comma-separated', defaultValue: '' },
    OTLP_REMOTE_FIELD,
  ],
  build: ({ baseUrl, apiKey, fieldValues }) => {
    const endpoint = fieldValues.endpoint.trim() || 'http://localhost:4040';
    const applicationNames = fieldValues.applicationNames
      .split(',')
      .map((a) => a.trim())
      .filter(Boolean);
    const applicationNamesYaml = applicationNames.length
      ? `\n    application_names:\n${applicationNames.map((a) => `      - ${a}`).join('\n')}`
      : '';

    const content = `# Obzly OpenTelemetry Collector config for Apache Spark.
#
# Point endpoint at a running application's own UI (default port 4040) or
# a cluster manager/history server that proxies the REST API.${applicationNames.length ? '' : ' Leave\n# application_names unset to auto-discover running applications.'}

receivers:
  apache_spark:
    endpoint: ${endpoint}
    collection_interval: 30s${applicationNamesYaml}

${otlpReceiverYaml(fieldValues)}

processors:
${MEMORY_LIMITER_PROCESSOR_YAML}
${BATCH_PROCESSOR_YAML}
${RESOURCEDETECTION_PROCESSOR_YAML}
  resource:
    attributes:
      - key: service.name
        value: spark
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
      receivers: [apache_spark, otlp]
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
