// Source: https://opentelemetry.io/docs/zero-code/python/ and .../python/configuration/
import {
  RUN_MODE_FIELD,
  TEMPORALITY_NOTE,
  commonFields,
  multiFileResult,
  otelEnv,
  readCommon,
  readRunMode,
  renderEnvFiles,
} from './languageSdkCommon';
import type { DataSourceDefinition } from './types';

const SERVICE = 'my-python-service';

export const pythonSdkDataSource: DataSourceDefinition = {
  id: 'lang-python',
  name: 'Python',
  category: 'Languages',
  description:
    'opentelemetry-instrument: zero-code traces and metrics (logs optional) from Python apps.',
  fields: [
    ...commonFields(SERVICE, { traces: 'stable', metrics: 'stable', logs: 'development' }),
    RUN_MODE_FIELD,
  ],
  build: ({ baseUrl, apiKey, fieldValues }) => {
    const common = readCommon(fieldValues, SERVICE);
    const mode = readRunMode(fieldValues);
    // Logging auto-instrumentation is on by default from opentelemetry-python
    // 1.40.0; older releases need this flag.
    const extra: [string, string][] = common.signals.includes('logs')
      ? [['OTEL_PYTHON_LOGGING_AUTO_INSTRUMENTATION_ENABLED', 'true']]
      : [];
    const env = otelEnv({ baseUrl, apiKey, common, extra });

    const install = `# 1. Install the distro and OTLP exporter, then the instrumentations
#    for the libraries this environment has installed:
pip install opentelemetry-distro opentelemetry-exporter-otlp
opentelemetry-bootstrap -a install

# 2. Start the app through opentelemetry-instrument (reads the OTEL_* env vars):
opentelemetry-instrument python myapp.py
`;

    const rendered = renderEnvFiles({
      mode,
      env,
      shellIntro: 'Load before starting the app (source obzly-otel.env).',
      dockerfilePreamble: [
        'RUN pip install opentelemetry-distro opentelemetry-exporter-otlp \\',
        '    && opentelemetry-bootstrap -a install',
      ],
      dockerfileCommand: ['CMD ["opentelemetry-instrument", "python", "myapp.py"]'],
    });

    const files =
      mode === 'docker'
        ? rendered.files
        : [{ filename: 'install.sh', language: 'bash', content: install }, ...rendered.files];

    return multiFileResult(
      files,
      `${rendered.howTo} Run opentelemetry-bootstrap after installing your app's dependencies so ` +
        `it picks up their instrumentations. Gunicorn with several workers can break metrics: see ` +
        `"Pre-fork server issues" in the OpenTelemetry Python troubleshooting docs. ${TEMPORALITY_NOTE}`,
    );
  },
};
