// Source: https://opentelemetry.io/docs/languages/ruby/getting-started/ and
// https://opentelemetry.io/docs/languages/ruby/exporters/
import {
  RUN_MODE_FIELD,
  commonFields,
  multiFileResult,
  otelEnv,
  readCommon,
  readRunMode,
  renderEnvFiles,
} from './languageSdkCommon';
import type { DataSourceDefinition } from './types';

const SERVICE = 'my-ruby-service';

// Ruby metrics and logs are in development and not covered by the docs yet,
// so this entry sends traces only.
const INITIALIZER = `# config/initializers/opentelemetry.rb (Rails), or as early as possible at
# start-up for other apps. Service name, endpoint and API key come from the
# OTEL_* env vars.
require 'opentelemetry/sdk'
require 'opentelemetry/instrumentation/all'
require 'opentelemetry-exporter-otlp'

OpenTelemetry::SDK.configure do |c|
  c.use_all() # enables all instrumentation!
end
`;

export const rubySdkDataSource: DataSourceDefinition = {
  id: 'lang-ruby',
  name: 'Ruby',
  category: 'Languages',
  description:
    'opentelemetry-sdk with instrumentation-all: traces from Rails, Sinatra and common gems.',
  fields: [...commonFields(SERVICE, { traces: 'stable' }), RUN_MODE_FIELD],
  build: ({ baseUrl, apiKey, fieldValues }) => {
    const common = readCommon(fieldValues, SERVICE, ['traces']);
    const mode = readRunMode(fieldValues);
    const env = otelEnv({ baseUrl, apiKey, common, supported: ['traces'] });
    const rendered = renderEnvFiles({
      mode,
      env,
      shellIntro: 'Load before starting the app (source obzly-otel.env).',
    });
    return multiFileResult(
      [
        {
          filename: 'install.sh',
          language: 'bash',
          content:
            'bundle add opentelemetry-sdk opentelemetry-exporter-otlp opentelemetry-instrumentation-all\n',
        },
        { filename: 'opentelemetry.rb', language: 'ruby', content: INITIALIZER },
        ...rendered.files,
      ],
      `${rendered.howTo} Ruby metrics and logs are still in development upstream, so only ` +
        `traces are set up here.`,
    );
  },
};
