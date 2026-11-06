// Shared pieces of the "Languages" entries: OpenTelemetry SDKs and agents
// export OTLP/HTTP straight to Obzly, configured by the standard OTEL_*
// env vars, so the key lives in the environment (or a Secret), never in code.
// Obzly serves POST /v1/{traces,metrics,logs} at the server root; SDKs
// append those paths to OTEL_EXPORTER_OTLP_ENDPOINT themselves.
import type { DataSourceConfigFile, DataSourceConfigResult, DataSourceField } from './types';

type DataSourceFieldsValues = Record<string, string>;

export type Signal = 'traces' | 'metrics' | 'logs';

export const ALL_SIGNALS: Signal[] = ['traces', 'metrics', 'logs'];

const SIGNAL_LABELS: Record<Signal, string> = {
  traces: 'Send traces',
  metrics: 'Send metrics',
  logs: 'Send logs',
};

export type SignalStatus = 'stable' | 'rc' | 'beta' | 'development';

const STATUS_TEXT: Record<SignalStatus, string> = {
  stable: 'stable',
  rc: 'release candidate',
  beta: 'beta',
  development: 'in development',
};

/** Service name, environment and one checkbox per supported signal (on when stable). */
export function commonFields(
  defaultServiceName: string,
  signals: Partial<Record<Signal, SignalStatus>>,
): DataSourceField[] {
  const fields: DataSourceField[] = [
    {
      key: 'serviceName',
      label: 'Service name',
      defaultValue: defaultServiceName,
      helpText: 'OTEL_SERVICE_NAME - how this service shows up in traces and the Service Map.',
    },
    {
      key: 'environment',
      label: 'Deployment environment',
      defaultValue: 'production',
      helpText: 'Sent as the deployment.environment.name resource attribute.',
    },
  ];
  for (const signal of ALL_SIGNALS) {
    const status = signals[signal];
    if (!status) continue;
    fields.push({
      key: signalKey(signal),
      label: SIGNAL_LABELS[signal],
      type: 'checkbox',
      defaultValue: status === 'stable' ? 'yes' : 'no',
      helpText: `${capitalize(signal)} support in this SDK is ${STATUS_TEXT[status]}.`,
    });
  }
  return fields;
}

export function signalKey(signal: Signal): string {
  return `send${capitalize(signal)}`;
}

function capitalize(value: string): string {
  return value.charAt(0).toUpperCase() + value.slice(1);
}

export interface CommonValues {
  serviceName: string;
  environment: string;
  signals: Signal[];
}

export function readCommon(
  fieldValues: DataSourceFieldsValues,
  defaultServiceName: string,
  supported: Signal[] = ALL_SIGNALS,
): CommonValues {
  return {
    serviceName: fieldValues.serviceName?.trim() || defaultServiceName,
    environment: fieldValues.environment?.trim() || 'production',
    signals: supported.filter((signal) => fieldValues[signalKey(signal)] === 'yes'),
  };
}

export const HEADERS_ENV = 'OTEL_EXPORTER_OTLP_HEADERS';

export interface OtelEnvOptions {
  baseUrl: string;
  apiKey: string;
  common: CommonValues;
  /** Signals this SDK can export; the others are left unset. */
  supported?: Signal[];
  /** False for SDKs whose protocol env value differs from the spec (Erlang). */
  setProtocol?: boolean;
  /** Extra non-secret vars, appended after the standard ones. */
  extra?: [string, string][];
}

/** The standard OTel env vars, in a stable order. The key is only in HEADERS_ENV. */
export function otelEnv(options: OtelEnvOptions): [string, string][] {
  const { baseUrl, apiKey, common } = options;
  const supported = options.supported ?? ALL_SIGNALS;
  const env: [string, string][] = [
    ['OTEL_SERVICE_NAME', common.serviceName],
    ['OTEL_RESOURCE_ATTRIBUTES', `deployment.environment.name=${common.environment}`],
    ['OTEL_EXPORTER_OTLP_ENDPOINT', baseUrl],
  ];
  if (options.setProtocol !== false) env.push(['OTEL_EXPORTER_OTLP_PROTOCOL', 'http/protobuf']);
  env.push([HEADERS_ENV, `X-API-Key=${apiKey}`]);
  for (const signal of supported) {
    env.push([
      `OTEL_${signal.toUpperCase()}_EXPORTER`,
      common.signals.includes(signal) ? 'otlp' : 'none',
    ]);
  }
  return [...env, ...(options.extra ?? [])];
}

export type RunMode = 'shell' | 'docker' | 'kubernetes';

export const RUN_MODE_FIELD: DataSourceField = {
  key: 'runMode',
  label: 'How you run it',
  defaultValue: 'shell',
  options: [
    { value: 'shell', label: 'Shell / systemd (env vars)' },
    { value: 'docker', label: 'Dockerfile' },
    { value: 'kubernetes', label: 'Kubernetes Deployment' },
  ],
  helpText:
    'Docker and Kubernetes keep the API key out of the image: an env file for docker run, or a ' +
    'Secret referenced from the Deployment.',
};

