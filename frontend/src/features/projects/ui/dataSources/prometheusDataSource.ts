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

export const prometheusDataSource: DataSourceDefinition = {
  id: 'prometheus',
  name: 'Prometheus-format metrics',
  category: 'Custom',
  description:
    'Scrape any existing /metrics endpoint (app, exporter, sidecar) that already speaks Prometheus.',
  fields: [
    { key: 'jobName', label: 'Job name', defaultValue: 'my-app' },
    {
      key: 'targets',
      label: 'Targets (comma-separated host:port)',
      defaultValue: 'localhost:9100',
    },
    { key: 'metricsPath', label: 'Metrics path', defaultValue: '/metrics' },
    OTLP_REMOTE_FIELD,
  ],
  build: ({ baseUrl, apiKey, fieldValues }) => {
    const jobName = fieldValues.jobName.trim() || 'my-app';
    const targets = fieldValues.targets
      .split(',')
      .map((t) => t.trim())
      .filter(Boolean);
    const targetsYaml = (targets.length ? targets : ['localhost:9100'])
      .map((t) => `                - ${t}`)
      .join('\n');
    const metricsPath = fieldValues.metricsPath.trim() || '/metrics';

    const content = `# Obzly OpenTelemetry Collector config for scraping an existing
# Prometheus-format /metrics endpoint - works for anything already
# exposing one: your own app's client library, node_exporter,
# cadvisor, blackbox_exporter, etc.

receivers:
  prometheus:
    config:
      scrape_configs:
        - job_name: '${jobName}'
          metrics_path: '${metricsPath}'
          scrape_interval: 30s
          static_configs:
            - targets:
${targetsYaml}

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
      receivers: [prometheus, otlp]
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
      instructions: `Run with: OBZLY_API_KEY="${apiKey}" otelcol-contrib --config=otel-collector-config.yaml. Add more scrape_configs entries for additional targets.`,
    };
  },
};
