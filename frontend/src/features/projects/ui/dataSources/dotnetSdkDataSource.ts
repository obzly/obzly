// Source: https://opentelemetry.io/docs/zero-code/dotnet/ , .../zero-code/dotnet/nuget-packages/
// and https://opentelemetry.io/docs/languages/dotnet/exporters/
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

const SERVICE = 'my-dotnet-service';
// Pinned by the docs' installer snippet.
const AUTO_VERSION = 'v1.17.0';

type Method = 'zero-code-unix' | 'zero-code-windows' | 'zero-code-nuget' | 'sdk';

function readMethod(value: string | undefined): Method {
  return value === 'zero-code-windows' || value === 'zero-code-nuget' || value === 'sdk'
    ? value
    : 'zero-code-unix';
}

const UNIX_INSTALL = `# Download, verify (needs the GitHub CLI) and run the installer.
version="${AUTO_VERSION}"
repository="open-telemetry/opentelemetry-dotnet-instrumentation"
release_workflow="$repository/.github/workflows/release.yml"
download_dir="$(mktemp -d "\${TMPDIR:-/tmp}/otel-dotnet-auto-installer.XXXXXX")"
installer="$download_dir/otel-dotnet-auto-install.sh"
trap 'rm -rf "$download_dir"' 0

curl -sSfL "https://github.com/$repository/releases/download/$version/otel-dotnet-auto-install.sh" -o "$installer" &&
  gh release verify-asset "$version" "$installer" --repo "$repository" &&
  gh attestation verify "$installer" \\
    --repo "$repository" \\
    --signer-workflow "$release_workflow" \\
    --source-ref "refs/tags/$version" &&
  VERSION="$version" sh "$installer"

chmod +x $HOME/.otel-dotnet-auto/instrument.sh

# In the shell that starts the app (after loading obzly-otel.env):
. $HOME/.otel-dotnet-auto/instrument.sh
./MyNetApp
`;

const WINDOWS_INSTALL = `# Windows PowerShell 5.1 as Administrator. Install the module and core files
# with the verified download block from
# https://opentelemetry.io/docs/zero-code/dotnet/#windows-powershell
# (version ${AUTO_VERSION}), then, in the session that starts the app:
Import-Module "OpenTelemetry.DotNet.Auto.psm1"
Register-OpenTelemetryForCurrentSession -OTelServiceName "<service name>"
.\\MyNetApp.exe

# Windows Service: Register-OpenTelemetryForWindowsService (restarts the service).
# IIS: Register-OpenTelemetryForIIS (restarts IIS).
`;

const NUGET_INSTALL = `# Add the automatic instrumentation package to the app project:
dotnet add package OpenTelemetry.AutoInstrumentation

# Build or publish with a runtime identifier, then start the app through the
# script the package puts in the output folder:
dotnet publish -r linux-x64 -o out
./out/instrument.sh dotnet out/MyNetApp.dll
`;

function sdkFiles(signals: Signal[]): DataSourceConfigFile[] {
  const packages = [
    'dotnet add package OpenTelemetry.Extensions.Hosting',
    'dotnet add package OpenTelemetry.Exporter.OpenTelemetryProtocol',
    'dotnet add package OpenTelemetry.Instrumentation.AspNetCore',
    'dotnet add package OpenTelemetry.Instrumentation.Http',
  ];
  const tracing = signals.includes('traces')
    ? `
    .WithTracing(tracing => tracing
        .AddAspNetCoreInstrumentation()
        .AddHttpClientInstrumentation()
        .AddOtlpExporter())`
    : '';
  const metrics = signals.includes('metrics')
    ? `
    .WithMetrics(metrics => metrics
        .AddAspNetCoreInstrumentation()
        .AddHttpClientInstrumentation()
        .AddOtlpExporter())`
    : '';
  const logs = signals.includes('logs')
    ? `

builder.Logging.AddOpenTelemetry(logging => logging.AddOtlpExporter());`
    : '';
  const program = `// Program.cs (ASP.NET Core). AddOtlpExporter() with no options reads
// OTEL_EXPORTER_OTLP_ENDPOINT/_PROTOCOL/_HEADERS, and the resource reads
// OTEL_SERVICE_NAME/OTEL_RESOURCE_ATTRIBUTES, so nothing secret is in code.
using OpenTelemetry.Logs;
using OpenTelemetry.Metrics;
using OpenTelemetry.Trace;

var builder = WebApplication.CreateBuilder(args);

builder.Services.AddOpenTelemetry()${tracing}${metrics};${logs}

var app = builder.Build();
`;
  return [
    { filename: 'install.sh', language: 'bash', content: `${packages.join('\n')}\n` },
    { filename: 'Program.cs', language: 'csharp', content: program },
  ];
}

export const dotnetSdkDataSource: DataSourceDefinition = {
  id: 'lang-dotnet',
  name: '.NET (C#)',
  category: 'Languages',
  description:
    'OpenTelemetry .NET automatic instrumentation (zero-code) or the SDK in code: traces, metrics, logs.',
  fields: [
    ...commonFields(SERVICE, { traces: 'stable', metrics: 'stable', logs: 'stable' }),
    {
      key: 'method',
      label: 'Instrumentation',
      defaultValue: 'zero-code-unix',
      options: [
        { value: 'zero-code-unix', label: 'Zero-code - Linux/macOS installer' },
        { value: 'zero-code-windows', label: 'Zero-code - Windows (PowerShell)' },
        { value: 'zero-code-nuget', label: 'Zero-code - NuGet package (containers)' },
        { value: 'sdk', label: 'SDK in code (ASP.NET Core)' },
      ],
      helpText:
        'Zero-code needs no code changes. The NuGet package suits containers and self-contained ' +
        'apps; SDK in code gives full control.',
    },
    RUN_MODE_FIELD,
  ],
  build: ({ baseUrl, apiKey, fieldValues }) => {
    const common = readCommon(fieldValues, SERVICE);
    const method = readMethod(fieldValues.method);
    const mode = readRunMode(fieldValues);
    const env = otelEnv({ baseUrl, apiKey, common });

    const dockerCommand =
      method === 'zero-code-nuget'
        ? ['ENTRYPOINT ["./instrument.sh", "dotnet", "MyNetApp.dll"]']
        : undefined;
    const rendered = renderEnvFiles({
      mode,
      env,
      shellIntro: 'Load before starting the app (source obzly-otel.env).',
      dockerfileCommand: dockerCommand,
    });

    let setup: DataSourceConfigFile[];
    if (method === 'sdk') setup = sdkFiles(common.signals);
    else if (method === 'zero-code-windows')
      setup = [{ filename: 'install.ps1', language: 'powershell', content: WINDOWS_INSTALL }];
    else if (method === 'zero-code-nuget')
      setup = [{ filename: 'install.sh', language: 'bash', content: NUGET_INSTALL }];
    else setup = [{ filename: 'install.sh', language: 'bash', content: UNIX_INSTALL }];

    const windowsHint =
      method === 'zero-code-windows' && mode === 'shell'
        ? ' On Windows set the same variables with $env:NAME = "value" (or the service/IIS ' +
          'environment) instead of export.'
        : '';
    const dockerHint =
      mode === 'docker' && method !== 'zero-code-nuget' && method !== 'sdk'
        ? ' In containers, prefer the NuGet package option (or the OpenTelemetry Operator entry ' +
          'on Kubernetes).'
        : '';

    return multiFileResult(
      [...setup, ...rendered.files],
      `${rendered.howTo}${windowsHint}${dockerHint} ${TEMPORALITY_NOTE}`,
    );
  },
};
