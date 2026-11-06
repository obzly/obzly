import {
  BATCH_PROCESSOR_YAML,
  MEMORY_LIMITER_PROCESSOR_YAML,
  OTLP_REMOTE_FIELD,
  RESOURCEDETECTION_PROCESSOR_YAML,
  WINDOWS_FILE_STORAGE_DIR,
  extensionsYaml,
  otlpHttpExporterYaml,
  otlpReceiverYaml,
  serviceExtensionsYaml,
} from './otelCollectorCommon';
import type { DataSourceDefinition } from './types';

export const windowsDataSource: DataSourceDefinition = {
  id: 'windows',
  name: 'Windows Server',
  category: 'Infrastructure',
  description: 'Windows Event Log (Application/System/Security) plus host metrics.',
  fields: [
    {
      key: 'channels',
      label: 'Event Log channels (comma-separated)',
      defaultValue: 'Application, System',
    },
    OTLP_REMOTE_FIELD,
  ],
  build: ({ baseUrl, apiKey, fieldValues }) => {
    const channels = fieldValues.channels
      .split(',')
      .map((c) => c.trim())
      .filter(Boolean);
    const channelList = channels.length ? channels : ['Application', 'System'];
    const receiverBlocks = channelList
      .map(
        (channel, i) => `  windows_event_log/${i}:
    channel: ${channel}
    start_at: end
    storage: file_storage`,
      )
      .join('\n\n');
    const receiverNames = channelList.map((_, i) => `windows_event_log/${i}`).join(', ');

    const content = `# Obzly OpenTelemetry Collector config for Windows Server.
#
# Requires the collector to run as a Windows service with permission to
# read the listed Event Log channels (the default LocalSystem account
# normally has this). Event Log bookmarks are kept in file_storage, so a
# restart resumes where it stopped.

receivers:
${receiverBlocks}

  host_metrics:
    collection_interval: 30s
    scrapers:
      cpu: {}
      memory: {}
      disk: {}
      filesystem: {}
      network: {}
      load: {}

${otlpReceiverYaml(fieldValues)}

processors:
${MEMORY_LIMITER_PROCESSOR_YAML}
${BATCH_PROCESSOR_YAML}
${RESOURCEDETECTION_PROCESSOR_YAML}

${extensionsYaml({ fileStorageDir: WINDOWS_FILE_STORAGE_DIR })}

exporters:
${otlpHttpExporterYaml(baseUrl)}

service:
${serviceExtensionsYaml(true)}
  pipelines:
    logs:
      receivers: [${receiverNames}, otlp]
      processors: [memory_limiter, resource_detection, batch]
      exporters: [otlp_http]
    metrics:
      receivers: [host_metrics, otlp]
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
      instructions:
        `Install the collector as a Windows service (see the official ` +
        `"otelcol-contrib" .msi/zip release), set OBZLY_API_KEY=${apiKey} in its environment, ` +
        `and point it at this config file.`,
    };
  },
};
