// Source: https://opentelemetry.io/docs/zero-code/js/
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

const SERVICE = 'my-node-service';
const REGISTER = '--require @opentelemetry/auto-instrumentations-node/register';

export const nodejsSdkDataSource: DataSourceDefinition = {
  id: 'lang-nodejs',
  name: 'Node.js',
  category: 'Languages',
  description:
    'auto-instrumentations-node: zero-code traces and metrics (logs optional) from Node.js apps.',
  fields: [
    ...commonFields(SERVICE, { traces: 'stable', metrics: 'stable', logs: 'development' }),
    RUN_MODE_FIELD,
  ],
  build: ({ baseUrl, apiKey, fieldValues }) => {
    const common = readCommon(fieldValues, SERVICE);
    const mode = readRunMode(fieldValues);
    const env = otelEnv({
      baseUrl,
      apiKey,
      common,
      extra: [['NODE_OPTIONS', REGISTER]],
    });

    const install = `# 1. Add the API and the auto-instrumentation meta package to your app:
npm install --save @opentelemetry/api
npm install --save @opentelemetry/auto-instrumentations-node

# 2. Start the app as usual - NODE_OPTIONS (in obzly-otel.env) loads the
#    instrumentation first. Without NODE_OPTIONS:
node ${REGISTER} app.js
`;

    const rendered = renderEnvFiles({
      mode,
      env,
      shellIntro: 'Load before starting the app (source obzly-otel.env).',
      dockerfilePreamble: [
        '# After your app dependencies are installed:',
        'RUN npm install --save @opentelemetry/api @opentelemetry/auto-instrumentations-node',
      ],
    });

    const files =
      mode === 'docker'
        ? rendered.files
        : [{ filename: 'install.sh', language: 'bash', content: install }, ...rendered.files];

    return multiFileResult(
      files,
      `${rendered.howTo} The register hook instruments HTTP, Express, databases and more with no ` +
        `code changes. For a browser app use the Browser JavaScript entry instead. ${TEMPORALITY_NOTE}`,
    );
  },
};
