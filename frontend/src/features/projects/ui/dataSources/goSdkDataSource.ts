// Source: https://opentelemetry.io/docs/languages/go/getting-started/ , .../go/exporters/ (autoexport)
// and https://opentelemetry.io/docs/zero-code/go/ (compile-time, OBI)
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
import type { DataSourceConfigFile, DataSourceDefinition } from './types';

const SERVICE = 'my-go-service';

// autoexport picks the exporter from OTEL_{TRACES,METRICS,LOGS}_EXPORTER and
// defaults to OTLP over http/protobuf; the SDK's default resource reads
// OTEL_SERVICE_NAME and OTEL_RESOURCE_ATTRIBUTES.
function otelGo(signals: Signal[]): string {
  const imports = ['"context"', '"errors"', ''];
  if (signals.length > 0) imports.push('"go.opentelemetry.io/contrib/exporters/autoexport"');
  imports.push('"go.opentelemetry.io/otel"');
  if (signals.includes('logs')) imports.push('"go.opentelemetry.io/otel/log/global"');
  imports.push('"go.opentelemetry.io/otel/propagation"');
  if (signals.includes('logs')) imports.push('sdklog "go.opentelemetry.io/otel/sdk/log"');
  if (signals.includes('metrics')) imports.push('sdkmetric "go.opentelemetry.io/otel/sdk/metric"');
  if (signals.includes('traces')) imports.push('sdktrace "go.opentelemetry.io/otel/sdk/trace"');

  const blocks: string[] = [];
  if (signals.includes('traces'))
    blocks.push(`	spanExporter, err := autoexport.NewSpanExporter(ctx)
	if err != nil {
		return nil, errors.Join(err, shutdown(ctx))
	}
	tracerProvider := sdktrace.NewTracerProvider(sdktrace.WithBatcher(spanExporter))
	shutdownFuncs = append(shutdownFuncs, tracerProvider.Shutdown)
	otel.SetTracerProvider(tracerProvider)`);
  if (signals.includes('metrics'))
    blocks.push(`	metricReader, err := autoexport.NewMetricReader(ctx)
	if err != nil {
		return nil, errors.Join(err, shutdown(ctx))
	}
	meterProvider := sdkmetric.NewMeterProvider(sdkmetric.WithReader(metricReader))
	shutdownFuncs = append(shutdownFuncs, meterProvider.Shutdown)
	otel.SetMeterProvider(meterProvider)`);
  if (signals.includes('logs'))
    blocks.push(`	logExporter, err := autoexport.NewLogExporter(ctx)
	if err != nil {
		return nil, errors.Join(err, shutdown(ctx))
	}
	loggerProvider := sdklog.NewLoggerProvider(sdklog.WithProcessor(sdklog.NewBatchProcessor(logExporter)))
	shutdownFuncs = append(shutdownFuncs, loggerProvider.Shutdown)
	global.SetLoggerProvider(loggerProvider)`);

  const body = blocks.length > 0 ? `\n${blocks.join('\n\n')}\n` : '\n';
  return `// otel.go - call setupOTelSDK at the start of main and defer the shutdown it
// returns. Endpoint, protocol and the API key come from the OTEL_* env vars.
package main

import (
${imports.map((line) => (line ? `\t${line}` : '')).join('\n')}
)

func setupOTelSDK(ctx context.Context) (func(context.Context) error, error) {
	var shutdownFuncs []func(context.Context) error
	shutdown := func(ctx context.Context) error {
		var err error
		for _, fn := range shutdownFuncs {
			err = errors.Join(err, fn(ctx))
		}
		shutdownFuncs = nil
		return err
	}

	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{},
		propagation.Baggage{},
	))
${body}
	return shutdown, nil
}
`;
}

function goGetCommand(signals: Signal[]): string {
  const modules = ['go.opentelemetry.io/otel'];
  if (signals.length > 0) modules.push('go.opentelemetry.io/contrib/exporters/autoexport');
  if (signals.includes('traces')) modules.push('go.opentelemetry.io/otel/sdk');
  if (signals.includes('metrics')) modules.push('go.opentelemetry.io/otel/sdk/metric');
  if (signals.includes('logs'))
    modules.push('go.opentelemetry.io/otel/sdk/log', 'go.opentelemetry.io/otel/log');
  return `go get ${modules.join(' \\\n  ')}
# Then wrap handlers and clients with the instrumentation libraries you use,
# e.g. go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp.
`;
}

const OTELC_INSTALL = `# Compile-time instrumentation (stable): no code changes, needs Go 1.25+.
go install go.opentelemetry.io/otelc/tool/cmd/otelc@latest

# Build with otelc instead of plain go build:
otelc go build -o myapp .
./myapp
`;

export const goSdkDataSource: DataSourceDefinition = {
  id: 'lang-go',
  name: 'Go',
  category: 'Languages',
  description:
    'OpenTelemetry Go SDK setup code, or compile-time zero-code instrumentation with otelc.',
  fields: [
    ...commonFields(SERVICE, { traces: 'stable', metrics: 'stable', logs: 'rc' }),
    {
      key: 'method',
      label: 'Instrumentation',
      defaultValue: 'sdk',
      options: [
        { value: 'sdk', label: 'SDK in code (otel.go)' },
        { value: 'otelc', label: 'Zero-code at build time (otelc)' },
      ],
      helpText:
        'For zero-code without rebuilding, use the eBPF Network Map (OBI) entry with "Trace ' +
        'applications" on - it traces Go binaries from the outside. Go\'s separate eBPF ' +
        'auto-instrumentation project is still a work in progress upstream.',
    },
    RUN_MODE_FIELD,
  ],
  build: ({ baseUrl, apiKey, fieldValues }) => {
    const common = readCommon(fieldValues, SERVICE);
    const mode = readRunMode(fieldValues);
    const otelc = fieldValues.method === 'otelc';
    const env = otelEnv({ baseUrl, apiKey, common });

    const setup: DataSourceConfigFile[] = otelc
      ? [{ filename: 'install.sh', language: 'bash', content: OTELC_INSTALL }]
      : [
          { filename: 'install.sh', language: 'bash', content: goGetCommand(common.signals) },
          { filename: 'otel.go', language: 'go', content: otelGo(common.signals) },
        ];

    const rendered = renderEnvFiles({
      mode,
      env,
      shellIntro: 'Load before starting the binary (source obzly-otel.env).',
    });

    const otelcHint = otelc
      ? ' In a Dockerfile build stage, install otelc and replace go build with otelc go build.'
      : '';
    return multiFileResult(
      [...setup, ...rendered.files],
      `${rendered.howTo}${otelcHint} ${TEMPORALITY_NOTE}`,
    );
  },
};
