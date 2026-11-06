// Source: https://opentelemetry.io/docs/languages/rust/exporters/ and the opentelemetry-otlp
// crate docs (0.33.x) linked from it
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

const SERVICE = 'my-rust-service';

function cargoCommands(signals: Signal[]): string {
  const lines = [
    '# reqwest-rustls adds TLS to the default blocking HTTP client (needed for https://).',
    'cargo add opentelemetry opentelemetry_sdk',
    'cargo add opentelemetry-otlp --features reqwest-rustls',
  ];
  if (signals.includes('logs'))
    lines.push(
      'cargo add opentelemetry-appender-tracing tracing',
      'cargo add tracing-subscriber --features env-filter',
    );
  return `${lines.join('\n')}\n`;
}

// Exporters built without an endpoint read OTEL_EXPORTER_OTLP_ENDPOINT (signal
// path appended) and OTEL_EXPORTER_OTLP_HEADERS; Resource::builder() reads
// OTEL_SERVICE_NAME and OTEL_RESOURCE_ATTRIBUTES.
function telemetryRs(signals: Signal[]): string {
  const sdkImports: string[] = ['Resource'];
  if (signals.includes('logs')) sdkImports.push('logs::SdkLoggerProvider');
  if (signals.includes('metrics')) sdkImports.push('metrics::SdkMeterProvider');
  if (signals.includes('traces')) sdkImports.push('trace::SdkTracerProvider');
  const otlpImports: string[] = [];
  if (signals.includes('logs')) otlpImports.push('LogExporter');
  if (signals.includes('metrics')) otlpImports.push('MetricExporter');
  otlpImports.push('Protocol');
  if (signals.includes('traces')) otlpImports.push('SpanExporter');
  otlpImports.push('WithExportConfig');

  const fields: string[] = [];
  const inits: string[] = [];
  const shutdowns: string[] = [];
  if (signals.includes('traces')) {
    fields.push('    tracer_provider: SdkTracerProvider,');
    inits.push(`    let span_exporter = SpanExporter::builder()
        .with_http()
        .with_protocol(Protocol::HttpBinary)
        .build()?;
    let tracer_provider = SdkTracerProvider::builder()
        .with_resource(resource.clone())
        .with_batch_exporter(span_exporter)
        .build();
    global::set_tracer_provider(tracer_provider.clone());`);
    shutdowns.push('        let _ = self.tracer_provider.shutdown();');
  }
  if (signals.includes('metrics')) {
    fields.push('    meter_provider: SdkMeterProvider,');
    inits.push(`    let metric_exporter = MetricExporter::builder()
        .with_http()
        .with_protocol(Protocol::HttpBinary)
        .build()?;
    let meter_provider = SdkMeterProvider::builder()
        .with_resource(resource.clone())
        .with_periodic_exporter(metric_exporter)
        .build();
    global::set_meter_provider(meter_provider.clone());`);
    shutdowns.push('        let _ = self.meter_provider.shutdown();');
  }
  if (signals.includes('logs')) {
    fields.push('    logger_provider: SdkLoggerProvider,');
    inits.push(`    let log_exporter = LogExporter::builder()
        .with_http()
        .with_protocol(Protocol::HttpBinary)
        .build()?;
    let logger_provider = SdkLoggerProvider::builder()
        .with_resource(resource.clone())
        .with_batch_exporter(log_exporter)
        .build();
    // Keep the exporter's own HTTP client logs out of the pipeline.
    let filter = EnvFilter::new("info")
        .add_directive("hyper=off".parse()?)
        .add_directive("reqwest=off".parse()?);
    tracing_subscriber::registry()
        .with(OpenTelemetryTracingBridge::new(&logger_provider).with_filter(filter))
        .init();`);
    shutdowns.push('        let _ = self.logger_provider.shutdown();');
  }

  const fieldNames = fields.map((field) => field.trim().split(':')[0]);
  const logsImports = signals.includes('logs')
    ? `use opentelemetry_appender_tracing::layer::OpenTelemetryTracingBridge;
use tracing_subscriber::{prelude::*, EnvFilter};
`
    : '';
  const globalImport = signals.some((s) => s !== 'logs') ? 'use opentelemetry::global;\n' : '';
  const resourceLine =
    signals.length > 0 ? '    let resource = Resource::builder().build();\n' : '';

  return `// telemetry.rs - call telemetry::init()? at the top of main and keep the
// returned guard alive; dropping it flushes and shuts the providers down.
${globalImport}use opentelemetry_otlp::{${otlpImports.join(', ')}};
use opentelemetry_sdk::{${sdkImports.join(', ')}};
${logsImports}
pub struct Telemetry {
${fields.join('\n')}
}

pub fn init() -> Result<Telemetry, Box<dyn std::error::Error + Send + Sync>> {
${resourceLine}${inits.join('\n\n')}
    Ok(Telemetry { ${fieldNames.join(', ')} })
}

impl Drop for Telemetry {
    fn drop(&mut self) {
${shutdowns.join('\n')}
    }
}
`;
}

export const rustSdkDataSource: DataSourceDefinition = {
  id: 'lang-rust',
  name: 'Rust',
  category: 'Languages',
  description: 'opentelemetry + opentelemetry_sdk + opentelemetry-otlp setup code, OTLP over HTTP.',
  fields: [
    ...commonFields(SERVICE, { traces: 'beta', metrics: 'stable', logs: 'stable' }),
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
        { filename: 'install.sh', language: 'bash', content: cargoCommands(common.signals) },
        { filename: 'telemetry.rs', language: 'rust', content: telemetryRs(common.signals) },
        ...rendered.files,
      ],
      `${rendered.howTo} The Rust trace exporter is beta upstream (metrics and logs are stable), ` +
        `which is why traces start unchecked. Logs are bridged from the tracing crate. ` +
        TEMPORALITY_NOTE,
    );
  },
};
