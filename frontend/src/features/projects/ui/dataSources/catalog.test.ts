import { describe, expect, it } from 'vitest';
import { parseAllDocuments } from 'yaml';

import { dataSourceCatalog } from './catalog';
import { GATEWAY_CONFIG_FILE } from './clientGateway';
import { COLLECTOR_IMAGE } from './otelCollectorCommon';
import type { DataSourceConfigFile, DataSourceDefinition } from './types';

const CTX = { baseUrl: 'https://obzly.example.com', apiKey: 'obz_test_key' };

// Field values that change the output beyond each field's own options.
const EXTRA_VARIANTS: Record<string, Record<string, string>[]> = {
  kubernetes: [
    {
      clusterName: 'contoso-prod',
      includeNamespaces: 'prod-*, checkout',
      excludeNamespaces: 'kube-system',
      excludePods: 'noisy-*',
      hostNetwork: 'yes',
      limitUtilization: 'yes',
    },
  ],
  'obi-network': [
    { enableTraces: 'yes', tracePermissions: 'capabilities', traceNamespaces: 'prod-*' },
    { enableTraces: 'yes', tracePermissions: 'selinux' },
    { enableTraces: 'yes', tracePermissions: 'selinux-sysadmin' },
    { enableTraces: 'yes', tracePermissions: 'privileged', enableK8sCache: 'yes' },
    { clusterName: 'contoso-prod' },
  ],
  windows: [{ channels: 'Application, System, Security' }],
  syslog: [{ protocol: 'tcp', port: '5514', format: 'rfc5424' }],
  postgresql: [{ logPath: '', password: 'secret' }],
  mysql: [{ logPath: '', password: 'secret' }],
  mongodb: [{ logPath: '', password: 'secret' }],
  haproxy: [{ logPath: '' }],
  redis: [{ password: 'secret' }],
  valkey: [{ password: 'secret' }],
  elasticsearch: [{ username: 'elastic', password: 'secret' }],
  consul: [{ aclToken: 'token' }],
  spark: [{ applicationNames: 'etl, reports' }],
  kafka: [{ brokers: 'kafka-1.example.com:9092, kafka-2.example.com:9092' }],
  prometheus: [{ targets: 'localhost:9100, localhost:9200', metricsPath: '/prom' }],
  'claude-code': [{ team: 'platform', logPrompts: 'yes' }],
};

function defaults(source: DataSourceDefinition): Record<string, string> {
  return Object.fromEntries(source.fields.map((f) => [f.key, f.defaultValue]));
}

// Defaults, every option/checkbox value on its own, plus EXTRA_VARIANTS.
function variants(source: DataSourceDefinition): Record<string, string>[] {
  const base = defaults(source);
  const out = [base];
  for (const field of source.fields) {
    const values = field.options?.map((o) => o.value) ?? (field.type ? ['yes', 'no'] : []);
    for (const value of values) {
      if (value !== field.defaultValue) out.push({ ...base, [field.key]: value });
    }
  }
  for (const extra of EXTRA_VARIANTS[source.id] ?? []) out.push({ ...base, ...extra });
  return out;
}

function render(source: DataSourceDefinition, fieldValues: Record<string, string>) {
  const result = source.build({ ...CTX, fieldValues });
  const files: DataSourceConfigFile[] = result.files ?? [
    { filename: result.filename, language: result.language, content: result.content },
  ];
  return { result, files };
}

const rendered = dataSourceCatalog.flatMap((source) =>
  variants(source).map((fieldValues, index) => ({
    source,
    index,
    ...render(source, fieldValues),
  })),
);

