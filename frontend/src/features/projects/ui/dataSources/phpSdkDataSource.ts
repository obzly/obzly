// Source: https://opentelemetry.io/docs/zero-code/php/auto/ and
// https://opentelemetry.io/docs/languages/php/exporters/
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

const SERVICE = 'my-php-service';

const FRAMEWORK_PACKAGES: Record<string, string[]> = {
  laravel: ['open-telemetry/opentelemetry-auto-laravel'],
  symfony: ['open-telemetry/opentelemetry-auto-symfony'],
  slim: ['open-telemetry/opentelemetry-auto-slim', 'open-telemetry/opentelemetry-auto-psr18'],
  none: [],
};

export const phpSdkDataSource: DataSourceDefinition = {
  id: 'lang-php',
  name: 'PHP',
  category: 'Languages',
  description:
    'OpenTelemetry PHP extension plus auto-instrumentation packages: zero-code traces, metrics, logs.',
  fields: [
    ...commonFields(SERVICE, { traces: 'stable', metrics: 'stable', logs: 'stable' }),
    {
      key: 'framework',
      label: 'Framework',
      defaultValue: 'laravel',
      options: [
        { value: 'laravel', label: 'Laravel' },
        { value: 'symfony', label: 'Symfony' },
        { value: 'slim', label: 'Slim (+ PSR-18 client)' },
        { value: 'none', label: 'Other (pick packages yourself)' },
      ],
      helpText:
        'Adds that auto-instrumentation package. More (PDO, Guzzle, ...) are listed on Packagist ' +
        'under open-telemetry/opentelemetry-auto-*.',
    },
    RUN_MODE_FIELD,
  ],
  build: ({ baseUrl, apiKey, fieldValues }) => {
    const common = readCommon(fieldValues, SERVICE);
    const mode = readRunMode(fieldValues);
    const packages = FRAMEWORK_PACKAGES[fieldValues.framework] ?? FRAMEWORK_PACKAGES.none;
    const env = otelEnv({
      baseUrl,
      apiKey,
      common,
      extra: [
        ['OTEL_PHP_AUTOLOAD_ENABLED', 'true'],
        ['OTEL_PROPAGATORS', 'baggage,tracecontext'],
      ],
    });

    // The OTLP exporter needs a PSR-18 HTTP client; guzzle7-adapter provides one.
    const composer = [
      'open-telemetry/sdk',
      'open-telemetry/exporter-otlp',
      'php-http/guzzle7-adapter',
      ...packages,
    ];
    const install = `# 1. Build and enable the extension (needs gcc, make, autoconf):
pecl install opentelemetry
# then add to php.ini:
#   [opentelemetry]
#   extension=opentelemetry.so
php -m | grep opentelemetry

# 2. Add the SDK, the OTLP exporter and auto-instrumentation packages:
composer require \\
  ${composer.join(' \\\n  ')}
`;

    const rendered = renderEnvFiles({
      mode,
      env,
      shellIntro: 'Load in the environment of PHP-FPM or the CLI (source obzly-otel.env).',
      dockerfilePreamble: [
        'ADD --chmod=0755 https://github.com/mlocati/docker-php-extension-installer/releases/latest/download/install-php-extensions /usr/local/bin/',
        'RUN install-php-extensions opentelemetry',
        '# Run where composer is available, e.g. your build stage:',
        `RUN composer require ${composer.join(' ')}`,
      ],
    });

    const files =
      mode === 'docker'
        ? rendered.files
        : [{ filename: 'install.sh', language: 'bash', content: install }, ...rendered.files];

    return multiFileResult(
      files,
      `${rendered.howTo} PHP-FPM only passes env vars listed in the pool config (env[NAME] = ` +
        `value, or clear_env = no). Logs need a logging bridge such as ` +
        `open-telemetry/opentelemetry-logger-monolog. ${TEMPORALITY_NOTE}`,
    );
  },
};
