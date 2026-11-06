// Source: https://opentelemetry.io/docs/languages/kotlin/getting-started/ , .../kotlin/configuration/
// and the opentelemetry-kotlin v0.9.0 example app. The docs' exporter
// snippet predates 0.9.0, whose exporters take an { endpoint = ... } block.
import {
  GATEWAY_RUN_INSTRUCTIONS,
  GATEWAY_URL_FIELD,
  gatewayFiles,
  readGatewayUrl,
} from './clientGateway';
import { buildJavaAgentConfig } from './javaSdkDataSource';
import {
  RUN_MODE_FIELD,
  type Signal,
  commonFields,
  multiFileResult,
  readCommon,
} from './languageSdkCommon';
import type { DataSourceDefinition } from './types';

const SERVICE = 'my-kotlin-service';
// The docs leave the version to fill in; this is the latest on Maven Central when written.
const KOTLIN_SDK_VERSION = '0.9.0';

const GRADLE = `// build.gradle.kts of the module that initializes the SDK
dependencies {
    val otelKotlinVersion = "${KOTLIN_SDK_VERSION}"
    implementation("io.opentelemetry.kotlin:core:$otelKotlinVersion")
    implementation("io.opentelemetry.kotlin:implementation:$otelKotlinVersion")
    implementation("io.opentelemetry.kotlin:exporters-otlp:$otelKotlinVersion")
}

// The API is experimental and needs an opt-in:
kotlin.compilerOptions {
    optIn.add("io.opentelemetry.kotlin.ExperimentalApi")
}
`;

function telemetryKt(
  gatewayUrl: string,
  serviceName: string,
  environment: string,
  signals: Signal[],
): string {
  const imports = ['import io.opentelemetry.kotlin.OpenTelemetry'];
  imports.push('import io.opentelemetry.kotlin.createOpenTelemetry');
  if (signals.includes('logs'))
    imports.push(
      'import io.opentelemetry.kotlin.logging.export.batchLogRecordProcessor',
      'import io.opentelemetry.kotlin.logging.export.otlpHttpLogRecordExporter',
    );
  if (signals.includes('traces'))
    imports.push(
      'import io.opentelemetry.kotlin.tracing.export.batchSpanProcessor',
      'import io.opentelemetry.kotlin.tracing.export.otlpHttpSpanExporter',
    );
  const blocks: string[] = [];
  if (signals.includes('traces'))
    blocks.push(`    tracerProvider {
        export { batchSpanProcessor(otlpHttpSpanExporter { endpoint = GATEWAY_URL }) }
    }`);
  if (signals.includes('logs'))
    blocks.push(`    loggerProvider {
        export { batchLogRecordProcessor(otlpHttpLogRecordExporter { endpoint = GATEWAY_URL }) }
    }`);
  return `// Telemetry.kt - create once at start-up and pass it to the code you instrument.
// Sends to YOUR gateway, which adds the Obzly API key server-side.
// Never put an API key in an app: anyone can extract it from the binary.
${imports.join('\n')}

private const val GATEWAY_URL = ${JSON.stringify(gatewayUrl)}

val otel: OpenTelemetry = createOpenTelemetry {
    serviceName = ${JSON.stringify(serviceName)}
    resource(mapOf("deployment.environment.name" to ${JSON.stringify(environment)}))
${blocks.join('\n')}
}
`;
}

export const kotlinSdkDataSource: DataSourceDefinition = {
  id: 'lang-kotlin',
  name: 'Kotlin',
  category: 'Languages',
  description:
    'Kotlin on the JVM via the Java agent, or the Kotlin Multiplatform SDK (in development).',
  fields: [
    ...commonFields(SERVICE, { traces: 'stable', metrics: 'stable', logs: 'stable' }),
    {
      key: 'target',
      label: 'Where it runs',
      defaultValue: 'jvm',
      options: [
        { value: 'jvm', label: 'JVM server (Java agent)' },
        { value: 'kmp', label: 'Android / iOS / Multiplatform (Kotlin SDK)' },
      ],
      helpText:
        'Kotlin on the JVM is covered by the Java agent - same output as the Java entry. The ' +
        'Kotlin Multiplatform SDK is in development (breaking changes possible) and sends traces ' +
        'and logs only, through a gateway so the app never holds the API key.',
    },
    RUN_MODE_FIELD,
    GATEWAY_URL_FIELD,
  ],
  build: (ctx) => {
    if (ctx.fieldValues.target !== 'kmp') return buildJavaAgentConfig(ctx, SERVICE);

    const { baseUrl, apiKey, fieldValues } = ctx;
    const common = readCommon(fieldValues, SERVICE, ['traces', 'logs']);
    const gatewayUrl = readGatewayUrl(fieldValues);
    const client =
      common.signals.length > 0
        ? [
            { filename: 'build.gradle.kts', language: 'kotlin', content: GRADLE },
            {
              filename: 'Telemetry.kt',
              language: 'kotlin',
              content: telemetryKt(
                gatewayUrl,
                common.serviceName,
                common.environment,
                common.signals,
              ),
            },
          ]
        : [];
    return multiFileResult(
      [...gatewayFiles(baseUrl, apiKey), ...client],
      `The app never gets the API key: Telemetry.kt posts to the gateway, and only the gateway ` +
        `(a collector you run) talks to Obzly with the key. ${GATEWAY_RUN_INSTRUCTIONS} "How you ` +
        `run it" only applies to the JVM option; metrics aren't exported by the Kotlin SDK yet.`,
    );
  },
};
