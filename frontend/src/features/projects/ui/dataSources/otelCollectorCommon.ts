// Shared building blocks for the OTel-Collector-based data source configs.
// The exporter is OTLP/HTTP everywhere: it uses the same host/port as the UI,
// so whatever TLS or reverse proxy fronts that URL just works. OTLP/gRPC
// (port 4317) may not be exposed, and is plaintext unless the operator set
// OTLP_GRPC_TLS_CERT_FILE/_KEY_FILE.
import type { DataSourceField } from './types';

export const COLLECTOR_VERSION = '0.162.0';
export const COLLECTOR_IMAGE = `otel/opentelemetry-collector-contrib:${COLLECTOR_VERSION}`;

export function otlpHttpExporterYaml(baseUrl: string): string {
  return `  otlp_http:
    endpoint: "${baseUrl}"
    headers:
      X-API-Key: "\${env:OBZLY_API_KEY}"
    # Uncomment if this endpoint uses a self-signed/internal CA cert:
    # tls:
    #   insecure_skip_verify: true`;
}

// The collector's own OTLP receiver has no authentication, so by default it
// only takes data from apps on the same machine.
export const OTLP_REMOTE_FIELD: DataSourceField = {
  key: 'otlpRemote',
  label: 'Accept OTLP from other machines',
  type: 'checkbox',
  defaultValue: 'no',
  helpText:
    'Off: only apps on this machine can send OTLP (127.0.0.1:4317/4318). On: the collector ' +
    'listens on every interface with no authentication, so firewall ports 4317 and 4318 to ' +
    'trusted networks.',
};

export function otlpReceiverYaml(fieldValues: Record<string, string>): string {
  const host = fieldValues.otlpRemote === 'yes' ? '0.0.0.0' : '127.0.0.1';
  return `  otlp:
    protocols:
      grpc:
        endpoint: ${host}:4317
      http:
        endpoint: ${host}:4318`;
}

// Obzly rejects a whole ingest request over MaxBatchSize (5000 records), and
// the batch processor has no hard cap by default, so busy sources exceeded it.
export const BATCH_PROCESSOR_YAML = `  batch:
    send_batch_size: 2500
    send_batch_max_size: 2500`;

// First in every pipeline: refuses data (the sender retries) before the
// collector runs out of memory.
export const MEMORY_LIMITER_PROCESSOR_YAML = `  memory_limiter:
    check_interval: 1s
    limit_mib: 512
    spike_limit_mib: 128`;

// Cloud/host resource attributes (cloud.*, host.id, host.name, os.type) that
// Infrastructure uses. Detectors that don't apply are skipped; env first so
// OTEL_RESOURCE_ATTRIBUTES wins, system last as the fallback.
export const RESOURCEDETECTION_PROCESSOR_YAML = `  resource_detection:
    detectors: [env, ec2, gcp, azure, system]
    timeout: 5s
    override: false`;

// Where tailed files' read offsets live, so a restart neither skips nor
// re-sends lines.
export const FILE_STORAGE_DIR = '/var/lib/otelcol/file_storage';
export const WINDOWS_FILE_STORAGE_DIR = 'C:\\ProgramData\\otelcol-contrib\\file_storage';

export const FILE_STORAGE_NOTE =
  `Read offsets are kept in ${FILE_STORAGE_DIR}: create it first, writable by the ` +
  `collector's user (e.g. sudo install -d -o "$USER" ${FILE_STORAGE_DIR}).`;

/**
 * The top-level extensions block: health_check always (a long-lived
 * service), file_storage when the config tails files.
 */
export function extensionsYaml(
  options: { fileStorageDir?: string; healthCheckEndpoint?: string } = {},
): string {
  const storage = options.fileStorageDir
    ? `
  file_storage:
    directory: '${options.fileStorageDir}'
    create_directory: true`
    : '';
  return `extensions:
  health_check:
    endpoint: ${options.healthCheckEndpoint ?? 'localhost:13133'}${storage}`;
}

/** The service.extensions line matching extensionsYaml. */
export function serviceExtensionsYaml(withFileStorage = false): string {
  return `  extensions: [health_check${withFileStorage ? ', file_storage' : ''}]`;
}
