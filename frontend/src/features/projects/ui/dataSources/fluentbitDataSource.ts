import type { DataSourceDefinition } from './types';

export const fluentbitDataSource: DataSourceDefinition = {
  id: 'fluent-bit',
  name: 'Fluent Bit',
  category: 'Custom',
  description:
    'Already running Fluent Bit? Point its built-in OpenTelemetry output plugin straight at Obzly.',
  fields: [],
  build: ({ baseUrl, apiKey }) => {
    // A path prefix (Obzly behind a sub-path) goes in front of each URI.
    const url = new URL(/^https?:\/\//.test(baseUrl) ? baseUrl : `https://${baseUrl}`);
    const isHttps = url.protocol === 'https:';
    const host = url.hostname;
    const port = url.port || (isHttps ? '443' : '80');
    const prefix = url.pathname.replace(/\/+$/, '');

    const content = `# Obzly config for Fluent Bit's built-in OpenTelemetry output plugin -
# no separate collector needed, Fluent Bit exports OTLP/HTTP directly.
# Add this [OUTPUT] block to your existing fluent-bit.conf (alongside
# whatever [INPUT] blocks you already have).

[OUTPUT]
    Name              opentelemetry
    Match             *
    Host              ${host}
    Port              ${port}
    ${isHttps ? 'Tls               On\n    ' : ''}Logs_uri          ${prefix}/v1/logs
    Traces_uri        ${prefix}/v1/traces
    Metrics_uri       ${prefix}/v1/metrics
    Header            X-API-Key ${apiKey}
    Log_response_payload true
`;

    return {
      filename: 'fluent-bit-output.conf',
      language: 'ini',
      content,
      instructions:
        'Add this [OUTPUT] block to your fluent-bit.conf and restart Fluent Bit. If you use the ' +
        'YAML config format instead, the same keys apply under an "outputs:" entry with name: opentelemetry.',
    };
  },
};
