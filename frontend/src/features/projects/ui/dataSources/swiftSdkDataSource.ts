// Source: https://opentelemetry.io/docs/languages/swift/getting-started/ and the opentelemetry-swift
// README and Package.swift linked from it
import {
  GATEWAY_RUN_INSTRUCTIONS,
  GATEWAY_URL_FIELD,
  gatewayFiles,
  readGatewayUrl,
} from './clientGateway';
import { commonFields, multiFileResult, readCommon } from './languageSdkCommon';
import type { DataSourceDefinition } from './types';

const SERVICE = 'my-ios-app';

// Minimum versions as in the docs; SwiftPM resolves the latest compatible.
const PACKAGE = `// Package.swift - add to dependencies:
.package(url: "https://github.com/open-telemetry/opentelemetry-swift-core.git", from: "2.4.1"),
.package(url: "https://github.com/open-telemetry/opentelemetry-swift.git", from: "2.4.1"),

// ...and to your target's dependencies:
.product(name: "OpenTelemetryApi", package: "opentelemetry-swift-core"),
.product(name: "OpenTelemetrySdk", package: "opentelemetry-swift-core"),
.product(name: "OpenTelemetryProtocolExporterHTTP", package: "opentelemetry-swift"),
.product(name: "ResourceExtension", package: "opentelemetry-swift"),
// In an Xcode project, add the two packages via File > Add Package Dependencies.
`;

function telemetrySwift(gatewayUrl: string, serviceName: string, environment: string): string {
  return `// Telemetry.swift - call Telemetry.start() once at launch.
// Sends to YOUR gateway, which adds the Obzly API key server-side.
// Never put an API key in an app: anyone can extract it from the binary.
import Foundation
import OpenTelemetryApi
import OpenTelemetryProtocolExporterHttp
import OpenTelemetrySdk
import ResourceExtension

enum Telemetry {
    static func start() {
        let exporter = OtlpHttpTraceExporter(
            endpoint: URL(string: ${JSON.stringify(`${gatewayUrl}/v1/traces`)})!
        )
        let resource = DefaultResources().get().merging(other: Resource(attributes: [
            "service.name": AttributeValue.string(${JSON.stringify(serviceName)}),
            "deployment.environment.name": AttributeValue.string(${JSON.stringify(environment)}),
        ]))
        OpenTelemetry.registerTracerProvider(tracerProvider:
            TracerProviderBuilder()
                .add(spanProcessor: BatchSpanProcessor(spanExporter: exporter))
                .with(resource: resource)
                .build()
        )
    }
}
`;
}

export const swiftSdkDataSource: DataSourceDefinition = {
  id: 'lang-swift',
  name: 'Swift (iOS / macOS)',
  category: 'Languages',
  description:
    'App traces via opentelemetry-swift, sent through your own collector gateway - no key in the app.',
  fields: [...commonFields(SERVICE, { traces: 'stable' }), GATEWAY_URL_FIELD],
  build: ({ baseUrl, apiKey, fieldValues }) => {
    const common = readCommon(fieldValues, SERVICE, ['traces']);
    const gatewayUrl = readGatewayUrl(fieldValues);
    const client = common.signals.includes('traces')
      ? [
          { filename: 'Package.swift', language: 'swift', content: PACKAGE },
          {
            filename: 'Telemetry.swift',
            language: 'swift',
            content: telemetrySwift(gatewayUrl, common.serviceName, common.environment),
          },
        ]
      : [];
    return multiFileResult(
      [...gatewayFiles(baseUrl, apiKey), ...client],
      `The app never gets the API key: Telemetry.swift posts to the gateway, and only the ` +
        `gateway (a collector you run) talks to Obzly with the key. ${GATEWAY_RUN_INSTRUCTIONS} ` +
        `Swift metrics and logs are still in development upstream, so only traces are set up here.`,
    );
  },
};
