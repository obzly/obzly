import { describe, expect, it } from 'vitest';

import { browserJsSdkDataSource } from './browserJsSdkDataSource';
import { dataSourceCatalog, dataSourceCategoryOrder } from './catalog';
import { GATEWAY_CONFIG_FILE, GATEWAY_ENV_FILE, gatewayCollectorYaml } from './clientGateway';
import { cppSdkDataSource } from './cppSdkDataSource';
import { dotnetSdkDataSource } from './dotnetSdkDataSource';
import { erlangSdkDataSource } from './erlangSdkDataSource';
import { goSdkDataSource } from './goSdkDataSource';
import { javaSdkDataSource } from './javaSdkDataSource';
import { kotlinSdkDataSource } from './kotlinSdkDataSource';
import { nodejsSdkDataSource } from './nodejsSdkDataSource';
import { otelOperatorDataSource } from './otelOperatorDataSource';
import { phpSdkDataSource } from './phpSdkDataSource';
import { pythonSdkDataSource } from './pythonSdkDataSource';
import { rubySdkDataSource } from './rubySdkDataSource';
import { rustSdkDataSource } from './rustSdkDataSource';
import { swiftSdkDataSource } from './swiftSdkDataSource';
import type { DataSourceConfigFile, DataSourceConfigResult, DataSourceDefinition } from './types';

const BASE_URL = 'https://obzly.example.com';
const API_KEY = 'obz_0123456789abcdef0123456789abcdef'; // gitleaks:allow (test fixture)

const LANGUAGE_SOURCES: DataSourceDefinition[] = [
  javaSdkDataSource,
  dotnetSdkDataSource,
  pythonSdkDataSource,
  nodejsSdkDataSource,
  browserJsSdkDataSource,
  goSdkDataSource,
  phpSdkDataSource,
  rubySdkDataSource,
  rustSdkDataSource,
  cppSdkDataSource,
  erlangSdkDataSource,
  swiftSdkDataSource,
  kotlinSdkDataSource,
  otelOperatorDataSource,
];

/** Every combination of the definition's dropdowns and checkboxes; text fields keep defaults. */
function allFieldCombinations(definition: DataSourceDefinition): Record<string, string>[] {
  let combos: Record<string, string>[] = [{}];
  for (const field of definition.fields) {
    const values =
      field.type === 'checkbox'
        ? ['yes', 'no']
        : field.options
          ? field.options.map((option) => option.value)
          : [field.defaultValue];
    combos = combos.flatMap((combo) => values.map((value) => ({ ...combo, [field.key]: value })));
  }
  return combos;
}

function build(definition: DataSourceDefinition, fieldValues: Record<string, string>) {
  return definition.build({ baseUrl: BASE_URL, apiKey: API_KEY, fieldValues });
}

function filesOf(result: DataSourceConfigResult): DataSourceConfigFile[] {
  return result.files ?? [result];
}

function allText(result: DataSourceConfigResult): string {
  return [...filesOf(result).map((file) => file.content), result.instructions].join('\n');
}

function count(haystack: string, needle: string): number {
  return haystack.split(needle).length - 1;
}

function isClientSide(definition: DataSourceDefinition, fieldValues: Record<string, string>) {
  return (
    definition === browserJsSdkDataSource ||
    definition === swiftSdkDataSource ||
    (definition === kotlinSdkDataSource && fieldValues.target === 'kmp')
  );
}

// Minimal YAML reader for the collector configs generated here (block maps,
// "- scalar" lists, [flow, lists], quoted scalars, comments). Throws on
// anything it doesn't understand, e.g. inconsistent indentation.
type YamlValue = string | YamlValue[] | { [key: string]: YamlValue };

function parseScalar(raw: string): YamlValue {
  const value = raw.trim();
  if (value.startsWith('"')) return JSON.parse(value) as string;
  if (value.startsWith('[')) {
    if (!value.endsWith(']')) throw new Error(`bad flow list: ${value}`);
    return value
      .slice(1, -1)
      .split(',')
      .map((item) => item.trim())
      .filter(Boolean);
  }
  return value;
}

function parseYaml(text: string): YamlValue {
  const lines = text
    .split('\n')
    .filter((line) => line.trim() !== '' && !line.trim().startsWith('#'))
    .map((line) => ({ indent: line.length - line.trimStart().length, body: line.trim() }));
  let index = 0;

  function parseBlock(indent: number): YamlValue {
    if (lines[index].body.startsWith('- ')) {
      const list: YamlValue[] = [];
      while (index < lines.length && lines[index].indent === indent) {
        const { body } = lines[index];
        if (!body.startsWith('- ')) throw new Error(`expected list item: ${body}`);
        list.push(parseScalar(body.slice(2)));
        index += 1;
      }
      return list;
    }
    const map: { [key: string]: YamlValue } = {};
    while (index < lines.length && lines[index].indent === indent) {
      const { body } = lines[index];
      const match = /^([A-Za-z0-9_.-]+):(?: (.*))?$/.exec(body);
      if (!match) throw new Error(`bad mapping line: ${body}`);
      const [, key, rest] = match;
      if (key in map) throw new Error(`duplicate key: ${key}`);
      index += 1;
      if (rest !== undefined && rest !== '') {
        map[key] = parseScalar(rest);
      } else {
        if (index >= lines.length || lines[index].indent <= indent)
          throw new Error(`empty value for ${key}`);
        map[key] = parseBlock(lines[index].indent);
      }
    }
    if (index < lines.length && lines[index].indent > indent)
      throw new Error(`unexpected indentation at: ${lines[index].body}`);
    return map;
  }

  const result = parseBlock(0);
  if (index !== lines.length) throw new Error(`trailing content at: ${lines[index].body}`);
  return result;
}

