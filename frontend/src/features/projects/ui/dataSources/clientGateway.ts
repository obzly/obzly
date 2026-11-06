// Client-side apps (browsers, mobile and desktop apps) can't hold a project
// API key: anyone can read it out of the bundle or binary. They send OTLP to
// a small collector gateway instead, which adds the key server-side. Obzly's
// ingest also sends no CORS headers outside development mode, so browsers
// couldn't post to it directly anyway.
import {
  BATCH_PROCESSOR_YAML,
  COLLECTOR_IMAGE,
  MEMORY_LIMITER_PROCESSOR_YAML,
  otlpHttpExporterYaml,
} from './otelCollectorCommon';
import type { DataSourceConfigFile, DataSourceField } from './types';

export const GATEWAY_URL_FIELD: DataSourceField = {
  key: 'gatewayUrl',
  label: 'Gateway URL (as your users reach it)',
  defaultValue: 'https://otel-gateway.example.com',
  helpText:
    'The public HTTPS address of the collector gateway generated below - the app sends ' +
    'telemetry here, never to Obzly directly. The gateway holds the API key; the app never does. ' +
    'It has to listen publicly, so put it behind TLS (your reverse proxy or load balancer).',
};

export const ALLOWED_ORIGINS_FIELD: DataSourceField = {
  key: 'allowedOrigins',
  label: 'Allowed origins (CORS)',
  defaultValue: 'https://app.example.com',
  placeholder: 'https://app.example.com, https://*.example.com',
  helpText:
    'Comma-separated origins of the web app pages allowed to send telemetry. Keep this to your ' +
    'own sites - the gateway is public, and CORS is what stops other sites posting from browsers.',
};

export function readGatewayUrl(fieldValues: Record<string, string>): string {
  return (fieldValues.gatewayUrl?.trim() || 'https://otel-gateway.example.com').replace(/\/+$/, '');
}

export function readAllowedOrigins(fieldValues: Record<string, string>): string[] {
  const origins = (fieldValues.allowedOrigins ?? '')
    .split(',')
    .map((origin) => origin.trim())
    .filter(Boolean);
  return origins.length > 0 ? origins : ['https://app.example.com'];
}

export const GATEWAY_ENV_FILE = 'obzly-gateway.env';
export const GATEWAY_CONFIG_FILE = 'otel-gateway.yaml';

/**
 * Collector config for the gateway. No resource detection: it would stamp the
 * gateway's own host onto every client's telemetry.
 */
export function gatewayCollectorYaml(baseUrl: string, allowedOrigins?: string[]): string {
  const cors = allowedOrigins
    ? `
        cors:
          allowed_origins:
${allowedOrigins.map((origin) => `            - ${JSON.stringify(origin)}`).join('\n')}
          max_age: 7200`
    : '';
  return `# Obzly OTLP gateway for client-side telemetry. Clients post OTLP/HTTP here;
# the collector forwards it to Obzly with the API key from OBZLY_API_KEY.
# Put it behind your HTTPS reverse proxy or load balancer.

receivers:
  otlp:
    protocols:
      http:
        endpoint: 0.0.0.0:4318${cors}

processors:
  # Public endpoint: bound memory so a burst of clients can't exhaust it.
${MEMORY_LIMITER_PROCESSOR_YAML}
${BATCH_PROCESSOR_YAML}

exporters:
${otlpHttpExporterYaml(baseUrl)}

extensions:
  health_check:
    endpoint: 0.0.0.0:13133

service:
  extensions: [health_check]
  pipelines:
    traces:
      receivers: [otlp]
      processors: [memory_limiter, batch]
      exporters: [otlp_http]
    metrics:
      receivers: [otlp]
      processors: [memory_limiter, batch]
      exporters: [otlp_http]
    logs:
      receivers: [otlp]
      processors: [memory_limiter, batch]
      exporters: [otlp_http]
`;
}

/** The gateway config plus the env file that holds the key (the only place it appears). */
export function gatewayFiles(
  baseUrl: string,
  apiKey: string,
  allowedOrigins?: string[],
): DataSourceConfigFile[] {
  return [
    {
      filename: GATEWAY_CONFIG_FILE,
      language: 'yaml',
      content: gatewayCollectorYaml(baseUrl, allowedOrigins),
    },
    {
      filename: GATEWAY_ENV_FILE,
      language: 'bash',
      content: `# Server-side only - never ship this file with the app.\nOBZLY_API_KEY=${apiKey}\n`,
    },
  ];
}

export const GATEWAY_RUN_INSTRUCTIONS =
  `Run the gateway on a server: docker run -d --name obzly-otel-gateway -p 4318:4318 ` +
  `--env-file ${GATEWAY_ENV_FILE} -v "$PWD/${GATEWAY_CONFIG_FILE}:/etc/otelcol-contrib/config.yaml:ro" ` +
  `${COLLECTOR_IMAGE}, and publish it over HTTPS at the gateway URL. The API key stays in ` +
  `${GATEWAY_ENV_FILE} on that server; the app code only knows the gateway URL. Anyone can post ` +
  `to a public gateway, so use a dedicated key and rate-limit it at your proxy.`;
