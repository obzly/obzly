// Source: https://opentelemetry.io/docs/languages/js/getting-started/browser/ and
// https://opentelemetry.io/docs/languages/js/exporters/#usage-in-the-browser.
// The code targets the JS SDK 3.x packages the docs now use.
import {
  ALLOWED_ORIGINS_FIELD,
  GATEWAY_RUN_INSTRUCTIONS,
  GATEWAY_URL_FIELD,
  gatewayFiles,
  readAllowedOrigins,
  readGatewayUrl,
} from './clientGateway';
import { commonFields, multiFileResult, readCommon } from './languageSdkCommon';
import type { DataSourceDefinition } from './types';

const SERVICE = 'my-web-app';

const INSTALL = `npm install @opentelemetry/api \\
  @opentelemetry/core \\
  @opentelemetry/resources \\
  @opentelemetry/sdk-trace \\
  @opentelemetry/context-zone \\
  @opentelemetry/instrumentation \\
  @opentelemetry/auto-instrumentations-web \\
  @opentelemetry/exporter-trace-otlp-http
`;

function tracingJs(gatewayUrl: string, serviceName: string, environment: string): string {
  return `// tracing.js - import once, before the rest of your app.
// Sends to YOUR gateway, which adds the Obzly API key server-side.
// Never put an API key in browser code: anyone can read it from the bundle.
import { context, propagation, trace } from '@opentelemetry/api';
import {
  CompositePropagator,
  W3CBaggagePropagator,
  W3CTraceContextPropagator,
} from '@opentelemetry/core';
import { resourceFromAttributes } from '@opentelemetry/resources';
import { BatchSpanProcessor, TracerProvider } from '@opentelemetry/sdk-trace';
import { ZoneContextManager } from '@opentelemetry/context-zone';
import { registerInstrumentations } from '@opentelemetry/instrumentation';
import { getWebAutoInstrumentations } from '@opentelemetry/auto-instrumentations-web';
import { OTLPTraceExporter } from '@opentelemetry/exporter-trace-otlp-http';

const tracerProvider = new TracerProvider({
  resource: resourceFromAttributes({
    'service.name': ${JSON.stringify(serviceName)},
    'deployment.environment.name': ${JSON.stringify(environment)},
  }),
  spanProcessors: [
    new BatchSpanProcessor({
      exporter: new OTLPTraceExporter({ url: ${JSON.stringify(`${gatewayUrl}/v1/traces`)} }),
    }),
  ],
});
trace.setGlobalTracerProvider(tracerProvider);
context.setGlobalContextManager(new ZoneContextManager().enable());
propagation.setGlobalPropagator(
  new CompositePropagator({
    propagators: [new W3CTraceContextPropagator(), new W3CBaggagePropagator()],
  }),
);

// Document load, fetch/XHR and user interaction spans. To link them to your
// backend traces, set propagateTraceHeaderCorsUrls on the fetch and XHR
// instrumentations (and allow the traceparent header in your API's CORS).
registerInstrumentations({
  instrumentations: [getWebAutoInstrumentations()],
});
`;
}

export const browserJsSdkDataSource: DataSourceDefinition = {
  id: 'lang-browser-js',
  name: 'Browser JavaScript',
  category: 'Languages',
  description:
    'Web app traces via the OpenTelemetry browser SDK, sent through your own collector gateway.',
  fields: [
    ...commonFields(SERVICE, { traces: 'stable' }),
    GATEWAY_URL_FIELD,
    ALLOWED_ORIGINS_FIELD,
  ],
  build: ({ baseUrl, apiKey, fieldValues }) => {
    const common = readCommon(fieldValues, SERVICE, ['traces']);
    const gatewayUrl = readGatewayUrl(fieldValues);
    const origins = readAllowedOrigins(fieldValues);
    const client = common.signals.includes('traces')
      ? [
          { filename: 'install.sh', language: 'bash', content: INSTALL },
          {
            filename: 'tracing.js',
            language: 'javascript',
            content: tracingJs(gatewayUrl, common.serviceName, common.environment),
          },
        ]
      : [];
    return multiFileResult(
      [...gatewayFiles(baseUrl, apiKey, origins), ...client],
      `The browser never gets the API key: tracing.js posts to the gateway, and only the ` +
        `gateway (a collector you run) talks to Obzly with the key. Obzly's ingest doesn't answer ` +
        `CORS, so browsers can't post to it directly anyway. ${GATEWAY_RUN_INSTRUCTIONS} If your ` +
        `site sets a Content-Security-Policy, add the gateway origin to connect-src. Browser ` +
        `instrumentation is still experimental upstream.`,
    );
  },
};
