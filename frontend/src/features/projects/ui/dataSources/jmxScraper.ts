// The collector's jmx receiver was removed from otelcol-contrib; JMX metrics come from the
// standalone OpenTelemetry JMX Scraper, which exports OTLP to the local collector.
export const JMX_SCRAPER_VERSION = '1.61.0';

const JAR_URL = `https://github.com/open-telemetry/opentelemetry-java-contrib/releases/download/v${JMX_SCRAPER_VERSION}/opentelemetry-jmx-scraper.jar`;

export function jmxScraperProperties(options: {
  jmxUrl: string;
  targetSystem: string;
  /** auto (default), instrumentation or legacy metric definitions. */
  targetSource?: string;
  serviceName: string;
}): string {
  const targetSource = options.targetSource
    ? `otel.jmx.target.source=${options.targetSource}\n`
    : '';
  return `# OpenTelemetry JMX Scraper config: reads ${options.targetSystem} MBeans over JMX and
# sends them as OTLP metrics to the collector running next to it.
otel.jmx.service.url=${options.jmxUrl}
otel.jmx.target.system=${options.targetSystem}
${targetSource}otel.service.name=${options.serviceName}
otel.metric.export.interval=30s
otel.exporter.otlp.endpoint=http://127.0.0.1:4318
otel.exporter.otlp.protocol=http/protobuf
# If JMX authentication is enabled on the target JVM:
# otel.jmx.username=monitor
# otel.jmx.password=...
`;
}

export function jmxScraperInstructions(apiKey: string): string {
  return (
    `Start the collector: OBZLY_API_KEY="${apiKey}" otelcol-contrib --config=otel-collector-config.yaml. ` +
    `Then download the JMX Scraper (Java 8+): curl -fsSL -o opentelemetry-jmx-scraper.jar ${JAR_URL} ` +
    `- check the connection with: java -jar opentelemetry-jmx-scraper.jar -test -config jmx-scraper.properties ` +
    `- and run it with: java -jar opentelemetry-jmx-scraper.jar -config jmx-scraper.properties`
  );
}