function get(value: YamlValue, ...path: string[]): YamlValue {
  let current = value;
  for (const key of path) {
    if (typeof current !== 'object' || Array.isArray(current)) throw new Error(`no ${key}`);
    current = current[key];
  }
  return current;
}

describe('language data sources', () => {
  it('Test_LanguagesCategory_IsListedBeforeApplicationRuntimes', () => {
    const order = dataSourceCategoryOrder;
    expect(order.indexOf('Languages')).toBe(order.indexOf('Application Runtimes') - 1);
    for (const definition of LANGUAGE_SOURCES) {
      expect(definition.category).toBe('Languages');
      expect(dataSourceCatalog).toContain(definition);
    }
    const ids = dataSourceCatalog.map((definition) => definition.id);
    expect(new Set(ids).size).toBe(ids.length);
  });

  for (const definition of LANGUAGE_SOURCES) {
    describe(definition.name, () => {
      const combos = allFieldCombinations(definition);

      it('Test_Build_EveryOptionCombination_DoesNotThrow', () => {
        for (const fieldValues of combos) {
          const result = build(definition, fieldValues);
          expect(filesOf(result).length).toBeGreaterThan(0);
          expect(result.instructions.length).toBeGreaterThan(0);
        }
      });

      it('Test_Build_PlacesKeyAndEndpointExactlyOnce', () => {
        for (const fieldValues of combos) {
          const result = build(definition, fieldValues);
          const text = allText(result);
          expect(count(text, API_KEY), JSON.stringify(fieldValues)).toBe(1);
          expect(count(text, BASE_URL), JSON.stringify(fieldValues)).toBe(1);
          // SDKs append /v1/<signal> themselves.
          expect(text).not.toContain(`${BASE_URL}/v1/`);

          if (isClientSide(definition, fieldValues)) {
            expect(text).toContain(`OBZLY_API_KEY=${API_KEY}\n`);
            expect(text).toContain(`endpoint: "${BASE_URL}"`);
          } else if (definition === otelOperatorDataSource) {
            expect(text).toContain(`OTEL_EXPORTER_OTLP_HEADERS: "X-API-Key=${API_KEY}"`);
            expect(text).toContain(`endpoint: "${BASE_URL}"`);
          } else {
            expect(text).toMatch(
              new RegExp(`OTEL_EXPORTER_OTLP_HEADERS(=|: |=")"?X-API-Key=${API_KEY}`),
            );
            expect(text).toMatch(
              new RegExp(`OTEL_EXPORTER_OTLP_ENDPOINT(="|\\n\\s+value: ")${BASE_URL}"`),
            );
            if (definition === erlangSdkDataSource) expect(text).toContain('http_protobuf');
            else
              expect(text).toMatch(/OTEL_EXPORTER_OTLP_PROTOCOL(="|\n\s+value: ")http\/protobuf"/);
          }
        }
      });
    });
  }

  it('Test_ClientSideTemplates_KeepKeyOutOfClientCode', () => {
    const clientSources = [browserJsSdkDataSource, swiftSdkDataSource, kotlinSdkDataSource];
    for (const definition of clientSources) {
      for (const fieldValues of allFieldCombinations(definition)) {
        if (!isClientSide(definition, fieldValues)) continue;
        const files = filesOf(build(definition, fieldValues));
        const gatewayNames = [GATEWAY_CONFIG_FILE, GATEWAY_ENV_FILE];
        expect(files.map((file) => file.filename)).toEqual(expect.arrayContaining(gatewayNames));
        for (const file of files.filter((f) => !gatewayNames.includes(f.filename))) {
          expect(file.content).not.toContain(API_KEY);
          expect(file.content).not.toContain(BASE_URL);
          expect(file.content).not.toMatch(/X-API-Key|OTEL_EXPORTER_OTLP_HEADERS/i);
          if (/\.(js|swift|kt)$/.test(file.filename) && file.filename !== 'Package.swift')
            expect(file.content).toContain('https://otel-gateway.example.com');
        }
        // The key lives only in the server-side env file.
        const envFile = files.find((file) => file.filename === GATEWAY_ENV_FILE);
        expect(envFile?.content).toContain(API_KEY);
        const config = files.find((file) => file.filename === GATEWAY_CONFIG_FILE);
        expect(config?.content).not.toContain(API_KEY);
        expect(config?.content).toContain('${env:OBZLY_API_KEY}');
      }
    }
  });

  it('Test_GatewayCollectorYaml_Parses', () => {
    const origins = ['https://app.example.com', 'https://*.example.com'];
    const browser = parseYaml(gatewayCollectorYaml(BASE_URL, origins));
    expect(get(browser, 'receivers', 'otlp', 'protocols', 'http', 'endpoint')).toBe('0.0.0.0:4318');
    expect(
      get(browser, 'receivers', 'otlp', 'protocols', 'http', 'cors', 'allowed_origins'),
    ).toEqual(origins);
    expect(get(browser, 'exporters', 'otlp_http', 'endpoint')).toBe(BASE_URL);
    expect(get(browser, 'exporters', 'otlp_http', 'headers', 'X-API-Key')).toBe(
      '${env:OBZLY_API_KEY}',
    );
    for (const signal of ['traces', 'metrics', 'logs']) {
      expect(get(browser, 'service', 'pipelines', signal, 'receivers')).toEqual(['otlp']);
      expect(get(browser, 'service', 'pipelines', signal, 'processors')).toEqual([
        'memory_limiter',
        'batch',
      ]);
      expect(get(browser, 'service', 'pipelines', signal, 'exporters')).toEqual(['otlp_http']);
    }

    expect(get(browser, 'service', 'extensions')).toEqual(['health_check']);
    expect(get(browser, 'extensions', 'health_check', 'endpoint')).toBe('0.0.0.0:13133');
    expect(get(browser, 'processors', 'memory_limiter', 'limit_mib')).toBe('512');

    const app = parseYaml(gatewayCollectorYaml(BASE_URL));
    expect(get(app, 'receivers', 'otlp', 'protocols', 'http')).toEqual({
      endpoint: '0.0.0.0:4318',
    });
  });

  it('Test_GatewayFromBrowserEntry_UsesTheChosenOrigins', () => {
    const result = build(browserJsSdkDataSource, {
      ...Object.fromEntries(browserJsSdkDataSource.fields.map((f) => [f.key, f.defaultValue])),
      allowedOrigins: 'https://shop.example.com, https://admin.example.com',
      gatewayUrl: 'https://otel.example.net/',
    });
    const files = filesOf(result);
    const config = parseYaml(files.find((f) => f.filename === GATEWAY_CONFIG_FILE)!.content);
    expect(
      get(config, 'receivers', 'otlp', 'protocols', 'http', 'cors', 'allowed_origins'),
    ).toEqual(['https://shop.example.com', 'https://admin.example.com']);
    expect(files.find((f) => f.filename === 'tracing.js')?.content).toContain(
      '"https://otel.example.net/v1/traces"',
    );
  });

  it('Test_SignalCheckboxes_DriveTheExporterEnvVars', () => {
    const defaults = Object.fromEntries(
      pythonSdkDataSource.fields.map((f) => [f.key, f.defaultValue]),
    );
    const text = allText(build(pythonSdkDataSource, defaults));
    expect(text).toContain('export OTEL_TRACES_EXPORTER="otlp"');
    expect(text).toContain('export OTEL_METRICS_EXPORTER="otlp"');
    expect(text).toContain('export OTEL_LOGS_EXPORTER="none"');
    expect(text).not.toContain('OTEL_PYTHON_LOGGING_AUTO_INSTRUMENTATION_ENABLED');

    const withLogs = allText(build(pythonSdkDataSource, { ...defaults, sendLogs: 'yes' }));
    expect(withLogs).toContain('export OTEL_LOGS_EXPORTER="otlp"');
    expect(withLogs).toContain('OTEL_PYTHON_LOGGING_AUTO_INSTRUMENTATION_ENABLED="true"');
  });

  it('Test_KubernetesRunMode_ReferencesTheKeyFromASecret', () => {
    const defaults = Object.fromEntries(
      javaSdkDataSource.fields.map((f) => [f.key, f.defaultValue]),
    );
    const files = filesOf(build(javaSdkDataSource, { ...defaults, runMode: 'kubernetes' }));
    const secret = files.find((f) => f.filename === 'obzly-otel-secret.yaml')!.content;
    const env = files.find((f) => f.filename === 'deployment-env.yaml')!.content;
    expect(secret).toContain(`OTEL_EXPORTER_OTLP_HEADERS: "X-API-Key=${API_KEY}"`);
    expect(env).not.toContain(API_KEY);
    expect(env).toContain('secretKeyRef:\n        name: my-java-service-obzly-otel');
    expect(get(parseYaml(secret), 'stringData', 'OTEL_EXPORTER_OTLP_HEADERS')).toBe(
      `X-API-Key=${API_KEY}`,
    );
  });
});
