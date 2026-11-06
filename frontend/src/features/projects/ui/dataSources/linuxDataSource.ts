import {
  BATCH_PROCESSOR_YAML,
  COLLECTOR_VERSION,
  FILE_STORAGE_DIR,
  MEMORY_LIMITER_PROCESSOR_YAML,
  OTLP_REMOTE_FIELD,
  RESOURCEDETECTION_PROCESSOR_YAML,
  extensionsYaml,
  otlpHttpExporterYaml,
  otlpReceiverYaml,
  serviceExtensionsYaml,
} from './otelCollectorCommon';
import type { DataSourceDefinition } from './types';

export const linuxDataSource: DataSourceDefinition = {
  id: 'linux',
  name: 'Linux Server',
  category: 'Infrastructure',
  description:
    'systemd journal logs, host metrics (CPU/memory/disk), and OTLP - as a systemd service.',
  fields: [OTLP_REMOTE_FIELD],
  build: ({ baseUrl, apiKey, fieldValues }) => {
    const configYaml = `receivers:
  # Every unit's systemd journal entries (the same source \`journalctl\`
  # reads from) - covers services managed by systemd without needing to
  # know which log files each one writes to. The journal cursor is kept in
  # file_storage, so a restart resumes where it stopped.
  journald:
    storage: file_storage

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

${extensionsYaml({ fileStorageDir: FILE_STORAGE_DIR })}

exporters:
${otlpHttpExporterYaml(baseUrl)}

service:
${serviceExtensionsYaml(true)}
  pipelines:
    logs:
      receivers: [journald, otlp]
      processors: [memory_limiter, resource_detection, batch]
      exporters: [otlp_http]
    traces:
      receivers: [otlp]
      processors: [memory_limiter, resource_detection, batch]
      exporters: [otlp_http]
    metrics:
      receivers: [host_metrics, otlp]
      processors: [memory_limiter, resource_detection, batch]
      exporters: [otlp_http]
`;

    const installScript = `#!/bin/bash
# Obzly OpenTelemetry Collector install script for a Linux server.
# Installs the collector as a systemd service that ships journald logs,
# host metrics, and accepts OTLP from local apps.
#
# Run as root: sudo bash install-obzly-collector.sh

set -euo pipefail

ARCH=$(dpkg --print-architecture 2>/dev/null || uname -m | sed 's/x86_64/amd64/;s/aarch64/arm64/')
VERSION="${COLLECTOR_VERSION}"
PKG="otelcol-contrib_\${VERSION}_linux_\${ARCH}.deb"

echo "Downloading OpenTelemetry Collector \${VERSION} (\${ARCH})..."
curl -fsSL -o "/tmp/\${PKG}" \\
  "https://github.com/open-telemetry/opentelemetry-collector-releases/releases/download/v\${VERSION}/\${PKG}"
dpkg -i "/tmp/\${PKG}"
rm -f "/tmp/\${PKG}"

mkdir -p /etc/otelcol-contrib
cat > /etc/otelcol-contrib/config.yaml << 'OBZLY_EOF'
${configYaml}OBZLY_EOF

mkdir -p /etc/systemd/system/otelcol-contrib.service.d
cat > /etc/systemd/system/otelcol-contrib.service.d/obzly-env.conf << ENV_EOF
[Service]
Environment="OBZLY_API_KEY=${apiKey}"
# journald access requires the service account to be in the systemd-journal group
SupplementaryGroups=systemd-journal
# /var/lib/otelcol, owned by the service user: file_storage keeps the journal cursor there
StateDirectory=otelcol
ENV_EOF

systemctl daemon-reload
systemctl enable --now otelcol-contrib
systemctl status otelcol-contrib --no-pager
`;

    return {
      filename: 'install-obzly-collector.sh',
      language: 'bash',
      content: installScript,
      instructions:
        'Download this as install-obzly-collector.sh and run: sudo bash install-obzly-collector.sh ' +
        '(Debian/Ubuntu - installs the official .deb package). For RPM-based distros, swap the .deb ' +
        'download for the matching .rpm from the same GitHub release and use rpm/dnf instead of dpkg.',
    };
  },
};
