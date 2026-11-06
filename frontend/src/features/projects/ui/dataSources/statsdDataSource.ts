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

export const statsdDataSource: DataSourceDefinition = {
  id: 'statsd',
  name: 'StatsD',
  category: 'Custom',
  description:
    'Accept metrics from anything already emitting the StatsD wire protocol (counters, gauges, timers).',
  fields: [{ key: 'port', label: 'Listen port', defaultValue: '8125' }, OTLP_REMOTE_FIELD],
  build: ({ baseUrl, apiKey, fieldValues }) => {
    const port = fieldValues.port.trim() || '8125';

    const content = `# Obzly OpenTelemetry Collector config for StatsD.
#
# Listens for StatsD-protocol metrics (counters, gauges, timers, sets) -
# point anything already emitting StatsD (many app frameworks, game
# servers, custom instrumentation) at this collector's host, port ${port}/udp.
# Counters arrive as delta sums, which Obzly turns into running totals.

receivers:
  statsd:
    endpoint: "0.0.0.0:${port}"
    aggregation_interval: 30s

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
      receivers: [statsd, otlp]
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
      instructions: `Run with: OBZLY_API_KEY="${apiKey}" otelcol-contrib --config=otel-collector-config.yaml, then point your app's StatsD client at this host, port ${port}/udp.`,
    };
  },
};
