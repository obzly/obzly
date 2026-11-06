// Source: https://opentelemetry.io/docs/languages/cpp/exporters/
import {
  RUN_MODE_FIELD,
  type Signal,
  TEMPORALITY_NOTE,
  commonFields,
  multiFileResult,
  otelEnv,
  readCommon,
  readRunMode,
  renderEnvFiles,
} from './languageSdkCommon';
import type { DataSourceDefinition } from './types';

const SERVICE = 'my-cpp-service';

const INSTALL = `# Install opentelemetry-cpp with the OTLP HTTP exporter, either with vcpkg:
vcpkg install "opentelemetry-cpp[otlp-http]"
# or from source (latest release) with -DWITH_OTLP_HTTP=ON, see
# https://github.com/open-telemetry/opentelemetry-cpp/blob/main/INSTALL.md

# CMakeLists.txt:
#   find_package(opentelemetry-cpp CONFIG REQUIRED)
#   target_link_libraries(myapp PRIVATE \${OPENTELEMETRY_CPP_LIBRARIES})
`;

// Default-constructed Otlp*Options read OTEL_EXPORTER_OTLP_ENDPOINT (signal
// path appended) and OTEL_EXPORTER_OTLP_HEADERS; the default resource reads
// OTEL_SERVICE_NAME and OTEL_RESOURCE_ATTRIBUTES.
function telemetryCc(signals: Signal[]): string {
  const includes: string[] = [];
  const namespaces: string[] = ['namespace otlp = opentelemetry::exporter::otlp;'];
  const inits: string[] = [];
  const cleanups: string[] = [];
  if (signals.includes('traces')) {
    includes.push(
      '#include "opentelemetry/exporters/otlp/otlp_http_exporter_factory.h"',
      '#include "opentelemetry/exporters/otlp/otlp_http_exporter_options.h"',
      '#include "opentelemetry/sdk/trace/batch_span_processor_factory.h"',
      '#include "opentelemetry/sdk/trace/batch_span_processor_options.h"',
      '#include "opentelemetry/sdk/trace/tracer_provider.h"',
      '#include "opentelemetry/sdk/trace/tracer_provider_factory.h"',
      '#include "opentelemetry/trace/provider.h"',
    );
    namespaces.push(
      'namespace trace_api = opentelemetry::trace;',
      'namespace trace_sdk = opentelemetry::sdk::trace;',
    );
    inits.push(`  {
    otlp::OtlpHttpExporterOptions opts;
    auto exporter = otlp::OtlpHttpExporterFactory::Create(opts);
    trace_sdk::BatchSpanProcessorOptions bsp_opts{};
    auto processor = trace_sdk::BatchSpanProcessorFactory::Create(std::move(exporter), bsp_opts);
    std::shared_ptr<trace_api::TracerProvider> provider =
        trace_sdk::TracerProviderFactory::Create(std::move(processor));
    trace_api::Provider::SetTracerProvider(provider);
  }`);
    cleanups.push(`  std::shared_ptr<trace_api::TracerProvider> none_tracer;
  trace_api::Provider::SetTracerProvider(none_tracer);`);
  }
  if (signals.includes('metrics')) {
    includes.push(
      '#include "opentelemetry/exporters/otlp/otlp_http_metric_exporter_factory.h"',
      '#include "opentelemetry/exporters/otlp/otlp_http_metric_exporter_options.h"',
      '#include "opentelemetry/metrics/provider.h"',
      '#include "opentelemetry/sdk/metrics/export/periodic_exporting_metric_reader_factory.h"',
      '#include "opentelemetry/sdk/metrics/meter_context_factory.h"',
      '#include "opentelemetry/sdk/metrics/meter_provider.h"',
      '#include "opentelemetry/sdk/metrics/meter_provider_factory.h"',
    );
    namespaces.push(
      'namespace metrics_api = opentelemetry::metrics;',
      'namespace metric_sdk = opentelemetry::sdk::metrics;',
    );
    inits.push(`  {
    otlp::OtlpHttpMetricExporterOptions opts;
    auto exporter = otlp::OtlpHttpMetricExporterFactory::Create(opts);
    metric_sdk::PeriodicExportingMetricReaderOptions reader_opts;
    auto reader =
        metric_sdk::PeriodicExportingMetricReaderFactory::Create(std::move(exporter), reader_opts);
    auto context = metric_sdk::MeterContextFactory::Create();
    context->AddMetricReader(std::move(reader));
    std::shared_ptr<metrics_api::MeterProvider> provider(
        metric_sdk::MeterProviderFactory::Create(std::move(context)));
    metrics_api::Provider::SetMeterProvider(provider);
  }`);
    cleanups.push(`  std::shared_ptr<metrics_api::MeterProvider> none_meter;
  metrics_api::Provider::SetMeterProvider(none_meter);`);
  }
  if (signals.includes('logs')) {
    includes.push(
      '#include "opentelemetry/exporters/otlp/otlp_http_log_record_exporter_factory.h"',
      '#include "opentelemetry/exporters/otlp/otlp_http_log_record_exporter_options.h"',
      '#include "opentelemetry/logs/provider.h"',
      '#include "opentelemetry/sdk/logs/batch_log_record_processor_factory.h"',
      '#include "opentelemetry/sdk/logs/batch_log_record_processor_options.h"',
      '#include "opentelemetry/sdk/logs/logger_provider.h"',
      '#include "opentelemetry/sdk/logs/logger_provider_factory.h"',
    );
    namespaces.push(
      'namespace logs_api = opentelemetry::logs;',
      'namespace logs_sdk = opentelemetry::sdk::logs;',
    );
    inits.push(`  {
    otlp::OtlpHttpLogRecordExporterOptions opts;
    auto exporter = otlp::OtlpHttpLogRecordExporterFactory::Create(opts);
    logs_sdk::BatchLogRecordProcessorOptions blrp_opts{};
    auto processor =
        logs_sdk::BatchLogRecordProcessorFactory::Create(std::move(exporter), blrp_opts);
    std::shared_ptr<logs_api::LoggerProvider> provider =
        logs_sdk::LoggerProviderFactory::Create(std::move(processor));
    logs_api::Provider::SetLoggerProvider(provider);
  }`);
    cleanups.push(`  std::shared_ptr<logs_api::LoggerProvider> none_logger;
  logs_api::Provider::SetLoggerProvider(none_logger);`);
  }

  return `// telemetry.cc - call InitTelemetry() at start-up and CleanupTelemetry()
// before exit. Endpoint and API key come from the OTEL_* env vars.
#include <memory>

${includes.join('\n')}

${namespaces.join('\n')}

void InitTelemetry()
{
${inits.join('\n')}
}

// Resetting the global providers flushes and shuts them down.
void CleanupTelemetry()
{
${cleanups.join('\n')}
}
`;
}

export const cppSdkDataSource: DataSourceDefinition = {
  id: 'lang-cpp',
  name: 'C++',
  category: 'Languages',
  description: 'opentelemetry-cpp SDK with the OTLP HTTP exporter: traces, metrics and logs.',
  fields: [
    ...commonFields(SERVICE, { traces: 'stable', metrics: 'stable', logs: 'stable' }),
    RUN_MODE_FIELD,
  ],
  build: ({ baseUrl, apiKey, fieldValues }) => {
    const common = readCommon(fieldValues, SERVICE);
    const mode = readRunMode(fieldValues);
    const env = otelEnv({ baseUrl, apiKey, common });
    const rendered = renderEnvFiles({
      mode,
      env,
      shellIntro: 'Load before starting the binary (source obzly-otel.env).',
    });
    return multiFileResult(
      [
        { filename: 'install.sh', language: 'bash', content: INSTALL },
        { filename: 'telemetry.cc', language: 'cpp', content: telemetryCc(common.signals) },
        ...rendered.files,
      ],
      `${rendered.howTo} The C++ SDK reads the endpoint and headers from the environment, but ` +
        `which signals to send is decided by the code above. ${TEMPORALITY_NOTE}`,
    );
  },
};