describe('data source catalog', () => {
  it('Test_DataSourceCatalog_EveryVariant_Renders', () => {
    for (const { source, result, files } of rendered) {
      expect(result.instructions, source.id).not.toBe('');
      for (const file of files) expect(file.content, `${source.id} ${file.filename}`).not.toBe('');
    }
  });

  it('Test_DataSourceCatalog_YamlOutputs_Parse', () => {
    for (const { source, files } of rendered) {
      for (const file of files.filter((f) => f.language === 'yaml')) {
        const docs = parseAllDocuments(file.content);
        const errors = docs.flatMap((d) => d.errors.map((e) => e.message));
        expect(errors, `${source.id} ${file.filename}`).toEqual([]);
      }
    }
  });

  it('Test_DataSourceCatalog_CollectorVersion_IsCollectorImage', () => {
    const version = COLLECTOR_IMAGE.split(':')[1];
    for (const { source, files } of rendered) {
      for (const file of files) {
        for (const match of file.content.matchAll(
          /otel\/opentelemetry-collector-contrib:[\w.-]+/g,
        )) {
          expect(match[0], source.id).toBe(COLLECTOR_IMAGE);
        }
        for (const match of file.content.matchAll(/VERSION="([\d.]+)"/g)) {
          expect(match[1], source.id).toBe(version);
        }
      }
    }
  });

  it('Test_DataSourceCatalog_HostOtlp_IsLocalUnlessRemoteTicked', () => {
    for (const { source, files, index } of rendered) {
      // Variant 0 is the defaults (box unticked).
      if (index !== 0 || !source.fields.some((f) => f.key === 'otlpRemote')) continue;
      const remote = source.build({
        ...CTX,
        fieldValues: { ...defaults(source), otlpRemote: 'yes' },
      });
      const local = files.map((f) => f.content).join('\n');
      const remoteText = (remote.files ?? [remote]).map((f) => f.content).join('\n');
      if (source.id === 'docker') {
        // Inside the container it must listen on the container IP; the host
        // publishes the ports on loopback unless ticked.
        expect(local, source.id).toContain('"127.0.0.1:4317:4317"');
        expect(remoteText, source.id).toContain('"4317:4317"');
        expect(remoteText, source.id).not.toContain('127.0.0.1:4317');
        continue;
      }
      expect(local, source.id).toContain('endpoint: 127.0.0.1:4317');
      expect(local, source.id).not.toMatch(/0\.0\.0\.0:431[78]/);
      expect(remoteText, source.id).toContain('endpoint: 0.0.0.0:4318');
    }
    // Every collector template outside Kubernetes offers the choice, except
    // the client gateway, which serves browsers and phones and must be public.
    const withCollector = dataSourceCatalog.filter((s) => {
      const files = render(s, defaults(s)).files;
      return (
        files.some((f) => /^receivers:/m.test(f.content)) &&
        !files.some((f) => f.filename === GATEWAY_CONFIG_FILE)
      );
    });
    for (const source of withCollector) {
      expect(
        source.fields.map((f) => f.key),
        source.id,
      ).toContain('otlpRemote');
    }
  });

  it('Test_DataSourceCatalog_Docker_ReadsHostMetricsThroughHostfs', () => {
    const docker = rendered.find((r) => r.source.id === 'docker')!;
    const [config, compose] = docker.files.map((f) => f.content);
    expect(config).toMatch(/host_metrics:\n {4}root_path: \/hostfs/);
    expect(config).toContain('receivers: [docker_stats, host_metrics, otlp]');
    for (const scraper of ['cpu', 'memory', 'load', 'filesystem', 'network', 'disk', 'paging']) {
      expect(config).toContain(`      ${scraper}: {}`);
    }
    expect(compose).toContain('- /:/hostfs:ro\n');
  });

  it('Test_DataSourceCatalog_Collectors_ShareConventions', () => {
    for (const { source, files } of rendered) {
      for (const file of files) {
        if (!/^exporters:/m.test(file.content) && !file.content.includes('    exporters:'))
          continue;
        const where = `${source.id} ${file.filename}`;
        expect(file.content, where).toContain('X-API-Key: "${env:OBZLY_API_KEY}"');
        expect(file.content, where).toContain('send_batch_max_size: 2500');
        expect(file.content, where).toMatch(/processors: \[memory_limiter, /);
        expect(file.content, where).not.toMatch(/processors: \[(?!memory_limiter)/);
        expect(file.content, where).toMatch(/batch\]\n/);
        expect(file.content, where).toContain('health_check');
        // Tailed files keep their read offsets across restarts.
        if (/^\s+(file_log|journald|windows_event_log)[/:]/m.test(file.content)) {
          expect(file.content, where).toContain('storage: file_storage');
        }
      }
    }
  });

  // OBZLY_DATASOURCE_OUT=<dir> npx vitest run catalog writes every rendered
  // file there, for `otelcol-contrib validate` and kubeconform. The app's
  // types are browser only, hence Node's fs through a dynamic import.
  const out = (globalThis as { process?: { env: Record<string, string | undefined> } }).process?.env
    .OBZLY_DATASOURCE_OUT;
  it.skipIf(!out)('writes every rendered file', async () => {
    const fsModule: string = 'node:fs';
    const fs = await import(/* @vite-ignore */ fsModule);
    for (const { source, index, files } of rendered) {
      const dir = `${out}/${source.id}--${index}`;
      fs.mkdirSync(dir, { recursive: true });
      for (const file of files) fs.writeFileSync(`${dir}/${file.filename}`, file.content);
    }
  });
});
