import {
  BATCH_PROCESSOR_YAML,
  COLLECTOR_IMAGE,
  FILE_STORAGE_DIR,
  MEMORY_LIMITER_PROCESSOR_YAML,
  RESOURCEDETECTION_PROCESSOR_YAML,
  extensionsYaml,
  otlpHttpExporterYaml,
  serviceExtensionsYaml,
} from './otelCollectorCommon';
import type { DataSourceDefinition } from './types';

export const dockerDataSource: DataSourceDefinition = {
  id: 'docker',
  name: 'Docker / Docker Compose',
  category: 'Infrastructure',
  description:
    'Container logs from the Docker daemon’s own JSON log files, via a sidecar collector.',
  fields: [
    {
      key: 'apiKeyName',
      label: 'Collector name (for reference only)',
      defaultValue: 'Docker OTel Collector',
    },
    {
      key: 'otlpRemote',
      label: 'Accept OTLP from other machines',
      type: 'checkbox',
      defaultValue: 'no',
      helpText:
        'Containers on the same Docker network always reach the collector at otel-collector:4317/' +
        '4318 (it listens on its container IP). Off: the host publishes those ports on 127.0.0.1 ' +
        'only. On: on every interface, with no authentication, so firewall ports 4317 and 4318 to ' +
        'trusted networks.',
    },
  ],
  build: ({ baseUrl, apiKey, fieldValues }) => {
    const publish = fieldValues.otlpRemote === 'yes' ? '' : '127.0.0.1:';
    const configYaml = `# Obzly OpenTelemetry Collector for Docker
#
# Tails every container's JSON log file the Docker daemon already writes
# (the same files \`docker logs\` reads from), scrapes per-container
# CPU/memory/network/block-IO metrics via the Docker API (the same data
# \`docker stats\` shows), the Docker host's own CPU/memory/disk/network from
# its /proc and /sys (mounted at /hostfs), and also accepts OTLP pushed
# directly from your apps. Run this collector as its own container on the same Docker host -
# it needs read access to the host's Docker log directory and the Docker
# socket, which only the daemon's own host has.

receivers:
  file_log:
    include: [/var/lib/docker/containers/*/*-json.log]
    start_at: end
    storage: file_storage
    include_file_path: true
    include_file_name: false
    operators:
      # Docker's own JSON log format: {"log":"...","stream":"stdout","time":"..."}
      - type: json_parser
        timestamp:
          parse_from: attributes.time
          layout: '%Y-%m-%dT%H:%M:%S.%LZ'
      - type: move
        from: attributes.log
        to: body
      - type: remove
        field: attributes.time

  docker_stats:
    endpoint: unix:///var/run/docker.sock
    collection_interval: 30s

  # The Docker host itself, read through the read-only /hostfs mount.
  # network reports this container's interfaces (its own network namespace).
  host_metrics:
    root_path: /hostfs
    collection_interval: 30s
    scrapers:
      cpu: {}
      memory: {}
      load: {}
      filesystem: {}
      network: {}
      disk: {}
      paging: {}

  otlp:
    protocols:
      grpc:
        endpoint: 0.0.0.0:4317
      http:
        endpoint: 0.0.0.0:4318

processors:
${MEMORY_LIMITER_PROCESSOR_YAML}
${BATCH_PROCESSOR_YAML}
${RESOURCEDETECTION_PROCESSOR_YAML}

${extensionsYaml({ fileStorageDir: FILE_STORAGE_DIR, healthCheckEndpoint: '0.0.0.0:13133' })}

exporters:
${otlpHttpExporterYaml(baseUrl)}

service:
${serviceExtensionsYaml(true)}
  pipelines:
    logs:
      receivers: [file_log, otlp]
      processors: [memory_limiter, resource_detection, batch]
      exporters: [otlp_http]
    traces:
      receivers: [otlp]
      processors: [memory_limiter, resource_detection, batch]
      exporters: [otlp_http]
    metrics:
      receivers: [docker_stats, host_metrics, otlp]
      processors: [memory_limiter, resource_detection, batch]
      exporters: [otlp_http]
`;

    const composeYaml = `# Add this service to your existing docker-compose.yml (or run standalone
# with \`docker run\`). Keep otel-collector-config.yaml as its own file next
# to this one - don't merge them, see the note below the generated files.

services:
  otel-collector:
    image: ${COLLECTOR_IMAGE}
    container_name: obzly-otel-collector
    restart: unless-stopped
    # The upstream image drops to a non-root user by default, which can't
    # read Docker's own (root-owned) container log directory or connect to
    # docker.sock - both needed for the file_log and docker_stats receivers
    # below. Running as root (uid 0) is the standard fix; it doesn't affect
    # what this container can do beyond those two host-owned resources it
    # already has read access to via the mounts below. Numeric uid, not the
    # name "root" - this image has no /etc/passwd entry for it, so
    # "user: root" fails with "unable to find user root: no matching
    # entries in passwd file".
    user: "0:0"
    # The host's UTS namespace, so host.name is the Docker host's name
    # rather than this container's ID.
    uts: host
    command: ["--config=/etc/otel/config.yaml"]
    environment:
      OBZLY_API_KEY: "${apiKey}"
    volumes:
      - ./otel-collector-config.yaml:/etc/otel/config.yaml:ro
      - /var/lib/docker/containers:/var/lib/docker/containers:ro
      - /var/run/docker.sock:/var/run/docker.sock:ro
      # The host's /proc, /sys and filesystems, for host_metrics.
      - /:/hostfs:ro
      # Log read offsets, so a restart or upgrade neither skips nor re-sends lines.
      - obzly-otel-storage:/var/lib/otelcol
    ports:
      - "${publish}4317:4317"
      - "${publish}4318:4318"

volumes:
  obzly-otel-storage:
`;

    return {
      // Kept for callers that only look at the single filename/content -
      // `files` below is what the modal actually renders.
      filename: 'otel-collector-config.yaml',
      language: 'yaml',
      content: configYaml,
      files: [
        { filename: 'otel-collector-config.yaml', language: 'yaml', content: configYaml },
        { filename: 'docker-compose.yml', language: 'yaml', content: composeYaml },
      ],
      instructions:
        'Save both files next to each other, then run: docker compose up -d otel-collector. ' +
        'Point other containers’ OTEL_EXPORTER_OTLP_ENDPOINT at http://otel-collector:4318 ' +
        '(or :4317 for gRPC) if they’re on the same Docker network. Keep the two files separate ' +
        '- don’t paste otel-collector-config.yaml’s contents into docker-compose.yml, its ' +
        '${env:OBZLY_API_KEY} is OTel Collector’s own env-var syntax and will break Docker ' +
        'Compose’s interpolation if it ends up there.',
    };
  },
};
