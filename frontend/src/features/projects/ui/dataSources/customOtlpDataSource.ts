import type { DataSourceDefinition } from './types';

export const customOtlpDataSource: DataSourceDefinition = {
  id: 'custom-otlp',
  name: 'Custom (OTLP)',
  category: 'Custom',
  description: 'Any other OTLP-speaking source - point it directly at Obzly, no collector needed.',
  fields: [],
  build: ({ baseUrl, apiKey }) => {
    const content = `# Obzly accepts OTLP/HTTP directly - no collector required for a
# source that can already export OTLP itself (most language SDKs,
# Vector, Fluent Bit's OTLP output, Grafana Alloy, etc.).
#
# Logs:    POST ${baseUrl}/v1/logs
# Traces:  POST ${baseUrl}/v1/traces
# Metrics: POST ${baseUrl}/v1/metrics
#
# All three expect the standard OTLP/HTTP protobuf or JSON encoding, plus
# this header on every request:
#   X-API-Key: ${apiKey}
#
# Example env vars for anything using the standard OTel SDK conventions:

export OTEL_EXPORTER_OTLP_ENDPOINT="${baseUrl}"
export OTEL_EXPORTER_OTLP_HEADERS="X-API-Key=${apiKey}"
export OTEL_EXPORTER_OTLP_PROTOCOL="http/protobuf"

# Or with a raw curl request:

curl -X POST "${baseUrl}/v1/logs" \\
  -H "Content-Type: application/json" \\
  -H "X-API-Key: ${apiKey}" \\
  -d '{
    "resourceLogs": [{
      "resource": { "attributes": [{ "key": "service.name", "value": { "stringValue": "my-app" } }] },
      "scopeLogs": [{
        "logRecords": [{
          "timeUnixNano": "'"$(date +%s%N)"'",
          "severityText": "INFO",
          "body": { "stringValue": "Hello from curl" }
        }]
      }]
    }]
  }'
`;

    return {
      filename: 'obzly-otlp.env',
      language: 'bash',
      content,
      instructions:
        'Set these env vars (or the equivalent for your language/tool) and point your OTLP exporter ' +
        'at them - works for anything speaking standard OTLP/HTTP, no Obzly-specific SDK needed.',
    };
  },
};