export function readRunMode(fieldValues: DataSourceFieldsValues): RunMode {
  const mode = fieldValues.runMode;
  return mode === 'docker' || mode === 'kubernetes' ? mode : 'shell';
}

export const TEMPORALITY_NOTE =
  'Sites older than v0.57.0 only accept cumulative metrics: if this SDK defaults to delta, also ' +
  'set OTEL_EXPORTER_OTLP_METRICS_TEMPORALITY_PREFERENCE=cumulative.';

function shellQuote(value: string): string {
  return `"${value.replace(/(["\\$`])/g, '\\$1')}"`;
}

function yamlQuote(value: string): string {
  return JSON.stringify(value);
}

export function k8sName(serviceName: string): string {
  const name = serviceName
    .toLowerCase()
    .replace(/[^a-z0-9-]+/g, '-')
    .replace(/^-+|-+$/g, '')
    .slice(0, 40);
  return name || 'app';
}

export interface EnvFilesOptions {
  mode: RunMode;
  env: [string, string][];
  /** Lines added before the env block, e.g. installing the agent (Dockerfile only). */
  dockerfilePreamble?: string[];
  /** The Dockerfile's last lines (CMD/ENTRYPOINT). */
  dockerfileCommand?: string[];
  /** Comment shown at the top of the shell file. */
  shellIntro?: string;
}

export interface EnvFilesResult {
  files: DataSourceConfigFile[];
  /** How to use the files, for the instructions text. */
  howTo: string;
}

/** Renders the env vars for the chosen run mode; the API key ends up in exactly one file. */
export function renderEnvFiles(options: EnvFilesOptions): EnvFilesResult {
  const { mode, env } = options;
  const secret = env.filter(([key]) => key === HEADERS_ENV);
  const plain = env.filter(([key]) => key !== HEADERS_ENV);

  if (mode === 'docker') {
    const dockerfile = [
      '# Add to your Dockerfile. The API key is not baked in: it comes from',
      '# obzly-otel.env at run time.',
      ...(options.dockerfilePreamble ?? []),
      ...plain.map(([key, value]) => `ENV ${key}=${shellQuote(value)}`),
      ...(options.dockerfileCommand ?? []),
    ].join('\n');
    return {
      files: [
        { filename: 'Dockerfile', language: 'docker', content: `${dockerfile}\n` },
        {
          filename: 'obzly-otel.env',
          language: 'bash',
          content: secret.map(([key, value]) => `${key}=${value}`).join('\n') + '\n',
        },
      ],
      howTo: 'Run the container with: docker run --env-file obzly-otel.env <your-image>.',
    };
  }

  if (mode === 'kubernetes') {
    const name = `${k8sName(serviceNameOf(env))}-obzly-otel`;
    const secretYaml = [
      'apiVersion: v1',
      'kind: Secret',
      'metadata:',
      `  name: ${name}`,
      'type: Opaque',
      'stringData:',
      ...secret.map(([key, value]) => `  ${key}: ${yamlQuote(value)}`),
    ].join('\n');
    const envYaml = [
      '# Merge into spec.template.spec.containers[] of your Deployment.',
      'env:',
      ...plain.flatMap(([key, value]) => [`  - name: ${key}`, `    value: ${yamlQuote(value)}`]),
      ...secret.flatMap(([key]) => [
        `  - name: ${key}`,
        '    valueFrom:',
        '      secretKeyRef:',
        `        name: ${name}`,
        `        key: ${key}`,
      ]),
    ].join('\n');
    return {
      files: [
        { filename: 'obzly-otel-secret.yaml', language: 'yaml', content: `${secretYaml}\n` },
        { filename: 'deployment-env.yaml', language: 'yaml', content: `${envYaml}\n` },
      ],
      howTo:
        'Create the Secret in the app namespace (kubectl apply -n <namespace> -f ' +
        'obzly-otel-secret.yaml), then add the env block to your Deployment container.',
    };
  }

  const shell = [
    ...(options.shellIntro ? [`# ${options.shellIntro}`] : []),
    ...env.map(([key, value]) => `export ${key}=${shellQuote(value)}`),
  ].join('\n');
  return {
    files: [{ filename: 'obzly-otel.env', language: 'bash', content: `${shell}\n` }],
    howTo: 'Load the env vars (source obzly-otel.env) in the shell or unit that starts the app.',
  };
}

function serviceNameOf(env: [string, string][]): string {
  return env.find(([key]) => key === 'OTEL_SERVICE_NAME')?.[1] ?? 'app';
}

/** A result made of several files; the single-file fields mirror the first one. */
export function multiFileResult(
  files: DataSourceConfigFile[],
  instructions: string,
): DataSourceConfigResult {
  const [first] = files;
  return {
    filename: first.filename,
    language: first.language,
    content: first.content,
    files,
    instructions,
  };
}
