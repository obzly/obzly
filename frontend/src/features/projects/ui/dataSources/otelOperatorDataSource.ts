// Source: https://opentelemetry.io/docs/platforms/kubernetes/operator/ and
// https://opentelemetry.io/docs/platforms/kubernetes/operator/automatic/
import {
  HEADERS_ENV,
  TEMPORALITY_NOTE,
  commonFields,
  k8sName,
  multiFileResult,
  readCommon,
} from './languageSdkCommon';
import type { DataSourceDefinition } from './types';

const SERVICE = '';

const LANGUAGES = [
  { key: 'java', label: 'Java', annotation: 'inject-java' },
  { key: 'nodejs', label: 'Node.js', annotation: 'inject-nodejs' },
  { key: 'python', label: 'Python', annotation: 'inject-python' },
  { key: 'dotnet', label: '.NET', annotation: 'inject-dotnet' },
  { key: 'go', label: 'Go (eBPF sidecar)', annotation: 'inject-go' },
] as const;

const INSTALL = `# The operator needs cert-manager (https://cert-manager.io/docs/installation/).
kubectl apply -f https://github.com/open-telemetry/opentelemetry-operator/releases/latest/download/opentelemetry-operator.yaml
kubectl -n opentelemetry-operator-system rollout status deployment/opentelemetry-operator-controller-manager
`;

function yamlString(value: string): string {
  return JSON.stringify(value);
}

export const otelOperatorDataSource: DataSourceDefinition = {
  id: 'otel-operator',
  name: 'OpenTelemetry Operator (Kubernetes)',
  category: 'Languages',
  description:
    'Zero-code instrumentation injected into pods by annotation: Java, Node.js, Python, .NET, Go.',
  fields: [
    {
      key: 'namespace',
      label: 'App namespace',
      defaultValue: 'default',
      helpText:
        'Where your instrumented workloads run. The Instrumentation and the key Secret go here: ' +
        'pods can only read Secrets from their own namespace.',
    },
    ...commonFields(SERVICE, { traces: 'stable', metrics: 'stable', logs: 'stable' }).filter(
      (field) => field.key !== 'serviceName',
    ),
    ...LANGUAGES.map((language) => ({
      key: `lang_${language.key}`,
      label: `Annotate for ${language.label}`,
      type: 'checkbox' as const,
      defaultValue: language.key === 'go' ? 'no' : 'yes',
      helpText:
        language.key === 'go'
          ? 'Go is off in the operator by default (--enable-go-instrumentation=true on the ' +
            'manager) and runs a privileged eBPF sidecar; it also needs the binary path.'
          : undefined,
    })),
  ],
  build: ({ baseUrl, apiKey, fieldValues }) => {
    const namespace = k8sName(fieldValues.namespace?.trim() || 'default');
    const common = readCommon(fieldValues, SERVICE);
    const secretName = 'obzly-otel';
    const exporters = (['traces', 'metrics', 'logs'] as const).map(
      (signal) =>
        `    - name: OTEL_${signal.toUpperCase()}_EXPORTER\n      value: ${
          common.signals.includes(signal) ? 'otlp' : 'none'
        }`,
    );

    // Service names come from each pod's workload name, so the Instrumentation
    // only carries what every service shares.
    const manifest = `apiVersion: v1
kind: Secret
metadata:
  name: ${secretName}
  namespace: ${namespace}
type: Opaque
stringData:
  ${HEADERS_ENV}: ${yamlString(`X-API-Key=${apiKey}`)}
---
apiVersion: opentelemetry.io/v1alpha1
kind: Instrumentation
metadata:
  name: obzly
  namespace: ${namespace}
spec:
  exporter:
    endpoint: ${yamlString(baseUrl)}
  propagators:
    - tracecontext
    - baggage
  sampler:
    type: parentbased_traceidratio
    argument: "1"
  resource:
    resourceAttributes:
      deployment.environment.name: ${yamlString(common.environment)}
  env:
    # Obzly takes OTLP over HTTP only; the operator's Node.js default is gRPC.
    - name: OTEL_EXPORTER_OTLP_PROTOCOL
      value: http/protobuf
    - name: ${HEADERS_ENV}
      valueFrom:
        secretKeyRef:
          name: ${secretName}
          key: ${HEADERS_ENV}
${exporters.join('\n')}
`;

    const chosen = LANGUAGES.filter((language) => fieldValues[`lang_${language.key}`] === 'yes');
    const annotationLines = chosen.flatMap((language) => {
      const lines = [`        instrumentation.opentelemetry.io/${language.annotation}: "true"`];
      if (language.key === 'go')
        lines.push(
          '        # Path of the Go binary inside the container:',
          '        instrumentation.opentelemetry.io/otel-go-auto-target-exe: "/app/server"',
        );
      return lines;
    });
    const annotations =
      annotationLines.length > 0
        ? `# Add the line for each Deployment's language to its
# spec.template.metadata.annotations - one language per pod, unless the operator
# runs with --enable-multi-instrumentation and you use <lang>-container-names.
spec:
  template:
    metadata:
      annotations:
${annotationLines.join('\n')}
`
        : '# No language selected: tick at least one to get the pod annotations.\n';

    return multiFileResult(
      [
        { filename: 'install.sh', language: 'bash', content: INSTALL },
        { filename: 'obzly-instrumentation.yaml', language: 'yaml', content: manifest },
        { filename: 'deployment-annotations.yaml', language: 'yaml', content: annotations },
      ],
      `Install the operator, then kubectl apply -f obzly-instrumentation.yaml, then add the ` +
        `annotations and roll the Deployments - pods pick up the agent on restart. The API key ` +
        `stays in the Secret. Apache HTTPD and NGINX injection isn't offered: their module ` +
        `exports OTLP/gRPC, so use the Apache/NGINX entries instead. ${TEMPORALITY_NOTE}`,
    );
  },
};
