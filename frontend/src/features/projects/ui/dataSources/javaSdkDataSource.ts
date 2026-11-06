// Source: https://opentelemetry.io/docs/zero-code/java/agent/getting-started/
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
import type { DataSourceBuildContext, DataSourceConfigResult, DataSourceDefinition } from './types';

const AGENT_URL =
  'https://github.com/open-telemetry/opentelemetry-java-instrumentation/releases/latest/download/opentelemetry-javaagent.jar';

/** Shared with the Kotlin entry: Kotlin on the JVM uses the same agent. */
export function buildJavaAgentConfig(
  { baseUrl, apiKey, fieldValues }: DataSourceBuildContext,
  defaultServiceName: string,
): DataSourceConfigResult {
  const common = readCommon(fieldValues, defaultServiceName);
  const mode = readRunMode(fieldValues);
  const env = otelEnv({ baseUrl, apiKey, common });

  const install = `# 1. Download the OpenTelemetry Java agent (latest release):
curl -fsSL -o opentelemetry-javaagent.jar ${AGENT_URL}

# 2. Start the JVM with the agent; it reads the OTEL_* env vars:
java -javaagent:./opentelemetry-javaagent.jar -jar myapp.jar
# Or, when you can't change the java command line:
# export JAVA_TOOL_OPTIONS="-javaagent:/path/to/opentelemetry-javaagent.jar"
`;

  const rendered = renderEnvFiles({
    mode,
    env,
    shellIntro: 'Load before starting the JVM (source obzly-otel.env).',
    dockerfilePreamble: [
      // Remote ADD defaults to mode 600, unreadable for a non-root app user.
      `ADD --chmod=644 ${AGENT_URL} /otel/opentelemetry-javaagent.jar`,
      'ENV JAVA_TOOL_OPTIONS="-javaagent:/otel/opentelemetry-javaagent.jar"',
    ],
  });

  const files =
    mode === 'docker'
      ? rendered.files
      : [{ filename: 'install.sh', language: 'bash', content: install }, ...rendered.files];

  const k8sHint =
    mode === 'kubernetes'
      ? ' Bake the agent into the image (see the Dockerfile option) or let the OpenTelemetry ' +
        'Operator entry inject it.'
      : '';

  return multiFileResult(
    files,
    `${rendered.howTo}${k8sHint} The agent instruments common frameworks with no code changes ` +
      `and exports over OTLP/HTTP to Obzly. ${TEMPORALITY_NOTE}`,
  );
}

export const javaSdkDataSource: DataSourceDefinition = {
  id: 'lang-java',
  name: 'Java',
  category: 'Languages',
  description:
    'OpenTelemetry Java agent: traces, metrics and logs from any JVM app, no code changes.',
  fields: [
    ...commonFields('my-java-service', { traces: 'stable', metrics: 'stable', logs: 'stable' }),
    RUN_MODE_FIELD,
  ],
  build: (ctx) => buildJavaAgentConfig(ctx, 'my-java-service'),
};
