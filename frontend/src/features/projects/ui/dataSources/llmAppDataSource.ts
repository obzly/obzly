import type { DataSourceDefinition } from './types';

// Apps instrumented with the OpenTelemetry GenAI conventions export spans
// themselves: point their OTLP exporter at Obzly. Message content capture
// stays off (the LLM apps view never shows it anyway).
export const llmAppDataSource: DataSourceDefinition = {
  id: 'llm-app-otel',
  name: 'LLM app (OpenTelemetry)',
  category: 'AI Tools',
  description:
    'Any app instrumented with the OpenTelemetry gen_ai conventions: OpenAI, Anthropic, Bedrock, Vertex AI, LangChain and others.',
  fields: [
    {
      key: 'serviceName',
      label: 'Service name',
      defaultValue: 'support-bot',
      placeholder: 'support-bot',
      helpText: 'How the app shows up in the LLM apps breakdown and in traces.',
    },
  ],
  build: ({ baseUrl, apiKey, fieldValues }) => {
    const serviceName = fieldValues.serviceName?.trim() || 'llm-app';
    const env = `export OTEL_SERVICE_NAME="${serviceName}"
export OTEL_EXPORTER_OTLP_ENDPOINT="${baseUrl}"
export OTEL_EXPORTER_OTLP_HEADERS="X-API-Key=${apiKey}"
export OTEL_EXPORTER_OTLP_PROTOCOL="http/protobuf"
# Keep prompts and completions out of the spans.
export OTEL_INSTRUMENTATION_GENAI_CAPTURE_MESSAGE_CONTENT="false"
`;

    const python = `# pip install opentelemetry-sdk opentelemetry-exporter-otlp-proto-http \\
#   opentelemetry-instrumentation-openai-v2
# Reads OTEL_SERVICE_NAME and OTEL_EXPORTER_OTLP_* from obzly-llm-app.env.
from opentelemetry import trace
from opentelemetry.exporter.otlp.proto.http.trace_exporter import OTLPSpanExporter
from opentelemetry.instrumentation.openai_v2 import OpenAIInstrumentor
from opentelemetry.sdk.trace import TracerProvider
from opentelemetry.sdk.trace.export import BatchSpanProcessor

provider = TracerProvider()
provider.add_span_processor(BatchSpanProcessor(OTLPSpanExporter()))
trace.set_tracer_provider(provider)

# Swap in your library's instrumentation (Anthropic, Bedrock, LangChain...)
# if it isn't the OpenAI SDK.
OpenAIInstrumentor().instrument()
`;

    const node = `// npm install @opentelemetry/sdk-node @opentelemetry/exporter-trace-otlp-proto \\
//   @traceloop/instrumentation-openai
// Load first (node --import ./otel.mjs app.mjs); reads OTEL_* from obzly-llm-app.env.
import { NodeSDK } from '@opentelemetry/sdk-node';
import { OTLPTraceExporter } from '@opentelemetry/exporter-trace-otlp-proto';
import { OpenAIInstrumentation } from '@traceloop/instrumentation-openai';

const sdk = new NodeSDK({
  traceExporter: new OTLPTraceExporter(),
  // traceContent: false keeps prompts and completions out of the spans.
  instrumentations: [new OpenAIInstrumentation({ traceContent: false })],
});
sdk.start();
`;

    return {
      filename: 'obzly-llm-app.env',
      language: 'bash',
      content: '',
      files: [
        { filename: 'obzly-llm-app.env', language: 'bash', content: env },
        { filename: 'otel_setup.py', language: 'python', content: python },
        { filename: 'otel.mjs', language: 'javascript', content: node },
      ],
      instructions:
        "Set the env vars in the app's environment, then add the Python or Node.js setup before the " +
        'app makes its first LLM call. Calls show up under AI Observability, LLM apps; set model ' +
        'prices there (Settings) to see estimated cost.',
    };
  },
};
