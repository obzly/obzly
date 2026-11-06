// Source: https://opentelemetry.io/docs/languages/erlang/exporters/ , .../erlang/resources/ and the
// opentelemetry_exporter README linked from them
import {
  RUN_MODE_FIELD,
  commonFields,
  multiFileResult,
  otelEnv,
  readCommon,
  readRunMode,
  renderEnvFiles,
} from './languageSdkCommon';
import type { DataSourceConfigFile, DataSourceDefinition } from './types';

const SERVICE = 'my_elixir_app';

// Version requirements as the docs give them; the exporter goes first so it
// starts before the SDK.
const ELIXIR_DEPS = `# mix.exs
def deps do
  [
    {:opentelemetry_exporter, "~> 1.8"},
    {:opentelemetry_api, "~> 1.4"},
    {:opentelemetry, "~> 1.5"}
  ]
end

# In a release, start the exporter before the SDK:
#   releases: [
#     my_app: [applications: [opentelemetry_exporter: :permanent, opentelemetry: :temporary]]
#   ]
`;

const ERLANG_DEPS = `%% rebar.config
{deps, [{opentelemetry_exporter, "~> 1.8"},
        {opentelemetry_api, "~> 1.4"},
        {opentelemetry, "~> 1.5"}]}.

%% In the release, start the exporter before the SDK:
{relx, [{release, {my_app, "0.1.0"},
         [opentelemetry_exporter,
          {opentelemetry, temporary},
          my_app]}]}.
`;

// The exporter's protocol env value is http_protobuf rather than the spec's
// http/protobuf, so the protocol is set in config (its default anyway).
// Endpoint and API key come from OTEL_EXPORTER_OTLP_ENDPOINT/_HEADERS.
const ELIXIR_CONFIG = `# config/runtime.exs
config :opentelemetry,
  span_processor: :batch,
  traces_exporter: :otlp

config :opentelemetry_exporter,
  otlp_protocol: :http_protobuf
`;

const ERLANG_CONFIG = `%% config/sys.config.src
[
 {opentelemetry,
  [{span_processor, batch},
   {traces_exporter, otlp}]},
 {opentelemetry_exporter,
  [{otlp_protocol, http_protobuf}]}
].
`;

export const erlangSdkDataSource: DataSourceDefinition = {
  id: 'lang-erlang',
  name: 'Erlang / Elixir',
  category: 'Languages',
  description: 'opentelemetry + opentelemetry_exporter for the BEAM: traces over OTLP/HTTP.',
  fields: [
    ...commonFields(SERVICE, { traces: 'stable' }),
    {
      key: 'flavor',
      label: 'Language',
      defaultValue: 'elixir',
      options: [
        { value: 'elixir', label: 'Elixir (mix)' },
        { value: 'erlang', label: 'Erlang (rebar3)' },
      ],
    },
    RUN_MODE_FIELD,
  ],
  build: ({ baseUrl, apiKey, fieldValues }) => {
    const common = readCommon(fieldValues, SERVICE, ['traces']);
    const mode = readRunMode(fieldValues);
    const env = otelEnv({
      baseUrl,
      apiKey,
      common,
      supported: ['traces'],
      setProtocol: false,
    });
    const erlang = fieldValues.flavor === 'erlang';
    const setup: DataSourceConfigFile[] = erlang
      ? [
          { filename: 'rebar.config', language: 'erlang', content: ERLANG_DEPS },
          { filename: 'sys.config.src', language: 'erlang', content: ERLANG_CONFIG },
        ]
      : [
          { filename: 'mix.exs', language: 'elixir', content: ELIXIR_DEPS },
          { filename: 'runtime.exs', language: 'elixir', content: ELIXIR_CONFIG },
        ];
    const rendered = renderEnvFiles({
      mode,
      env,
      shellIntro: 'Load before starting the release (source obzly-otel.env).',
    });
    return multiFileResult(
      [...setup, ...rendered.files],
      `${rendered.howTo} Add instrumentation libraries for your stack (for example ` +
        `opentelemetry_phoenix, opentelemetry_ecto) from hex.pm. Erlang/Elixir metrics and logs ` +
        `are still in development upstream, so only traces are set up here.`,
    );
  },
};
